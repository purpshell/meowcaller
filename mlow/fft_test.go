package mlow

import (
	"math"
	"testing"
)

// naiveDFT is the O(n^2) DFT in float64: the reference the mixed-radix transform
// is measured against. Slow on purpose — correctness has no shortcuts here.
func naiveDFT(x []float32) (re, im []float64) {
	n := len(x)
	re = make([]float64, n)
	im = make([]float64, n)
	for k := 0; k < n; k++ {
		for j := 0; j < n; j++ {
			a := -2.0 * math.Pi * float64(k) * float64(j) / float64(n)
			re[k] += float64(x[j]) * math.Cos(a)
			im[k] += float64(x[j]) * math.Sin(a)
		}
	}
	return re, im
}

// TestFFTMatchesReferenceDFT pins the twiddle-table transform to the reference DFT
// for the two lengths the codec uses (512, the LPC analysis; 576 = 2^6*3^2, the
// perceptual model, which exercises both the radix-2 and the radix-3 levels) plus
// a couple of small mixed-radix lengths that hit the p == n base case.
func TestFFTMatchesReferenceDFT(t *testing.T) {
	for _, n := range []int{9, 12, 60, SmplLPCNFFT, percwNfft} {
		x := make([]float32, n)
		for i := range x {
			x[i] = float32(math.Sin(float64(i)*0.037) + 0.4*math.Cos(float64(i)*0.11))
		}
		re, im := naiveDFT(x)

		cin := make([]cpx, n)
		for i := range cin {
			cin[i].re = x[i]
		}
		out := make([]cpx, n)
		cfft(cin, out, -1.0)

		var peak float64
		for k := range re {
			if m := math.Hypot(re[k], im[k]); m > peak {
				peak = m
			}
		}
		// float32 arithmetic carries ~1e-7 of relative error; anything above 1e-5
		// of the spectrum peak is a wrong twiddle, not rounding.
		tol := 1e-5 * peak
		for k := range re {
			if d := math.Hypot(float64(out[k].re)-re[k], float64(out[k].im)-im[k]); d > tol {
				t.Fatalf("n=%d bin %d: got (%v,%v) want (%v,%v), err %g > %g",
					n, k, out[k].re, out[k].im, re[k], im[k], d, tol)
			}
		}
	}
}

// TestFFTInverseMatchesReferenceDFT does the same for sign=+1, which reads the
// other (conjugate) twiddle table.
func TestFFTInverseMatchesReferenceDFT(t *testing.T) {
	n := percwNfft
	cin := make([]cpx, n)
	for i := range cin {
		cin[i] = cpx{re: float32(math.Sin(float64(i) * 0.023)), im: float32(math.Cos(float64(i) * 0.041))}
	}
	fwd := make([]cpx, n)
	cfft(cin, fwd, -1.0)
	back := make([]cpx, n)
	cfft(fwd, back, 1.0)
	for i := range cin {
		wantRe := cin[i].re * float32(n)
		wantIm := cin[i].im * float32(n)
		if math.Abs(float64(back[i].re-wantRe)) > 1e-2*float64(n)/100.0+1e-2 ||
			math.Abs(float64(back[i].im-wantIm)) > 1e-2*float64(n)/100.0+1e-2 {
			t.Fatalf("idx %d: got (%v,%v) want (%v,%v)", i, back[i].re, back[i].im, wantRe, wantIm)
		}
	}
}

// TestTwiddleTableIsShared checks the memoization actually memoizes: a second ask
// for the same (length, direction) must hand back the very same backing array, and
// the two directions must not collide.
func TestTwiddleTableIsShared(t *testing.T) {
	a := twiddleFactors(percwNfft, -1.0)
	b := twiddleFactors(percwNfft, -1.0)
	if &a[0] != &b[0] {
		t.Fatal("forward twiddle table was rebuilt instead of reused")
	}
	inv := twiddleFactors(percwNfft, 1.0)
	if &inv[0] == &a[0] {
		t.Fatal("forward and inverse tables must be distinct")
	}
	// w[t] for the inverse is the conjugate of the forward one.
	for _, t2 := range []int{1, 7, percwNfft / 3, percwNfft - 1} {
		if a[t2].re != inv[t2].re || a[t2].im != -inv[t2].im {
			t.Fatalf("t=%d: inverse twiddle is not the conjugate: %v vs %v", t2, a[t2], inv[t2])
		}
	}
}

func benchTone() []float32 {
	pcm := make([]float32, opusFrameSamps)
	for i := range pcm {
		t := float64(i) / 16000.0
		pcm[i] = float32(0.3*math.Sin(2*math.Pi*220*t) + 0.15*math.Sin(2*math.Pi*1370*t) + 0.05*math.Sin(2*math.Pi*3100*t))
	}
	return pcm
}

// BenchmarkEncodeFrame is the number that decides how many calls fit on a core:
// one 60 ms frame through the whole encoder. Divide ns/op by 60e6 for the share of
// a core one call costs.
func BenchmarkEncodeFrame(b *testing.B) {
	pcm := benchTone()
	enc := NewMlowEncoder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(pcm); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRfftForward(b *testing.B) {
	for _, n := range []int{SmplLPCNFFT, percwNfft} {
		in := make([]float32, n)
		for i := range in {
			in[i] = float32(math.Sin(float64(i) * 0.01))
		}
		out := make([]float32, n)
		b.Run(map[bool]string{true: "512", false: "576"}[n == SmplLPCNFFT], func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rfftForwardOrdered(in, out)
			}
		})
	}
}
