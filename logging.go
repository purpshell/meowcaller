package meowcaller

import (
	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
	"time"
)

// Option configures optional, non-behavioral aspects of the call/media types —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log                           zerolog.Logger
	diag                          *diag.Recorder
	incomingAcceptFallbackTimeout time.Duration
}

func resolveConfig(opts []Option) config {
	c := config{log: zerolog.Nop(), incomingAcceptFallbackTimeout: 1500 * time.Millisecond}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithIncomingAcceptFallbackTimeout sets how long an answered incoming call waits
// for the peer's optional mute_v2 signal after relay transport is ready.
func WithIncomingAcceptFallbackTimeout(timeout time.Duration) Option {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/call-mute.yaml#L22-L34
	return func(c *config) {
		if timeout > 0 {
			c.incomingAcceptFallbackTimeout = timeout
		}
	}
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
