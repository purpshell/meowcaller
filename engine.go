package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// engine adapts whatsmeow's call control plane to meowcaller's media plane.
// Whatsmeow owns signaling, call-key exchange, and relay election. Meowcaller owns
// RTP/SRTP, codecs, media pacing, reactions, and public media callbacks.
type engine struct {
	c *Client

	mu           sync.Mutex
	calls        map[string]*engineCall
	setCallVideo func(context.Context, string, types.CallVideoState, *int) error
	setCallMute  func(context.Context, string, bool) error
}

type engineCall struct {
	call    *Call
	callKey []byte
	relay   *types.RelayEndpoint
	selfLID string
	peerLID string
	from    types.JID

	direction        CallDirection
	codec            AudioCodec
	localVideo       bool
	remoteVideo      bool
	videoGate        bool
	peerVideoUpgrade bool
	videoTx          *videoSender
	appDataTx        *appDataSender
	rekeyPeer        func(string) error
	started          bool
	ended            bool
	cancel           context.CancelFunc
}

func newEngine(c *Client) *engine {
	e := &engine{c: c, calls: make(map[string]*engineCall)}
	if c != nil && c.wa != nil {
		e.setCallVideo = c.wa.SetCallVideo
		e.setCallMute = c.wa.SetCallMute
	}
	return e
}

func (e *engine) sendCallVideo(ctx context.Context, callID string, state types.CallVideoState, orientation *int) error {
	if e.setCallVideo == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	return e.setCallVideo(ctx, callID, state, orientation)
}

func (c *Call) onEndFn() func(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onEnd
}

func (c *Call) onReadyFn() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onReady
}

func (c *Call) playerAndSink() (*Player, AudioSink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.player, c.sink
}

func (e *engine) install() {
	e.c.wa.AddEventHandler(func(evt any) {
		switch ev := evt.(type) {
		case *events.CallOffer:
			e.onOffer(ev)
		case *events.CallPreAccept:
			e.onPreAccept(ev)
		case *events.CallAccept:
			e.onAccept(ev)
		case *events.CallMediaReady:
			e.onMediaReady(ev)
		case *events.CallMediaStop:
			e.onMediaStop(ev)
		case *events.CallMute:
			e.onMute(ev)
		case *events.CallVideo:
			e.onVideo(ev)
		}
	})
}

func (e *engine) entry(callID string) *engineCall {
	if e.calls[callID] == nil {
		e.calls[callID] = &engineCall{codec: AudioCodecMlow}
	}
	return e.calls[callID]
}

func (e *engine) lookup(callID string) *engineCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[callID]
}

func (e *engine) callIsVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && (m.localVideo || m.remoteVideo)
}

func (e *engine) callIsSendingVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && m.localVideo
}

func (e *engine) callIsReceivingVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && m.remoteVideo
}

func (e *engine) sendReaction(callID, emoji string) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	sender := m.appDataTx
	e.mu.Unlock()
	if sender == nil {
		return errAppDataUnavailable
	}
	return sender.sendReaction(emoji)
}

func (e *engine) sendVideoFrame(callID string, accessUnit []byte, duration time.Duration) error {
	e.mu.Lock()
	var sender *videoSender
	if m := e.calls[callID]; m != nil {
		sender = m.videoTx
	}
	e.mu.Unlock()
	if sender == nil {
		return errors.New("meowcaller: call has no active video media")
	}
	sender.send(accessUnit, duration)
	return nil
}

func (e *engine) transitionVideo(callID string, transition types.CallVideoState) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	sender := m.videoTx
	localVideoActive := m.localVideo
	switch transition {
	case types.CallVideoStateUpgradeRequestV2:
		m.localVideo = true
		m.videoGate = true
	case types.CallVideoStateUpgradeAccept:
		if !m.peerVideoUpgrade {
			e.mu.Unlock()
			return errors.New("meowcaller: no pending peer video upgrade")
		}
		m.peerVideoUpgrade = false
	case types.CallVideoStateStopped:
		m.localVideo = false
		m.videoGate = false
	default:
		e.mu.Unlock()
		return fmt.Errorf("meowcaller: unsupported local video transition %d", transition)
	}
	e.mu.Unlock()

	if sender != nil {
		switch transition {
		case types.CallVideoStateUpgradeRequestV2:
			sender.enable(true)
		case types.CallVideoStateStopped:
			sender.disable()
		}
	}

	orientation := 0
	var err error
	switch transition {
	case types.CallVideoStateUpgradeRequestV2, types.CallVideoStateStopped:
		err = e.sendCallVideo(context.Background(), callID, transition, &orientation)
	case types.CallVideoStateUpgradeAccept:
		if !localVideoActive {
			err = e.sendCallVideo(context.Background(), callID, types.CallVideoStateStopped, &orientation)
		}
		if err == nil {
			err = e.sendCallVideo(context.Background(), callID, transition, nil)
		}
	}
	if err == nil || transition == types.CallVideoStateStopped {
		return err
	}

	e.mu.Lock()
	var currentSender *videoSender
	if current := e.calls[callID]; current == m {
		if transition == types.CallVideoStateUpgradeAccept {
			current.peerVideoUpgrade = true
		} else {
			current.localVideo = false
			current.videoGate = false
			currentSender = current.videoTx
		}
	}
	e.mu.Unlock()
	if currentSender != nil {
		currentSender.disable()
	}
	return err
}

func (e *engine) setVideoEnabled(callID string, enabled bool) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	m.localVideo = enabled
	m.videoGate = false
	sender := m.videoTx
	e.mu.Unlock()

	if sender != nil {
		if enabled {
			sender.enable(false)
		} else {
			sender.disable()
		}
	}
	state := types.CallVideoStateDisabled
	if enabled {
		state = types.CallVideoStateEnabled
	}
	err := e.sendCallVideo(context.Background(), callID, state, nil)
	if err == nil || !enabled {
		return err
	}
	e.mu.Lock()
	if current := e.calls[callID]; current == m {
		current.localVideo = false
	}
	e.mu.Unlock()
	if sender != nil {
		sender.disable()
	}
	return err
}

func (e *engine) setVideoOrientation(callID string, orientation int) error {
	if orientation < 0 || orientation > 3 {
		return fmt.Errorf("meowcaller: video orientation %d is outside 0..3", orientation)
	}
	e.mu.Lock()
	m := e.calls[callID]
	active := m != nil && m.call != nil && m.call.State() != CallPhaseEnded && m.localVideo
	e.mu.Unlock()
	if !active {
		return errors.New("meowcaller: call has no active video media")
	}
	return e.sendCallVideo(context.Background(), callID, types.CallVideoStateEnabled, &orientation)
}

func (e *engine) setMuted(callID string, muted bool) error {
	e.mu.Lock()
	m := e.calls[callID]
	active := m != nil && m.call != nil && m.call.State() != CallPhaseEnded
	e.mu.Unlock()
	if !active {
		return errors.New("meowcaller: call is not active")
	}
	if e.setCallMute == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	if err := e.setCallMute(context.Background(), callID, muted); err != nil {
		return fmt.Errorf("meowcaller: set call mute: %w", err)
	}
	return nil
}

func (e *engine) placeCall(ctx context.Context, target string, opts CallOptions) (*Call, error) {
	jid, err := parseCallTarget(target)
	if err != nil {
		return nil, err
	}
	callID, err := e.c.wa.OfferCall(ctx, jid, whatsmeowCallOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: offer call: %w", err)
	}
	call := &Call{eng: e, id: callID, peer: jid, phase: CallPhaseCalling}
	e.mu.Lock()
	m := e.entry(callID)
	if m.call == nil {
		m.call = call
	} else {
		call = m.call
	}
	m.from = jid
	m.direction = CallDirectionOutgoing
	m.localVideo = opts.Video
	m.remoteVideo = opts.Video
	e.mu.Unlock()
	e.c.diag.Emit("meta", map[string]any{
		"event": "offer_sent", "call_id": callID, "peer": jid.String(), "direction": "out", "video": opts.Video,
	})
	return call, nil
}

func whatsmeowCallOptions(opts CallOptions) whatsmeow.CallOfferOptions {
	return whatsmeow.CallOfferOptions{Video: opts.Video}
}

func parseCallTarget(target string) (types.JID, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return types.EmptyJID, errors.New("meowcaller: empty call target")
	}
	if strings.ContainsRune(target, '@') {
		jid, err := types.ParseJID(target)
		if err != nil {
			return types.EmptyJID, fmt.Errorf("meowcaller: parse target JID %q: %w", target, err)
		}
		return jid, nil
	}
	return types.NewJID(strings.TrimPrefix(target, "+"), types.DefaultUserServer), nil
}

func (e *engine) onOffer(ev *events.CallOffer) {
	peer := ev.CallCreator
	if peer.IsEmpty() {
		peer = ev.From
	}
	call := &Call{eng: e, id: ev.CallID, peer: peer, phase: CallPhaseRinging}
	e.mu.Lock()
	m := e.entry(ev.CallID)
	if m.call == nil {
		m.call = call
	} else {
		call = m.call
	}
	m.from = ev.From
	m.direction = CallDirectionIncoming
	m.localVideo = ev.Video
	m.remoteVideo = ev.Video
	e.mu.Unlock()
	e.c.diag.Emit("meta", map[string]any{
		"event": "offer_received", "call_id": ev.CallID, "from": ev.From.String(), "peer": peer.String(), "video": ev.Video,
	})
	if fn := e.c.incomingCallHandler(); fn != nil {
		fn(call)
	}
}

func (e *engine) onPreAccept(ev *events.CallPreAccept) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m != nil && m.call != nil && m.direction == CallDirectionOutgoing && m.call.State() == CallPhaseCalling {
		m.call.setPhase(CallPhaseRinging)
	}
	e.mu.Unlock()
}

func (e *engine) onAccept(ev *events.CallAccept) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m == nil || m.direction != CallDirectionOutgoing || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return
	}
	answeringJID := ev.PeerLID
	if answeringJID.IsEmpty() {
		answeringJID = ev.From
	}
	answeringPeer := answeringJID.String()
	var rekeyPeer func(string) error
	if answeringPeer != "" && answeringPeer != m.peerLID {
		m.peerLID = answeringPeer
		rekeyPeer = m.rekeyPeer
		m.call.setPeer(answeringJID)
	}
	if !ev.From.IsEmpty() {
		m.from = ev.From
	}
	call := m.call
	e.mu.Unlock()
	if rekeyPeer != nil {
		if err := rekeyPeer(answeringPeer); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Str("peer_lid", answeringPeer).Msg("failed to rekey media to answering device")
		}
	}
	if call.State() < CallPhaseConnecting {
		call.setPhase(CallPhaseConnecting)
	}
	call.markPeerAccepted()
}

func (e *engine) onMediaReady(ev *events.CallMediaReady) {
	relay := ev.Relay
	e.mu.Lock()
	m := e.entry(ev.CallID)
	if m.ended {
		e.mu.Unlock()
		return
	}
	if m.call == nil {
		phase := CallPhaseRinging
		if ev.Direction == types.CallDirectionOutgoing {
			phase = CallPhaseCalling
		}
		m.call = &Call{eng: e, id: ev.CallID, peer: ev.PeerLID, phase: phase}
	}
	m.callKey = append(m.callKey[:0], ev.CallKey...)
	m.relay = &relay
	m.selfLID = ev.SelfLID.String()
	m.peerLID = ev.PeerLID.String()
	m.call.setPeer(ev.PeerLID)
	m.localVideo = m.localVideo || ev.Video
	m.remoteVideo = m.remoteVideo || ev.Video
	if ev.Codec == types.CallCodecOpus {
		m.codec = AudioCodecOpus
	} else {
		m.codec = AudioCodecMlow
	}
	e.mu.Unlock()
	e.c.diag.Emit("meta", map[string]any{
		"event": "media_ready", "call_id": ev.CallID, "self_lid": ev.SelfLID.String(),
		"peer_lid": ev.PeerLID.String(), "codec": ev.Codec.String(), "video": ev.Video,
	})
	e.maybeStartMedia(ev.CallID)
}

func (e *engine) onMute(ev *events.CallMute) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		if fn := call.onMuteStateFn(); fn != nil {
			fn(ev.Muted)
		}
	}
}

func (e *engine) onVideo(ev *events.CallVideo) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m == nil {
		e.mu.Unlock()
		return
	}
	call := m.call
	sender := m.videoTx
	enableSender := false
	disableSender := false
	requestKeyframe := false
	announceEnabled := false
	switch ev.State {
	case types.CallVideoStateUpgradeRequest, types.CallVideoStateUpgradeRequestV2:
		m.peerVideoUpgrade = true
	case types.CallVideoStateEnabled:
		m.remoteVideo = true
		if m.localVideo && m.videoGate {
			m.videoGate = false
			enableSender = true
			requestKeyframe = true
		}
	case types.CallVideoStateDisabled, types.CallVideoStateStopped:
		m.remoteVideo = false
	case types.CallVideoStateUpgradeAccept:
		m.localVideo = true
		m.videoGate = false
		announceEnabled = true
	case types.CallVideoStateUpgradeReject, types.CallVideoStateUpgradeCancel:
		m.peerVideoUpgrade = false
		if m.videoGate {
			m.localVideo = false
			m.videoGate = false
			disableSender = true
		}
	}
	e.mu.Unlock()

	if announceEnabled {
		orientation := 0
		if err := e.sendCallVideo(context.Background(), ev.CallID, types.CallVideoStateEnabled, &orientation); err != nil {
			e.mu.Lock()
			if current := e.calls[ev.CallID]; current == m {
				current.localVideo = false
				current.videoGate = false
			}
			e.mu.Unlock()
			disableSender = true
		} else {
			enableSender = true
			requestKeyframe = true
		}
	}
	if sender != nil {
		if enableSender {
			sender.enable(false)
		} else if disableSender {
			sender.disable()
		}
	}
	if requestKeyframe && call != nil {
		call.requestVideoKeyframe()
	}
	if call != nil {
		if fn := call.onVideoStateFn(); fn != nil {
			fn(VideoState{
				Active:      ev.State == types.CallVideoStateEnabled,
				Upgrade:     ev.State == types.CallVideoStateUpgradeRequest || ev.State == types.CallVideoStateUpgradeRequestV2,
				Orientation: ev.Orientation,
				Raw:         int(ev.State),
			})
		}
	}
}

func (e *engine) onMediaStop(ev *events.CallMediaStop) {
	e.finishCall(ev.CallID, ev.Reason)
}

func (e *engine) answer(c *Call) error {
	if err := e.c.wa.AcceptCall(context.Background(), c.id); err != nil {
		return fmt.Errorf("meowcaller: accept call: %w", err)
	}
	c.setPhase(CallPhaseConnecting)
	return nil
}

func (e *engine) reject(c *Call) error {
	from := c.Peer()
	if m := e.lookup(c.id); m != nil && !m.from.IsEmpty() {
		from = m.from
	}
	if err := e.c.wa.RejectCall(context.Background(), from, c.id); err != nil {
		return fmt.Errorf("meowcaller: reject call: %w", err)
	}
	return nil
}

func (e *engine) hangup(c *Call) error {
	if err := e.c.wa.HangupCall(context.Background(), c.id); err != nil {
		return fmt.Errorf("meowcaller: hangup call: %w", err)
	}
	return nil
}

func (e *engine) finishCall(callID, reason string) {
	if callID == "" {
		return
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.ended {
		e.mu.Unlock()
		return
	}
	m.ended = true
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	call := m.call
	e.mu.Unlock()
	if call == nil || call.State() == CallPhaseEnded {
		return
	}
	call.setPhase(CallPhaseEnded)
	if fn := call.onEndFn(); fn != nil {
		fn(reason)
	}
}
