package meowcaller

import (
	"context"
	"sync"

	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
)

// Client is the managed entry point to the WhatsApp 1:1 calling stack. It wraps a
// connected *whatsmeow.Client, consumes its call-control events, and drives the media
// lifecycle behind a small surface:
// place a call with Call, handle inbound calls from an OnIncomingCall listener, and
// attach a Player (outbound audio) and a sink (inbound audio) to each Call.
//
// The library never configures logging; pass WithLogger to surface its debug/trace.
type Client struct {
	wa   *whatsmeow.Client
	log  zerolog.Logger
	diag *diag.Recorder
	eng  *engine

	mu             sync.Mutex
	onIncomingCall func(*Call)
}

// CallOptions controls media negotiated for an outbound call.
type CallOptions struct {
	// Video advertises a WhatsApp video call. The caller must provide encoded H.264
	// access units with Call.SendVideo after media is active.
	Video bool
}

// NewClient wraps a whatsmeow client and installs the call event handlers. Construct it
// before the whatsmeow client connects so no incoming call event is missed.
func NewClient(wa *whatsmeow.Client, opts ...Option) *Client {
	cfg := resolveConfig(opts)
	c := &Client{wa: wa, log: cfg.log, diag: cfg.diag}
	c.eng = newEngine(c)
	c.eng.install()
	return c
}

// Call places a 1:1 call to target (a phone number, a phone JID, or an @lid JID),
// returning the live Call once the offer is on the wire. Attach a Player and listeners
// to the returned Call; media starts automatically once the peer answers and the relay
// endpoint arrives.
func (c *Client) Call(ctx context.Context, target string) (*Call, error) {
	return c.CallWithOptions(ctx, target, CallOptions{})
}

// CallWithOptions places a 1:1 call with explicit media options.
func (c *Client) CallWithOptions(ctx context.Context, target string, opts CallOptions) (*Call, error) {
	return c.eng.placeCall(ctx, target, opts)
}

// OnIncomingCall registers the listener fired for each inbound call offer. The handler
// receives a Call that has not been answered yet; call Answer or Reject on it. Only the
// most recently registered listener is used.
func (c *Client) OnIncomingCall(fn func(*Call)) {
	c.mu.Lock()
	c.onIncomingCall = fn
	c.mu.Unlock()
}

// incomingCallHandler returns the registered inbound-call listener, or nil.
func (c *Client) incomingCallHandler() func(*Call) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onIncomingCall
}
