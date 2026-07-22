package rtp

import (
	"encoding/binary"
	"sync"

	"github.com/rs/zerolog"
)

// RTCP: WhatsApp compact reports (PT 208/209) and a Sender Report (PT 200). The
// SR's NTP timestamp is taken as a nowMs argument so this stays pure/no-clock.

const (
	RtcpPtSr         uint8 = 200
	RtcpPtSdes       uint8 = 202
	RtcpPtPsfb       uint8 = 206
	RtcpPtWaCompact  uint8 = 208
	RtcpPtWaCompact2 uint8 = 209
	RtcpHeaderLen    int   = 8
	SrtcpTrailerLen  int   = 14

	ntpUnixOffsetSecs    uint64 = 2208988800
	WhatsappRtcpCnameLen        = 18
)

// IsRtcpPacket reports whether data is an RTCP packet (vs a WhatsApp RTP packet).
func IsRtcpPacket(data []byte) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/rtcp.rs#L16-L28
	if len(data) < RtcpHeaderLen {
		return false
	}
	if (data[0]>>6)&0x03 != 2 {
		return false
	}
	return data[1] >= 192 && data[1] <= 223
}

// RtcpPayloadType returns the RTCP payload type; ok=false if not an RTCP packet.
func RtcpPayloadType(data []byte) (uint8, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/rtcp.rs#L30-L32
	if !IsRtcpPacket(data) {
		return 0, false
	}
	return data[1], true
}

// ParseRtcpSenderSsrc returns the sender SSRC (bytes 4-7); ok=false if malformed.
func ParseRtcpSenderSsrc(data []byte, log ...zerolog.Logger) (uint32, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/rtcp.rs#L34-L39
	lg := pickLog(log)
	if len(data) < 8 || (data[0]>>6)&0x03 != 2 {
		lg.Trace().Int("packet_bytes", len(data)).Msg("parse rtcp sender ssrc: malformed packet")
		return 0, false
	}
	ssrc := binary.BigEndian.Uint32(data[4:8])
	lg.Trace().Uint32("ssrc", ssrc).Msg("parsed rtcp sender ssrc")
	return ssrc, true
}

// RtcpSenderStats are the Sender Report counters.
type RtcpSenderStats struct {
	PacketsSent  uint32
	OctetsSent   uint32
	RtpTimestamp uint32
}

// RtcpReceptionReport is the RFC 3550 receive-stream state carried in a Sender
// Report. WhatsApp appends 24 bytes of transport statistics after this block.
type RtcpReceptionReport struct {
	Ssrc                       uint32
	FractionLost               uint8
	CumulativeLost             int32
	ExtendedHighestSequence    uint32
	Jitter                     uint32
	LastSenderReport           uint32
	DelaySinceLastSenderReport uint32
}

// RtcpReceptionStats tracks one inbound RTP stream and produces interval loss,
// jitter, LSR, and DLSR fields for periodic reception reports. Its methods are
// safe to call from the media receive loop and the RTCP ticker concurrently.
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d37b1756d05fb34c9b6c2410c48dd20d27394929/wacore/src/voip/rtcp.rs#L255-L388
type RtcpReceptionStats struct {
	mu sync.Mutex

	ssrc                 uint32
	hasSsrc              bool
	baseSequence         uint16
	maxSequence          uint16
	sequenceCycles       uint32
	received             uint32
	expectedPrior        uint32
	receivedPrior        uint32
	transit              uint32
	hasTransit           bool
	jitterQ4             uint64
	lastSenderReport     uint32
	lastSenderReportAtMs uint64
	hasSenderReport      bool
}

// Observe records one authenticated inbound RTP packet.
func (s *RtcpReceptionStats) Observe(ssrc uint32, sequence uint16, rtpTimestamp uint32, arrivalMs uint64, clockRate uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSsrc || s.ssrc != ssrc {
		s.sequenceCycles = 0
		s.expectedPrior = 0
		s.receivedPrior = 0
		s.hasTransit = false
		s.jitterQ4 = 0
		s.lastSenderReport = 0
		s.hasSenderReport = false
		s.ssrc = ssrc
		s.hasSsrc = true
		s.baseSequence = sequence
		s.maxSequence = sequence
		s.received = 1
	} else {
		delta := sequence - s.maxSequence
		if delta != 0 && delta < 0x8000 {
			if sequence < s.maxSequence {
				s.sequenceCycles += 1 << 16
			}
			s.maxSequence = sequence
		}
		s.received++
	}

	arrivalRtp := uint32((uint64(arrivalMs) * uint64(clockRate)) / 1000)
	transit := arrivalRtp - rtpTimestamp
	if s.hasTransit {
		delta := int32(transit - s.transit)
		distance := uint64(delta)
		if delta < 0 {
			distance = uint64(-int64(delta))
		}
		decay := (s.jitterQ4 + 8) >> 4
		s.jitterQ4 += distance
		if s.jitterQ4 >= decay {
			s.jitterQ4 -= decay
		} else {
			s.jitterQ4 = 0
		}
	}
	s.transit = transit
	s.hasTransit = true
}

// ObserveSenderReport records the compact NTP timestamp needed for LSR/DLSR.
func (s *RtcpReceptionStats) ObserveSenderReport(senderSsrc, ntpSeconds, ntpFraction uint32, arrivalMs uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSsrc || s.ssrc != senderSsrc {
		return
	}
	s.lastSenderReport = (ntpSeconds << 16) | (ntpFraction >> 16)
	s.lastSenderReportAtMs = arrivalMs
	s.hasSenderReport = true
}

// Report snapshots interval and cumulative reception state.
func (s *RtcpReceptionStats) Report(nowMs uint64) *RtcpReceptionReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSsrc {
		return nil
	}
	expected := s.sequenceCycles + uint32(s.maxSequence) - uint32(s.baseSequence) + 1
	expectedInterval := expected - s.expectedPrior
	receivedInterval := s.received - s.receivedPrior
	lostInterval := int64(expectedInterval) - int64(receivedInterval)
	var fractionLost uint8
	if expectedInterval != 0 && lostInterval > 0 {
		fraction := (lostInterval * 256) / int64(expectedInterval)
		if fraction > 255 {
			fraction = 255
		}
		fractionLost = uint8(fraction)
	}
	s.expectedPrior = expected
	s.receivedPrior = s.received

	cumulativeLost := int64(expected) - int64(s.received)
	if cumulativeLost < -0x80_0000 {
		cumulativeLost = -0x80_0000
	} else if cumulativeLost > 0x7f_ffff {
		cumulativeLost = 0x7f_ffff
	}
	var dlsr uint32
	if s.hasSenderReport {
		elapsed := nowMs - s.lastSenderReportAtMs
		if nowMs < s.lastSenderReportAtMs {
			elapsed = 0
		}
		value := elapsed * 65_536 / 1000
		if value > uint64(^uint32(0)) {
			value = uint64(^uint32(0))
		}
		dlsr = uint32(value)
	}
	jitter := s.jitterQ4 >> 4
	if jitter > uint64(^uint32(0)) {
		jitter = uint64(^uint32(0))
	}
	return &RtcpReceptionReport{
		Ssrc: s.ssrc, FractionLost: fractionLost, CumulativeLost: int32(cumulativeLost),
		ExtendedHighestSequence: s.sequenceCycles | uint32(s.maxSequence),
		Jitter:                  uint32(jitter),
		LastSenderReport:        s.lastSenderReport, DelaySinceLastSenderReport: dlsr,
	}
}

// ParseSenderReportTiming extracts the first SR sender SSRC and NTP timestamp
// from a compound RTCP packet.
func ParseSenderReportTiming(data []byte) (senderSsrc, ntpSeconds, ntpFraction uint32, ok bool) {
	for offset := 0; offset < len(data); {
		if offset+4 > len(data) {
			return 0, 0, 0, false
		}
		packetLen := (int(binary.BigEndian.Uint16(data[offset+2:offset+4])) + 1) * 4
		if packetLen < 4 || offset+packetLen > len(data) {
			return 0, 0, 0, false
		}
		packet := data[offset : offset+packetLen]
		if packet[1] == RtcpPtSr && len(packet) >= 28 {
			return binary.BigEndian.Uint32(packet[4:8]), binary.BigEndian.Uint32(packet[8:12]), binary.BigEndian.Uint32(packet[12:16]), true
		}
		offset += packetLen
	}
	return 0, 0, 0, false
}

// BuildCompactRtcp208 builds the 12-byte compact RTCP (PT 208, RC=1).
func BuildCompactRtcp208(localSsrc, remoteSsrc uint32, log ...zerolog.Logger) [12]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/rtcp.rs#L49-L58
	var buf [12]byte
	buf[0] = 0x81 // V=2, P=0, RC=1
	buf[1] = RtcpPtWaCompact
	buf[3] = 2 // (2+1)*4 = 12 bytes
	binary.BigEndian.PutUint32(buf[4:8], localSsrc)
	binary.BigEndian.PutUint32(buf[8:12], remoteSsrc)
	lg := pickLog(log)
	lg.Trace().Uint32("local_ssrc", localSsrc).Uint32("remote_ssrc", remoteSsrc).Uint8("payload_type", RtcpPtWaCompact).Msg("built compact rtcp 208")
	return buf
}

// BuildCompactRtcp209 builds the 8-byte compact RTCP (PT 209, RC=1).
func BuildCompactRtcp209(localSsrc uint32, log ...zerolog.Logger) [8]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/rtcp.rs#L61-L69
	var buf [8]byte
	buf[0] = 0x81
	buf[1] = RtcpPtWaCompact2
	buf[3] = 1 // (1+1)*4 = 8 bytes
	binary.BigEndian.PutUint32(buf[4:8], localSsrc)
	lg := pickLog(log)
	lg.Trace().Uint32("local_ssrc", localSsrc).Uint8("payload_type", RtcpPtWaCompact2).Msg("built compact rtcp 209")
	return buf
}

// BuildSenderReport builds the 28-byte Sender Report (PT 200, RC=0); nowMs is wall-clock ms.
func BuildSenderReport(localSsrc uint32, stats *RtcpSenderStats, nowMs uint64, log ...zerolog.Logger) [28]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/rtcp.rs#L72-L89
	var buf [28]byte
	buf[0] = 0x80 // V=2, RC=0
	buf[1] = RtcpPtSr
	buf[3] = 6 // (6+1)*4 = 28 bytes
	binary.BigEndian.PutUint32(buf[4:8], localSsrc)
	// NTP timestamp: seconds (upper 32) since 1900, fraction (lower 32). Both truncate
	// to u32 (wrapping), matching the reference encoder.
	ntpSec := uint32((nowMs / 1000) + ntpUnixOffsetSecs)
	ntpFrac := uint32(float64(nowMs%1000) / 1000.0 * 4294967296.0)
	binary.BigEndian.PutUint32(buf[8:12], ntpSec)
	binary.BigEndian.PutUint32(buf[12:16], ntpFrac)
	binary.BigEndian.PutUint32(buf[16:20], stats.RtpTimestamp)
	binary.BigEndian.PutUint32(buf[20:24], stats.PacketsSent)
	binary.BigEndian.PutUint32(buf[24:28], stats.OctetsSent)
	lg := pickLog(log)
	lg.Trace().Uint32("local_ssrc", localSsrc).Uint32("rtp_timestamp", stats.RtpTimestamp).Uint32("packets_sent", stats.PacketsSent).Uint32("octets_sent", stats.OctetsSent).Msg("built rtcp sender report")
	return buf
}

// BuildWhatsappRtcpCname builds the native 18-byte randomized CNAME shape.
func BuildWhatsappRtcpCname(entropy [12]byte) [WhatsappRtcpCnameLen]byte {
	const hexChars = "0123456789abcdef"
	var randomHex [11]byte
	for nibble := range randomHex {
		b := entropy[6+nibble/2]
		if nibble&1 == 0 {
			randomHex[nibble] = hexChars[b>>4]
		} else {
			randomHex[nibble] = hexChars[b&0x0f]
		}
	}
	var cname [WhatsappRtcpCnameLen]byte
	copy(cname[:5], randomHex[:5])
	copy(cname[5:8], "@pj")
	copy(cname[8:14], randomHex[5:])
	copy(cname[14:], ".org")
	return cname
}

// BuildSourceDescription builds WhatsApp's one-chunk SDES packet.
func BuildSourceDescription(localSsrc uint32, cname *[WhatsappRtcpCnameLen]byte, profileExtension bool) [32]byte {
	var packet [32]byte
	packet[0] = 0x81
	if profileExtension {
		packet[0] |= 0x10
	}
	packet[1] = RtcpPtSdes
	binary.BigEndian.PutUint16(packet[2:4], 7)
	binary.BigEndian.PutUint32(packet[4:8], localSsrc)
	packet[8] = 1
	packet[9] = WhatsappRtcpCnameLen
	copy(packet[10:28], cname[:])
	return packet
}

// BuildSenderReportWithSdes builds WhatsApp's periodic compound SR+SDES packet.
func BuildSenderReportWithSdes(localSsrc uint32, stats *RtcpSenderStats, nowMs uint64, cname *[WhatsappRtcpCnameLen]byte, profileExtension bool) []byte {
	return BuildSenderReportWithSdesAndReception(localSsrc, stats, nowMs, cname, nil, profileExtension)
}

// BuildSenderReportWithSdesAndReception builds WhatsApp's native 1:1 compound
// report. Its receive block is the RFC 3550 layout followed by 24 zero-valued
// transport/BWE fields, matching the native unavailable-value representation.
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d37b1756d05fb34c9b6c2410c48dd20d27394929/wacore/src/voip/rtcp.rs#L461-L583
func BuildSenderReportWithSdesAndReception(localSsrc uint32, stats *RtcpSenderStats, nowMs uint64, cname *[WhatsappRtcpCnameLen]byte, report *RtcpReceptionReport, profileExtension bool) []byte {
	sr := BuildSenderReport(localSsrc, stats, nowMs)
	if profileExtension {
		sr[0] |= 0x10
	}
	sdes := BuildSourceDescription(localSsrc, cname, profileExtension)
	out := make([]byte, 0, len(sr)+48+len(sdes))
	out = append(out, sr[:]...)
	if report != nil {
		out[0] |= 1
		out = appendReceptionReport(out, report)
		out = append(out, make([]byte, 24)...)
		binary.BigEndian.PutUint16(out[2:4], uint16(len(out)/4-1))
	}
	out = append(out, sdes[:]...)
	return out
}

func appendReceptionReport(out []byte, report *RtcpReceptionReport) []byte {
	out = binary.BigEndian.AppendUint32(out, report.Ssrc)
	out = append(out, report.FractionLost)
	lost := uint32(report.CumulativeLost)
	out = append(out, byte(lost>>16), byte(lost>>8), byte(lost))
	out = binary.BigEndian.AppendUint32(out, report.ExtendedHighestSequence)
	out = binary.BigEndian.AppendUint32(out, report.Jitter)
	out = binary.BigEndian.AppendUint32(out, report.LastSenderReport)
	out = binary.BigEndian.AppendUint32(out, report.DelaySinceLastSenderReport)
	return out
}

// RtcpRequestsKeyframe reports whether a compound RTCP packet contains PLI/FIR
// feedback for localVideoSsrc.
func RtcpRequestsKeyframe(data []byte, localVideoSsrc uint32) bool {
	for offset := 0; offset+4 <= len(data); {
		if (data[offset]>>6)&0x03 != 2 {
			return false
		}
		packetLen := (int(binary.BigEndian.Uint16(data[offset+2:offset+4])) + 1) * 4
		if packetLen < 8 || offset+packetLen > len(data) {
			return false
		}
		packet := data[offset : offset+packetLen]
		if packet[1] == RtcpPtPsfb && len(packet) >= 12 {
			rawFmt := packet[0] & 0x1f
			fmt := rawFmt
			if rawFmt&0x10 != 0 {
				fmt &= 0x0f
			}
			mediaSsrc := binary.BigEndian.Uint32(packet[8:12])
			if fmt == 1 && mediaSsrc == localVideoSsrc {
				return true
			}
			if fmt == 4 {
				if mediaSsrc == localVideoSsrc {
					return true
				}
				for fci := packet[12:]; len(fci) >= 8; fci = fci[8:] {
					if binary.BigEndian.Uint32(fci[:4]) == localVideoSsrc {
						return true
					}
				}
			}
		}
		offset += packetLen
	}
	return false
}
