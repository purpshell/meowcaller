package meowcaller

import (
	"testing"

	"github.com/polymorfa/hypermeow/types"
)

func TestParticipantAudioCallbackOwnsSamplesAndKeepsIdentity(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4/group_media_receive.go#L21-L31
	call := &Call{}
	input := ParticipantAudioFrame{
		ParticipantID: "300:4@lid",
		Sender:        types.NewJID("300", types.HiddenUserServer),
		Device:        types.JID{User: "300", Server: types.HiddenUserServer, Device: 4},
		PID:           9, HasPID: true, SSRC: 0x12345678, Timestamp: 960,
		PCM: []float32{0.25, -0.5},
	}
	var got ParticipantAudioFrame
	callbacks := 0
	call.OnParticipantAudioFrame(func(frame ParticipantAudioFrame) {
		got = frame
		callbacks++
		frame.PCM[0] = 1
		call.OnParticipantAudioFrame(nil)
	})
	call.dispatchParticipantAudioFrame(input)
	if input.PCM[0] != 0.25 {
		t.Fatal("callback changed the decoder's samples")
	}
	input.PCM[1] = 0
	if got.PCM[1] != -0.5 {
		t.Fatal("callback samples alias the decoder's reused buffer")
	}
	if got.ParticipantID != input.ParticipantID || got.Sender != input.Sender ||
		got.Device != input.Device || got.PID != input.PID || got.HasPID != input.HasPID ||
		got.SSRC != input.SSRC || got.Timestamp != input.Timestamp {
		t.Fatal("callback changed authenticated participant metadata")
	}
	call.dispatchParticipantAudioFrame(input)
	if callbacks != 1 {
		t.Fatal("callback removal did not take effect")
	}
}
