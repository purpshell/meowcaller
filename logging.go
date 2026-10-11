package meowcaller

import (
	"github.com/polymorfa/hypermeow/types"

	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
)

// Option configures optional, non-behavioral aspects of the call/media types —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log  zerolog.Logger
	diag *diag.Recorder

	offerGate func(Offer) bool
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

// Offer is what an offer gate is told about an inbound call before the library
// touches it.
type Offer struct {
	CallID string
	// From is the stanza's sender; Creator is the call-creator, the same user on a
	// 1:1 call.
	From    types.JID
	Creator types.JID
	// Group reports a group call or call-link invite.
	Group bool
	// Video reports that the offer advertises video.
	Video bool
}

// WithOfferGate makes the library ask before it engages with an inbound offer. When
// fn returns false nothing is decrypted, no <preaccept> is sent, no media starts and
// OnIncomingCall does not fire: the device stays as passive as a plain whatsmeow
// client. Without a gate every offer is engaged. fn runs on whatsmeow's event
// dispatch: keep it short.
func WithOfferGate(fn func(Offer) bool) Option {
	return func(c *config) { c.offerGate = fn }
}
