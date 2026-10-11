package meowcaller

import (
	"time"

	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
)

// Option configures optional, non-behavioral aspects of the call/media types —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log  zerolog.Logger
	diag *diag.Recorder

	audioRTP      func(callID string, ssrc uint32, seq uint16, timestamp uint32)
	reorderWindow time.Duration
	reorderReport func(callID string, audio, video AudioReorderReport)
}

func resolveConfig(opts []Option) config {
	c := config{log: zerolog.Nop(), reorderWindow: DefaultAudioReorderWindow}
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

// WithAudioRTPObserver calls fn for every audio RTP packet a call receives from the
// relay, in arrival order, before the reorder stage and before decryption, so a consumer can measure loss,
// duplicates and reordering on the media path. fn runs on the receive loop: keep it short.
func WithAudioRTPObserver(fn func(callID string, ssrc uint32, seq uint16, timestamp uint32)) Option {
	return func(c *config) { c.audioRTP = fn }
}

// WithAudioReorderWindow sets how long inbound audio or video that arrived ahead of a missing
// packet waits for it (DefaultAudioReorderWindow when unset). Zero or less turns the
// reorder stage off: packets reach the decoder in arrival order.
func WithAudioReorderWindow(window time.Duration) Option {
	return func(c *config) { c.reorderWindow = window }
}

// WithAudioReorderReport calls fn once per call, when its media ends, with what the
// reorder stage did to the call's inbound audio and video.
func WithAudioReorderReport(fn func(callID string, audio, video AudioReorderReport)) Option {
	return func(c *config) { c.reorderReport = fn }
}
