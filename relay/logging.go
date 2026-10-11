package relay

import (
	"net"

	"github.com/rs/zerolog"
)

// Option configures optional, non-behavioral aspects of the relay channel —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log        zerolog.Logger
	packetConn func(relayAddr *net.UDPAddr) (net.PacketConn, error)
}

func resolveConfig(opts []Option) config {
	c := config{log: zerolog.Nop()}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithLogger sets the zerolog logger for debug/trace diagnostics. The library never
// configures logging itself; without this option the channel is silent at zero cost.
// Pass the logger from a context, e.g. WithLogger(*zerolog.Ctx(ctx)).
func WithLogger(l zerolog.Logger) Option {
	return func(c *config) { c.log = l }
}

// WithPacketConn replaces the plain host UDP socket the media stack binds: fn is
// called once per relay connection and returns the datagram socket DTLS runs over
// (a SOCKS5 UDP association, for example). The channel closes it.
func WithPacketConn(fn func(relayAddr *net.UDPAddr) (net.PacketConn, error)) Option {
	return func(c *config) { c.packetConn = fn }
}

func pickLog(log []zerolog.Logger) zerolog.Logger {
	if len(log) > 0 {
		return log[0]
	}
	return zerolog.Nop()
}
