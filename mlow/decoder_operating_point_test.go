package mlow

import (
	"slices"
	"testing"
)

func TestUnsupportedLowRateFrameDoesNotPoisonDecoderState(t *testing.T) {
	pcm := make([]float32, opusFrameSamps)
	for i := range pcm {
		pcm[i] = 0.05
	}
	encoded, err := NewMlowEncoder().Encode(pcm)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	clean := NewMlowDecoder()
	want := clean.Decode(encoded)
	guarded := NewMlowDecoder()
	lowRate := append([]byte(nil), encoded...)
	lowRate[0] |= 0x04
	if got := guarded.Decode(lowRate); len(got) != opusFrameSamps || rms(got) != 0 {
		t.Fatalf("unsupported frame produced %d non-silent samples", len(got))
	}
	if got := guarded.Decode(encoded); !slices.Equal(got, want) {
		t.Fatal("unsupported frame changed decoder predictor state")
	}
}

func rms(frame []float32) float32 {
	var sum float32
	for _, sample := range frame {
		sum += sample * sample
	}
	return sum
}
