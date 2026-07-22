package mlow

import (
	"math"
	"testing"
)

func TestOperatingPointChangeResetsDecoderState(t *testing.T) {
	pcm := make([]float32, opusFrameSamps)
	for i := range pcm {
		pcm[i] = 0.05
	}
	encoded, err := NewMlowEncoder().Encode(pcm)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoder := NewMlowDecoder()
	decoder.Decode(encoded)
	highRateState := decoder.state
	lowRate := append([]byte(nil), encoded...)
	lowRate[0] |= 0x04
	if got := decoder.Decode(lowRate); len(got) != opusFrameSamps {
		t.Fatalf("low-rate frame produced %d samples", len(got))
	}
	if decoder.state == highRateState {
		t.Fatal("operating-point change retained the old predictor state")
	}
	if decoder.activeConfig != 1 {
		t.Fatalf("active config = %d, want 1", decoder.activeConfig)
	}
}

func TestLowRateCelpUsesTwoSubframes(t *testing.T) {
	state := NewCelpDecState()
	current := make([]float32, SmplOrder)
	var previous [SmplOrder]float32
	for i := range current {
		previous[i] = 0.1 + float32(i)*0.14
		current[i] = previous[i] + 0.02
		state.lsfPrev[i] = previous[i]
	}
	var out [SmplIntfLen]float32
	state.SynthFrame(current, 1, make([]int32, SmplIntfLen), &CelpDecParams{}, true, SmplIntfLen, out[:])
	for i := range current {
		want := previous[i]*0.05 + current[i]*0.95
		if math.Abs(float64(state.lsfPrev[i]-want)) > 1e-6 {
			t.Fatalf("lsfPrev[%d] = %f, want %f", i, state.lsfPrev[i], want)
		}
	}
}
