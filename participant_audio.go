package meowcaller

import (
	"slices"

	"github.com/polymorfa/hypermeow/types"
)

// ParticipantAudioFrame is authenticated 16 kHz mono audio from one participant
// device, before group mixing. PCM is owned by the callback and may be retained.
type ParticipantAudioFrame struct {
	ParticipantID string
	Sender        types.JID
	Device        types.JID
	PID           uint32
	HasPID        bool
	SSRC          uint32
	Timestamp     uint32
	PCM           []float32
}

// OnParticipantAudioFrame registers an observer independent of Receive. It runs
// on the media receive goroutine and must return promptly. Nil removes it.
func (c *Call) OnParticipantAudioFrame(fn func(ParticipantAudioFrame)) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4/livecall.go#L632-L650
	c.mu.Lock()
	c.onParticipantAudioFrame = fn
	c.mu.Unlock()
}

func (c *Call) dispatchParticipantAudioFrame(frame ParticipantAudioFrame) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4/livecall.go#L640-L650
	c.mu.Lock()
	fn := c.onParticipantAudioFrame
	c.mu.Unlock()
	if fn != nil {
		frame.PCM = slices.Clone(frame.PCM)
		fn(frame)
	}
}
