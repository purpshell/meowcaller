package meowcaller

import (
	"io"
	"sync"
)

// PlayerState is a Player's lifecycle state.
type PlayerState int

const (
	// PlayerIdle means no source is playing (none set, stopped, or finished).
	PlayerIdle PlayerState = iota
	// PlayerPlaying means the active source is streaming into the call.
	PlayerPlaying
	// PlayerPaused means playback is suspended (silence is sent in the meantime).
	PlayerPaused
)

// Player streams an AudioSource into a Call — the discord.js AudioPlayer analogue.
// Attach it with Call.Subscribe (or Call.Play as a shortcut); while Playing, the call
// pulls frames from the source on the codec's 60 ms cadence. When the source reaches
// io.EOF the player returns to PlayerIdle and fires OnFinish (queue the next source
// there for gapless playback).
type Player struct {
	mu       sync.Mutex
	src      AudioSource
	state    PlayerState
	onFinish func()
	// carry holds source samples not yet sent: sources may yield frames of any length
	// (a sink-fed loopback gets the decoder's multi-frame blocks), but the encoder only
	// takes FrameSamples per tick.
	carry []float32
}

// NewPlayer returns an idle Player.
func NewPlayer() *Player {
	return &Player{state: PlayerIdle}
}

// Play sets src as the active source and starts playback, replacing (and closing) any
// current source.
func (p *Player) Play(src AudioSource) {
	p.mu.Lock()
	old := p.src
	p.src = src
	p.state = PlayerPlaying
	p.carry = nil
	p.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// Pause suspends playback; the call sends silence until Resume.
func (p *Player) Pause() {
	p.mu.Lock()
	if p.state == PlayerPlaying {
		p.state = PlayerPaused
	}
	p.mu.Unlock()
}

// Resume continues a paused player.
func (p *Player) Resume() {
	p.mu.Lock()
	if p.state == PlayerPaused {
		p.state = PlayerPlaying
	}
	p.mu.Unlock()
}

// Stop halts playback and closes the active source.
func (p *Player) Stop() {
	p.mu.Lock()
	old := p.src
	p.src = nil
	p.state = PlayerIdle
	p.carry = nil
	p.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// State returns the current PlayerState.
func (p *Player) State() PlayerState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// OnFinish registers a callback fired when the active source is exhausted (the player
// transitions to PlayerIdle). Replaces any previous callback.
func (p *Player) OnFinish(fn func()) {
	p.mu.Lock()
	p.onFinish = fn
	p.mu.Unlock()
}

// nextFrame is pulled by the engine's send loop each frame interval. It returns exactly
// FrameSamples samples while Playing, or nil to send silence (idle/paused/between
// sources, or a live source with nothing buffered yet). Source frames of any other
// length are re-cut: longer ones are split across ticks and shorter ones are joined,
// because the encoder rejects anything but FrameSamples and the tick would otherwise
// carry no packet at all. On source EOF it goes Idle and fires OnFinish (outside the
// lock); a final partial frame is zero-padded.
func (p *Player) nextFrame() []float32 {
	p.mu.Lock()
	if p.state != PlayerPlaying || p.src == nil {
		p.mu.Unlock()
		return nil
	}
	src := p.src
	carry := p.carry
	p.carry = nil
	p.mu.Unlock()

	for len(carry) < FrameSamples {
		frame, err := src.ReadFrame()
		if err != nil {
			p.finish(src)
			if err != io.EOF {
				// A decode error still ends playback; buffered samples are dropped.
				return nil
			}
			carry = append(carry, frame...)
			if len(carry) == 0 {
				return nil
			}
			return padFrame(carry)
		}
		if frame == nil {
			break
		}
		if len(carry) == 0 && len(frame) == FrameSamples {
			return frame
		}
		carry = append(carry, frame...)
	}
	if len(carry) < FrameSamples {
		p.keepCarry(src, carry)
		return nil
	}
	out := make([]float32, FrameSamples)
	copy(out, carry)
	p.keepCarry(src, carry[FrameSamples:])
	return out
}

// keepCarry stores leftover samples for the next tick unless src was replaced meanwhile.
func (p *Player) keepCarry(src AudioSource, rest []float32) {
	if len(rest) == 0 {
		return
	}
	p.mu.Lock()
	if p.src == src {
		p.carry = append([]float32(nil), rest...)
	}
	p.mu.Unlock()
}

// finish handles an exhausted (or failed) source: go idle, close it, fire OnFinish once.
func (p *Player) finish(src AudioSource) {
	p.mu.Lock()
	var onFinish func()
	if p.src == src { // still the active source (not replaced concurrently)
		p.src = nil
		p.state = PlayerIdle
		p.carry = nil
		onFinish = p.onFinish
	}
	p.mu.Unlock()
	_ = src.Close()
	if onFinish != nil {
		onFinish()
	}
}

// padFrame returns the first FrameSamples of samples, zero-padding a short tail.
func padFrame(samples []float32) []float32 {
	out := make([]float32, FrameSamples)
	copy(out, samples)
	return out
}
