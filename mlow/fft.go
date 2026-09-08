package mlow

import (
	"math"
	"sync"
)

// cpx is a single-precision complex value.
//
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L318-L343
type cpx struct {
	re, im float32
}

func (a cpx) add(b cpx) cpx {
	return cpx{re: a.re + b.re, im: a.im + b.im}
}

func (a cpx) mul(b cpx) cpx {
	return cpx{
		re: a.re*b.re - a.im*b.im,
		im: a.re*b.im + a.im*b.re,
	}
}

// smallestFactor returns the smallest prime factor of n (>= 2).
func smallestFactor(n int) int {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L346-L358
	if n%2 == 0 {
		return 2
	}
	p := 3
	for p*p <= n {
		if n%p == 0 {
			return p
		}
		p += 2
	}
	return n
}

// twiddleKey identifies a precomputed twiddle table: its length and its direction.
type twiddleKey struct {
	n   int
	inv bool
}

// twiddleCache memoizes the twiddle tables. The encoder and the decoder only ever
// ask for the handful of transform lengths the codec is built around (512 for the
// LPC analysis, 576 for the perceptual model), so the cache holds a couple of
// entries for the life of the process and never grows with traffic.
var twiddleCache sync.Map // twiddleKey -> []cpx

// twiddleFactors returns the n-th roots of unity w[t] = e^(sign*i*2*pi*t/n),
// computed once per (length, direction) and shared afterwards.
//
// Every twiddle the mixed-radix recursion needs at a sub-length m that divides n
// is also an n-th root of unity, so one table of n entries serves every level:
// e^(sign*i*2*pi*k*q/m) == w[(k*q mod m)*(n/m)]. That is what keeps math.Cos and
// math.Sin out of the hot path — they used to run once per butterfly.
func twiddleFactors(n int, sign float32) []cpx {
	key := twiddleKey{n: n, inv: sign > 0}
	if v, ok := twiddleCache.Load(key); ok {
		return v.([]cpx)
	}
	w := make([]cpx, n)
	for t := 0; t < n; t++ {
		ang := float64(sign) * 2.0 * smplPI * float64(t) / float64(n)
		w[t] = cpx{re: float32(math.Cos(ang)), im: float32(math.Sin(ang))}
	}
	// LoadOrStore, not Store: two goroutines racing on the same length must end up
	// sharing one table instead of one of them replacing the other's in flight.
	actual, _ := twiddleCache.LoadOrStore(key, w)
	return actual.([]cpx)
}

// fftScratch holds the reusable buffers one real FFT needs: two complex buffers
// the size of the transform (input packing and spectrum) plus the recursion
// arena. A 60 ms frame runs ~24 transforms through the encoder, and the naive
// path allocated a fresh []cpx on every recursion level and two more per
// transform — ~3700 allocations and 1.6 MB per frame. Pooling the scratch keeps
// a busy encoder reusing the same buffers across frames instead of feeding the GC.
//
// The pool holds *fftScratch (not the slices directly) so nothing extra escapes
// to the heap on Put, and it never grows with traffic: the codec only ever asks
// for lengths 512 and 576, so after warm-up the pooled buffers settle at the 576
// size and are reused for both.
type fftScratch struct {
	a, b []cpx // transform-sized (n): input packing and spectrum
	rec  []cpx // recursion arena, >= 2n (see fftRecTw)
}

var fftScratchPool = sync.Pool{New: func() any { return new(fftScratch) }}

// ensureCpx returns buf resized to n, reusing its backing array when it is large
// enough and allocating only when it must grow.
func ensureCpx(buf []cpx, n int) []cpx {
	if cap(buf) < n {
		return make([]cpx, n)
	}
	return buf[:n]
}

// getFFTScratch returns scratch sized for an n-point transform: a and b hold n
// complex values each and rec holds the 2n-entry recursion arena. Callers must
// pair it with putFFTScratch.
func getFFTScratch(n int) *fftScratch {
	s := fftScratchPool.Get().(*fftScratch)
	s.a = ensureCpx(s.a, n)
	s.b = ensureCpx(s.b, n)
	s.rec = ensureCpx(s.rec, 2*n)
	return s
}

func putFFTScratch(s *fftScratch) { fftScratchPool.Put(s) }

// fftRec is the recursive mixed-radix Cooley-Tukey DFT. sign is -1 forward, +1
// inverse (unnormalized). x holds n inputs at the given stride; out is contiguous.
// scratch is the recursion arena and must hold at least 2n entries.
func fftRec(x []cpx, stride, n int, sign float32, out, scratch []cpx) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L362-L405
	if n == 1 {
		out[0] = x[0]
		return
	}
	fftRecTw(x, stride, n, out, twiddleFactors(n, sign), 1, scratch)
}

// fftRecTw is fftRec with the twiddle table threaded through. tw holds the N-th
// roots of unity of the top-level transform and step is N/n, so tw[t*step] is the
// t-th n-th root of unity at this level.
//
// scratch is a shared arena instead of a per-level make([]cpx, n). At a node of
// size n it carves sub := scratch[:n] for this level's outputs and hands
// scratch[n:] to the children. Siblings run sequentially, so a child reuses the
// arena its finished siblings left behind; only the ancestors along one path are
// live at once, and their sizes are n, n/p, n/p², … The invariant
// len(scratch) >= 2n is preserved down the recursion (2·(n/p) <= n for p >= 2),
// so a 2n arena at the top covers every level.
func fftRecTw(x []cpx, stride, n int, out, tw []cpx, step int, scratch []cpx) {
	if n == 1 {
		out[0] = x[0]
		return
	}
	p := smallestFactor(n)
	if p == n {
		for k := 0; k < n; k++ {
			var acc cpx
			for j := 0; j < n; j++ {
				acc = acc.add(x[j*stride].mul(tw[(k*j%n)*step]))
			}
			out[k] = acc
		}
		return
	}
	m := n / p
	sub := scratch[:n]
	child := scratch[n:]
	for q := 0; q < p; q++ {
		fftRecTw(x[q*stride:], stride*p, m, sub[q*m:(q+1)*m], tw, step*p, child)
	}
	for k := 0; k < n; k++ {
		kmod := k % m
		var acc cpx
		for q := 0; q < p; q++ {
			acc = acc.add(sub[q*m+kmod].mul(tw[(k*q%n)*step]))
		}
		out[k] = acc
	}
}

// cfft computes the complex FFT of a mixed-radix length into out. sign=-1 forward,
// +1 inverse. It borrows a recursion arena from the pool for the transform.
func cfft(input, out []cpx, sign float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L408-L412
	n := len(input)
	s := getFFTScratch(n)
	fftRec(input, 1, n, sign, out, s.rec)
	putFFTScratch(s)
}

// rfftForwardOrdered is the forward real FFT of n real samples, re-packed into the
// ordered REAL layout: f[0]=DC.re, f[1]=Nyquist.re, then [re,im] pairs for bins
// 1..n/2-1. Output length is n.
func rfftForwardOrdered(time, f []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L416-L432
	n := len(time)
	s := getFFTScratch(n)
	cin := s.a
	for i := 0; i < n; i++ {
		// Whole-struct assignment, not cin[i].re = …: the pooled buffer may carry
		// a stale imaginary part from a previous transform, and the input is real.
		cin[i] = cpx{re: time[i]}
	}
	spec := s.b
	fftRec(cin, 1, n, -1.0, spec, s.rec)
	f[0] = spec[0].re
	f[1] = spec[n/2].re
	for i := 1; i < n/2; i++ {
		f[2*i] = spec[i].re
		f[2*i+1] = spec[i].im
	}
	putFFTScratch(s)
}
