package meowcaller

import (
	"sync"

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

	mu           sync.Mutex
	phase        CallPhase
	player       *Player
	sink         AudioSink
	onReady      func()
	onEnd        func(reason string)
	onState      func(CallPhase)
	videoSink    VideoSink
	onVideoState func(VideoState)
}

// ID returns the call-id (32 uppercase hex chars).
func (c *Call) ID() string { return c.id }

// Peer returns the remote party's LID.
func (c *Call) Peer() types.JID { return c.peer }

// State returns the call's current phase.
func (c *Call) State() CallPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}

// IsVideo reports whether the inbound offer advertised video. Attach a VideoSink with
// ReceiveVideo to receive the peer's H.264.
func (c *Call) IsVideo() bool {
	if m := c.eng.lookup(c.id); m != nil {
		return m.isVideo
	}
	return false
}

// Answer accepts an inbound call (preaccept + accept) and brings media up. No-op error
// if the call is not in a ringing state.
func (c *Call) Answer() error { return c.eng.answer(c) }

// Reject declines an inbound call.
func (c *Call) Reject() error { return c.eng.reject(c) }

// Hangup ends the call (either direction) and tears down media.
func (c *Call) Hangup() error { return c.eng.hangup(c) }

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
func (c *Call) SendVideo(accessUnit []byte) error { return c.eng.sendVideoFrame(c.id, accessUnit) }

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
