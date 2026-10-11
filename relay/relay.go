package relay

import (
	"fmt"
	"net"

	"github.com/pion/datachannel"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/sctp"
	"github.com/rs/zerolog"
)

// Relay media transport: a pre-negotiated WebRTC DataChannel over
// SCTP-over-DTLS-over-UDP to a single WhatsApp relay endpoint. Only
// ClassifyRelayPacket is unit-testable; the connection path talks to a live relay.

// RelayPacketKind classifies a packet seen on the relay channel by its first byte.
type RelayPacketKind int

const (
	RelayPacketStun RelayPacketKind = iota
	RelayPacketRtcp
	RelayPacketRtp
	RelayPacketOther
)

const (
	// DataChannelLabel is the pre-negotiated (id=0) DataChannel label WA Web uses.
	DataChannelLabel = "pre-negotiated"
	// SctpPort is the SCTP-over-DTLS WebRTC port (a WebRTC convention; pion's
	// sctp.Client negotiates over the DTLS conn and does not take it as config).
	SctpPort = 5000
)

// ClassifyRelayPacket demuxes STUN from RTP/RTCP using the RTP version and payload
// type ranges. WhatsApp video RTCP sets a profile bit, so its first byte can be 0x91.
func ClassifyRelayPacket(data []byte, log ...zerolog.Logger) RelayPacketKind {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/transport.rs#L57-L70
	lg := pickLog(log)
	if len(data) < 2 {
		lg.Trace().Int("packet_bytes", len(data)).Msg("relay packet too short to classify")
		return RelayPacketOther
	}
	first := data[0]
	if first&0xc0 != 0 {
		if data[1] >= 192 && data[1] <= 223 {
			lg.Trace().Int("packet_bytes", len(data)).Str("kind", "rtcp").Msg("classified relay packet")
			return RelayPacketRtcp
		}
		if first>>6 == 2 {
			lg.Trace().Int("packet_bytes", len(data)).Str("kind", "rtp").Msg("classified relay packet")
			return RelayPacketRtp
		}
		lg.Trace().Int("packet_bytes", len(data)).Str("kind", "other").Msg("classified relay packet")
		return RelayPacketOther
	}
	lg.Trace().Int("packet_bytes", len(data)).Str("kind", "stun").Msg("classified relay packet")
	return RelayPacketStun
}

// CallTransportError categorizes a relay-transport failure so a consumer can branch:
// Connect is fatal (the call can't reach the relay); Send/Recv are recoverable on an
// established channel.
type CallTransportError struct {
	Op  string // "connect", "send", or "recv"
	Err error
}

func (e *CallTransportError) Error() string { return "relay " + e.Op + ": " + e.Err.Error() }
func (e *CallTransportError) Unwrap() error { return e.Err }

// RelayMediaChannel is an open relay media channel; STUN/RTP/RTCP travel as binary
// DataChannel messages. It owns the whole stack so Close tears it down cleanly
// (the reference relies on Rust Drop; Go needs explicit cleanup).
type RelayMediaChannel struct {
	udp      net.PacketConn
	dtlsConn net.Conn
	assoc    *sctp.Association
	dc       *datachannel.DataChannel
	log      zerolog.Logger
}

// Close tears down the media stack in reverse order of construction.
func (c *RelayMediaChannel) Close() error {
	c.log.Debug().Msg("tearing down relay media channel")
	var firstErr error
	for _, closer := range []func() error{c.dc.Close, c.assoc.Close, c.dtlsConn.Close, c.udp.Close} {
		if err := closer(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		c.log.Debug().Err(firstErr).Msg("relay media channel teardown error")
	}
	return firstErr
}

// Send writes one media/STUN packet as a binary DataChannel message.
func (c *RelayMediaChannel) Send(data []byte) (int, error) {
	// NOT VALIDATED: no vector exists for the live transport; exercised only against a real relay.
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/transport.rs#L118-L124
	n, err := c.dc.Write(data)
	if err != nil {
		c.log.Debug().Err(err).Int("packet_bytes", len(data)).Msg("relay send failed")
		return n, &CallTransportError{Op: "send", Err: err}
	}
	c.log.Trace().Int("packet_bytes", n).Msg("sent relay packet")
	return n, nil
}

// Recv reads one DataChannel message into buf, returning its length.
func (c *RelayMediaChannel) Recv(buf []byte) (int, error) {
	// NOT VALIDATED: no vector exists for the live transport; exercised only against a real relay.
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/transport.rs#L126-L132
	n, err := c.dc.Read(buf)
	if err != nil {
		c.log.Debug().Err(err).Msg("relay recv failed")
		return n, &CallTransportError{Op: "recv", Err: err}
	}
	c.log.Trace().Int("packet_bytes", n).Msg("received relay packet")
	return n, nil
}

// ConnectRelayMedia connects the full media stack (UDP→DTLS→SCTP→DataChannel) to one
// relay endpoint. Self-signed cert; server-cert verification skipped (media auth is
// HBH SRTP, not DTLS). No vector — validated only against a live relay.
func ConnectRelayMedia(relayAddr *net.UDPAddr, opts ...Option) (*RelayMediaChannel, error) {
	// NOT VALIDATED: no vector exists for the live transport; exercised only against a real relay.
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/transport.rs#L136-L195
	cfg := resolveConfig(opts)
	lg := cfg.log
	lg.Debug().Str("relay_addr", relayAddr.String()).Msg("connecting relay media stack")
	// Roll back already-allocated resources if a later step fails.
	var cleanup []func() error
	fail := func(err error) (*RelayMediaChannel, error) {
		for i := len(cleanup) - 1; i >= 0; i-- {
			_ = cleanup[i]()
		}
		lg.Debug().Err(err).Msg("relay media connect failed")
		return nil, &CallTransportError{Op: "connect", Err: err}
	}

	// 1. UDP socket.
	var udp net.PacketConn
	var err error
	if cfg.packetConn != nil {
		udp, err = cfg.packetConn(relayAddr)
	} else {
		udp, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	}
	if err != nil {
		lg.Debug().Err(err).Msg("relay media connect failed")
		return nil, &CallTransportError{Op: "connect", Err: fmt.Errorf("bind udp: %w", err)}
	}
	cleanup = append(cleanup, udp.Close)
	lg.Debug().Str("local_addr", udp.LocalAddr().String()).Msg("relay udp socket bound")

	// 2. DTLS client (self-signed cert; skip server-cert verification).
	cert, err := selfsign.GenerateSelfSignedWithDNS("wa-voip")
	if err != nil {
		return fail(fmt.Errorf("dtls self-signed cert: %w", err))
	}
	dtlsConn, err := dtls.ClientWithOptions(udp, relayAddr,
		dtls.WithCertificates(cert),
		dtls.WithInsecureSkipVerify(true),
	)
	if err != nil {
		return fail(fmt.Errorf("dtls handshake: %w", err))
	}
	cleanup = append(cleanup, dtlsConn.Close)
	lg.Debug().Msg("relay dtls handshake complete")

	// 3. SCTP association over the DTLS conn.
	assoc, err := sctp.ClientWithOptions(sctp.WithNetConn(dtlsConn), sctp.WithName("wa-voip"))
	if err != nil {
		return fail(fmt.Errorf("sctp client: %w", err))
	}
	cleanup = append(cleanup, assoc.Close)
	lg.Debug().Msg("relay sctp association established")

	// 4. Pre-negotiated DataChannel id=0. LoggerFactory is required: datachannel
	// does not default it and dereferences it on construction.
	dc, err := datachannel.Dial(assoc, 0, &datachannel.Config{
		Negotiated:    true,
		Label:         DataChannelLabel,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		return fail(fmt.Errorf("datachannel dial: %w", err))
	}
	lg.Debug().Str("label", DataChannelLabel).Msg("relay datachannel open")

	return &RelayMediaChannel{udp: udp, dtlsConn: dtlsConn, assoc: assoc, dc: dc, log: lg}, nil
}
