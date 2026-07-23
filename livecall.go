package meowcaller

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Call is one live 1:1 call. Place one with Client.Call, or receive one (unanswered)
// in an OnIncomingCall listener. Attach outbound audio with Subscribe/Play and inbound
// audio with Receive, and lifecycle listeners with OnReady/OnEnd/OnStateChange. All
// methods are safe for concurrent use.
type Call struct {
	eng  *engine
	id   string
	peer types.JID

	mu                     sync.Mutex
	phase                  CallPhase
	player                 *Player
	sink                   AudioSink
	onReady                func()
	onEnd                  func(reason string)
	onState                func(CallPhase)
	onPeerAccept           func()
	peerAccepted           bool
	acceptNotified         bool
	onMuteState            func(muted bool)
	videoSink              VideoSink
	onVideoState           func(VideoState)
	onVideoKeyframeRequest func()
	onReaction             func(CallReaction)
}

// CallReaction is a transient emoji reaction received over the call's RTC app-data stream.
// An empty Emoji with Removed set means the sender cleared their reaction.
type CallReaction struct {
	Emoji   string
	Sender  types.JID
	Removed bool
}

// ID returns the call-id (32 uppercase hex chars).
func (c *Call) ID() string { return c.id }

// Peer returns the remote party's LID.
func (c *Call) Peer() types.JID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peer
}

func (c *Call) setPeer(peer types.JID) {
	if peer.IsEmpty() {
		return
	}
	c.mu.Lock()
	c.peer = peer
	c.mu.Unlock()
}

// State returns the call's current phase.
func (c *Call) State() CallPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}

// IsVideo reports whether either direction of the call currently has video.
func (c *Call) IsVideo() bool {
	return c.eng.callIsVideo(c.id)
}

// IsSendingVideo reports whether this client currently owns an active or pending
// outbound video flow.
func (c *Call) IsSendingVideo() bool {
	return c.eng.callIsSendingVideo(c.id)
}

// IsReceivingVideo reports whether the peer currently owns an active inbound video flow.
func (c *Call) IsReceivingVideo() bool {
	return c.eng.callIsReceivingVideo(c.id)
}

// Answer accepts an inbound call (preaccept + accept) and brings media up. No-op error
// if the call is not in a ringing state.
func (c *Call) Answer() error { return c.eng.answer(c) }

// Reject declines an inbound call.
func (c *Call) Reject() error { return c.eng.reject(c) }

// Hangup ends the call (either direction) and tears down media.
func (c *Call) Hangup() error { return c.eng.hangup(c) }

// StartVideo requests an audio-to-video upgrade. Outbound video remains gated until
// the peer acknowledges the transition with state 4 or state 1.
func (c *Call) StartVideo() error {
	return c.eng.transitionVideo(c.id, types.CallVideoStateUpgradeRequestV2)
}

// AcceptVideo accepts a pending peer video upgrade without changing this client's
// independent outbound video state.
func (c *Call) AcceptVideo() error {
	return c.eng.transitionVideo(c.id, types.CallVideoStateUpgradeAccept)
}

// StopVideo stops this client's outbound video while preserving peer video and audio.
func (c *Call) StopVideo() error {
	return c.eng.transitionVideo(c.id, types.CallVideoStateStopped)
}

// SetVideoEnabled mutes or unmutes this client's outbound camera flow with state 0/1.
// It does not change whether the peer's video is received.
func (c *Call) SetVideoEnabled(enabled bool) error {
	return c.eng.setVideoEnabled(c.id, enabled)
}

// SetVideoOrientation announces the local camera rotation as quarter turns clockwise.
func (c *Call) SetVideoOrientation(orientation int) error {
	return c.eng.setVideoOrientation(c.id, orientation)
}

// SetMuted announces this client's microphone mute state to the peer.
func (c *Call) SetMuted(muted bool) error {
	return c.eng.setMuted(c.id, muted)
}

// SendReaction sends an emoji over this call's RTC app-data stream. Pass an empty
// string to clear the reaction previously sent by this client.
func (c *Call) SendReaction(emoji string) error {
	return c.eng.sendReaction(c.id, emoji)
}

// Subscribe attaches p as the call's outbound audio player, replacing any previous one.
// While the player is Playing, its source frames are encoded and sent to the peer;
// otherwise silence is sent (the call must keep sending to hold the relay bridge).
func (c *Call) Subscribe(p *Player) {
	c.mu.Lock()
	c.player = p
	c.mu.Unlock()
}

// Play is a shortcut: it creates a Player, subscribes it, starts src, and returns the
// Player (use it for Pause/Stop/OnFinish).
func (c *Call) Play(src AudioSource) *Player {
	p := NewPlayer()
	c.Subscribe(p)
	p.Play(src)
	return p
}

// Receive attaches a sink for the peer's decoded audio (16 kHz mono frames), replacing
// any previous one. Without a sink the inbound audio is decoded and discarded.
func (c *Call) Receive(sink AudioSink) {
	c.mu.Lock()
	c.sink = sink
	c.mu.Unlock()
}

// ReceiveVideo attaches a sink for the peer's H.264 video, delivered as Annex-B access units
// (one per frame, reassembled on the RTP marker), replacing any previous one. Without a sink
// the inbound video is discarded. The video analog of Receive; AnnexBRecorder records to a
// .h264 file, or use VideoSinkFunc to forward to a callback.
//
// NOT VALIDATED: the inbound-video media path is unproven (no captured video-RTP vector).
func (c *Call) ReceiveVideo(sink VideoSink) {
	c.mu.Lock()
	c.videoSink = sink
	c.mu.Unlock()
}

// videoSinkRef returns the Call's current video sink under its lock.
func (c *Call) videoSinkRef() VideoSink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoSink
}

// OnVideoState registers a callback fired for each inbound <video> state stanza — the peer's
// video on/off, the audio→video upgrade, and device orientation (rotate by Orientation × 90°).
func (c *Call) OnVideoState(fn func(VideoState)) {
	c.mu.Lock()
	c.onVideoState = fn
	c.mu.Unlock()
}

// OnVideoKeyframeRequest registers a callback for authenticated WhatsApp PLI/FIR
// feedback. Encoded video sources should make their next access unit an IDR.
func (c *Call) OnVideoKeyframeRequest(fn func()) {
	c.mu.Lock()
	c.onVideoKeyframeRequest = fn
	c.mu.Unlock()
}

// OnReaction registers a callback for emoji reactions targeting this call.
func (c *Call) OnReaction(fn func(CallReaction)) {
	c.mu.Lock()
	c.onReaction = fn
	c.mu.Unlock()
}

func (c *Call) onReactionFn() func(CallReaction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onReaction
}

func (c *Call) requestVideoKeyframe() {
	c.mu.Lock()
	fn := c.onVideoKeyframeRequest
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// onVideoStateFn returns the Call's video-state callback under its lock.
func (c *Call) onVideoStateFn() func(VideoState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onVideoState
}

// SendVideo sends one already-encoded H.264 access unit (Annex-B) to the peer — fed from an
// external encoder (browser WebCodecs, ffmpeg, hardware). Returns an error if the call has no
// active video media yet. meowcaller does not encode pixels (no pure-Go H.264 encoder); this
// is the video analog of writing a sample to a track.
//
// NOT VALIDATED: the video send media path is unproven.
func (c *Call) SendVideo(accessUnit []byte) error {
	return c.SendVideoWithDuration(accessUnit, 0)
}

// SendVideoWithDuration sends one already-encoded H.264 access unit and advances the
// outgoing RTP timestamp by duration for the next frame. A zero duration uses the sender
// fallback frame step.
func (c *Call) SendVideoWithDuration(accessUnit []byte, duration time.Duration) error {
	return c.eng.sendVideoFrame(c.id, accessUnit, duration)
}

// OnReady registers a callback fired once media is flowing (relay bound, first frames
// exchanged).
func (c *Call) OnReady(fn func()) {
	c.mu.Lock()
	c.onReady = fn
	c.mu.Unlock()
}

// OnEnd registers a callback fired when the call ends, with a short reason string.
func (c *Call) OnEnd(fn func(reason string)) {
	c.mu.Lock()
	c.onEnd = fn
	c.mu.Unlock()
}

// OnStateChange registers a callback fired on each phase transition.
func (c *Call) OnStateChange(fn func(CallPhase)) {
	c.mu.Lock()
	c.onState = fn
	c.mu.Unlock()
}

// OnPeerAccept registers a one-shot callback for the remote peer accepting an outgoing
// call. If acceptance arrived before registration, the callback is invoked immediately.
func (c *Call) OnPeerAccept(fn func()) {
	c.mu.Lock()
	c.onPeerAccept = fn
	shouldNotify := c.peerAccepted && !c.acceptNotified && fn != nil
	if shouldNotify {
		c.acceptNotified = true
	}
	c.mu.Unlock()
	if shouldNotify {
		fn()
	}
}

func (c *Call) markPeerAccepted() {
	c.mu.Lock()
	c.peerAccepted = true
	fn := c.onPeerAccept
	shouldNotify := !c.acceptNotified && fn != nil
	if shouldNotify {
		c.acceptNotified = true
	}
	c.mu.Unlock()
	if shouldNotify {
		fn()
	}
}

// OnMuteState registers a callback fired for each inbound WhatsApp mute_v2 state.
// The callback describes the remote party's microphone state: true means muted.
func (c *Call) OnMuteState(fn func(muted bool)) {
	c.mu.Lock()
	c.onMuteState = fn
	c.mu.Unlock()
}

// onMuteStateFn returns the Call's remote mute-state callback under its lock.
func (c *Call) onMuteStateFn() func(bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onMuteState
}

// setPhase advances the call's phase and fires OnStateChange (used by the engine).
func (c *Call) setPhase(next CallPhase) {
	c.mu.Lock()
	if c.phase == next {
		c.mu.Unlock()
		return
	}
	c.phase = next
	fn := c.onState
	c.mu.Unlock()
	if fn != nil {
		fn(next)
	}
}
