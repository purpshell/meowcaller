package mlow

import (
	"math"
	"math/rand"
	"testing"
)

// speechFrame synthesises one 60 ms frame of voice-like audio: a 110 Hz
// fundamental with harmonics, light noise and a ~4 Hz syllabic envelope.
// Benchmarking the encoder on silence is misleading — the VAD takes a much
// shorter path for silent frames.
func speechFrame(r *rand.Rand, phase *float64) []float32 {
	pcm := make([]float32, opusFrameSamps)
	step := 2 * math.Pi * 110.0 / 16000.0
	for i := range pcm {
		v := math.Sin(*phase)*0.45 + math.Sin(2**phase)*0.22 + math.Sin(3**phase)*0.11
		v += (r.Float64()*2 - 1) * 0.03
		env := 0.55 + 0.45*math.Sin(2*math.Pi*4*float64(i)/16000.0)
		pcm[i] = float32(v * env)
		*phase += step
	}
	return pcm
}

// BenchmarkMlowEncodeFrame measures one 60 ms frame through MlowEncoder, which
// is where fftRec dominates the profile.
func BenchmarkMlowEncodeFrame(b *testing.B) {
	enc := NewMlowEncoder()
	r := rand.New(rand.NewSource(7))
	phase := 0.0
	frames := make([][]float32, 32)
	for i := range frames {
		frames[i] = speechFrame(r, &phase)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(frames[i%len(frames)]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPercFFT isolates the transform itself at the size the perceptual
// model uses.
func BenchmarkPercFFT(b *testing.B) {
	n := percwNfft
	x := make([]float32, n)
	r := rand.New(rand.NewSource(11))
	for i := range x {
		x[i] = float32(r.Float64()*2 - 1)
	}
	f := make([]float32, n)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rfftForwardOrdered(x, f)
	}
}
