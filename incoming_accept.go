package meowcaller

import (
	"context"
	"errors"
	"fmt"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "go.mau.fi/whatsmeow/binary"
)

type acceptTimer interface {
	Stop() bool
}

type incomingAcceptState uint8

const (
	incomingAcceptIdle incomingAcceptState = iota
	incomingAcceptRequested
	incomingAcceptWaiting
	incomingAcceptSending
	incomingAcceptSent
	incomingAcceptFailed
	incomingAcceptCancelled
)

// AcceptTrigger identifies what released an incoming call's final accept.
type AcceptTrigger string

const (
	AcceptTriggerMuteV2   AcceptTrigger = "mute_v2"
	AcceptTriggerFallback AcceptTrigger = "fallback_timeout"
)

// IncomingAcceptEvent is a sanitized diagnostic event for incoming final accept.
type IncomingAcceptEvent struct {
	State   string
	Trigger AcceptTrigger
	Err     error
}

// IncomingAcceptError reports a typed final-accept failure.
type IncomingAcceptError struct {
	Kind string
	Err  error
}

func (e *IncomingAcceptError) Error() string {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/call-accept.yaml#L8-L37
	return fmt.Sprintf("meowcaller: %s: %v", e.Kind, e.Err)
}
func (e *IncomingAcceptError) Unwrap() error {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/call-accept.yaml#L8-L37
	return e.Err
}

var ErrIncomingAcceptCancelled = errors.New("incoming accept cancelled")

type incomingAccept struct {
	state           incomingAcceptState
	answerRequested bool
	preacceptSent   bool
	muteV2Received  bool
	trigger         AcceptTrigger
	timer           acceptTimer
	sendCancel      context.CancelFunc
	sendCount       uint32
}

func captureIncomingAcceptMetadata(offer *waBinary.Node) waBinary.Attrs {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d37b1756d05fb34c9b6c2410c48dd20d27394929/wacore/src/stanza/call.rs#L135-L155
	// NOT VALIDATED: remove after a live inbound call receives peer RTP with echoed metadata.
	if offer == nil {
		return nil
	}
	for i := range offer.GetChildren() {
		child := &offer.GetChildren()[i]
		if child.Tag != "metadata" {
			continue
		}
		metadata := make(waBinary.Attrs, 2)
		for _, key := range []string{"peer_abtest_bucket", "peer_abtest_bucket_id_list"} {
			if value, ok := child.Attrs[key].(string); ok {
				metadata[key] = value
			}
		}
		if len(metadata) == 0 {
			return nil
		}
		return metadata
	}
	return nil
}

func (e *engine) notifyIncomingAccept(call *Call, state string, trigger AcceptTrigger, err error) {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/flow-incoming-1to1.yaml#L82-L115
	if call != nil {
		call.notifyIncomingAccept(IncomingAcceptEvent{State: state, Trigger: trigger, Err: err})
	}
}

func (e *engine) canSendFinalAcceptLocked(m *engineCall, trigger AcceptTrigger) bool {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/flow-incoming-1to1.yaml#L82-L115
	if m == nil || m.direction != CallDirectionIncoming || m.call == nil {
		return false
	}
	if !m.accept.answerRequested || !m.accept.preacceptSent || m.relay == nil {
		return false
	}
	if m.accept.state == incomingAcceptSending || m.accept.state == incomingAcceptSent || m.accept.state == incomingAcceptFailed || m.accept.state == incomingAcceptCancelled {
		return false
	}
	return trigger == AcceptTriggerMuteV2 || trigger == AcceptTriggerFallback
}

func (e *engine) armIncomingAcceptFallback(callID string) {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/call-mute.yaml#L22-L34
	e.mu.Lock()
	m := e.calls[callID]
	if !e.canSendFinalAcceptLocked(m, AcceptTriggerFallback) || m.accept.muteV2Received || m.accept.timer != nil {
		e.mu.Unlock()
		return
	}
	m.accept.state = incomingAcceptWaiting
	call := m.call
	timer := e.afterFunc(e.acceptFallbackTimeout, func() {
		if err := e.trySendFinalAccept(callID, AcceptTriggerFallback); err != nil && !errors.Is(err, ErrIncomingAcceptCancelled) {
			e.c.log.Error().Err(err).Str("call_id", callID).Msg("incoming accept fallback failed")
		}
	})
	m.accept.timer = timer
	e.mu.Unlock()
	e.c.log.Info().Str("call_id", callID).Dur("timeout", e.acceptFallbackTimeout).Msg("incoming accept waiting")
	e.notifyIncomingAccept(call, "incoming_accept_waiting", "", nil)
}

func (e *engine) trySendFinalAccept(callID string, trigger AcceptTrigger) error {
	// Source of truth: https://github.com/WhiskeySockets/wacrg/blob/0114515cef5c0344a8a864f6ad5ff58e650550ed/spec/signalling/call-accept.yaml#L8-L37
	e.mu.Lock()
	m := e.calls[callID]
	if !e.canSendFinalAcceptLocked(m, trigger) {
		e.mu.Unlock()
		return nil
	}
	if m.accept.timer != nil {
		m.accept.timer.Stop()
		m.accept.timer = nil
	}
	m.accept.state = incomingAcceptSending
	m.accept.trigger = trigger
	ctx, cancel := context.WithCancel(context.Background())
	m.accept.sendCancel = cancel
	call, to, creator := m.call, m.from, m.creator
	isVideo := m.localVideo || m.remoteVideo
	metadata := m.acceptMetadata
	e.mu.Unlock()

	e.notifyIncomingAccept(call, "incoming_accept_send_started", trigger, nil)
	wrapperID := e.nextCallNodeID()
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d37b1756d05fb34c9b6c2410c48dd20d27394929/examples/voip-cli/src/main.rs#L1182-L1197
	// A from-start video accept carries neither the caller's relay <te> nor a
	// <capability>. Sending those optional children after allocation was observed to
	// make the caller stop the RTP stream already flowing through the elected relay.
	// Voice accepts keep their negotiated capability. Relay readiness remains a send
	// prerequisite.
	// NOT VALIDATED: remove after a live incoming video call keeps receiving peer RTP
	// after the final accept.
	var capability []byte
	if !isVideo {
		capability = signaling.CapabilityOffer
	}
	accept := signaling.BuildAccept(&signaling.AcceptParams{
		CallID: callID, To: to, WrapperID: wrapperID, CallCreator: creator,
		AudioRates: []string{"16000"},
		Capability: capability,
		// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d37b1756d05fb34c9b6c2410c48dd20d27394929/wacore/src/stanza/call.rs#L530-L569
		Metadata: metadata,
		Video:    isVideo,
	})
	err := e.transmitCallNode(ctx, accept)
	cancel()

	e.mu.Lock()
	current := e.calls[callID]
	if current != m || m.accept.state == incomingAcceptCancelled {
		e.mu.Unlock()
		return ErrIncomingAcceptCancelled
	}
	m.accept.sendCancel = nil
	if err != nil {
		m.accept.state = incomingAcceptFailed
		e.mu.Unlock()
		typed := &IncomingAcceptError{Kind: "accept_send_failed", Err: err}
		e.notifyIncomingAccept(call, "incoming_accept_failed", trigger, typed)
		e.finishCall(callID, "accept_failed")
		return typed
	}
	m.accept.state = incomingAcceptSent
	m.accept.sendCount++
	e.mu.Unlock()

	e.c.log.Info().Str("call_id", callID).Str("trigger", string(trigger)).Bool("video", isVideo).Uint32("accept_send_count", 1).Msg("incoming accept sent")
	e.notifyIncomingAccept(call, "incoming_accept_sent", trigger, nil)
	return nil
}
