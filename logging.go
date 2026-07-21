package meowcaller

import (
	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
)

// Option configures optional, non-behavioral aspects of the call/media types —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log             zerolog.Logger
	diag            *diag.Recorder
	ringPrimaryOnly bool
}

func resolveConfig(opts []Option) config {
	c := config{log: zerolog.Nop()}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithLogger sets the zerolog logger for debug/trace diagnostics. The library never
// configures logging itself; without this option the types are silent at zero cost.
// Pass the logger from a context, e.g. WithLogger(*zerolog.Ctx(ctx)).
func WithLogger(l zerolog.Logger) Option {
	return func(c *config) { c.log = l }
}

// WithDiagnostics attaches a developer-only *diag.Recorder that dumps exact,
// per-category call diagnostics (including raw secrets and media) to JSONL files.
// This is an opt-in maintainer carve-out from the library's sanitized logging and
// must never be enabled in production. Without it the recorder is nil and every
// diag emit is a no-op at zero cost.
func WithDiagnostics(rec *diag.Recorder) Option {
	return func(c *config) { c.diag = rec }
}

// WithPrimaryDeviceOnly makes outbound calls ring only the peer's primary device
// (device 0), like a normal 1:1 call, instead of offering to every registered companion
// device. Offering to all devices forks the media across legs and the relay bridges
// inbound RTP inconsistently for multi-device peers (the call rings everywhere but the
// callee's audio may never arrive). Off by default.
func WithPrimaryDeviceOnly() Option {
	return func(c *config) { c.ringPrimaryOnly = true }
}
