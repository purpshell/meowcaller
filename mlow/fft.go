package mlow

import (
	"math"
	"sync"
)

// twiddleCache memoizes the per-length roots of unity W[m] = exp(sign*2*pi*i*m/n), keyed by (n,sign).
// The FFT twiddles are constants that depend only on (n,sign); the recursive DFT below recomputed
// math.Cos/math.Sin for every element of every transform, which profiled at ~half of the entire MLow
// encode time (see the commit message benchmarks). The analysis calls the FFT at a handful of fixed
// sizes each frame, so after the first frame every angle is a table lookup. The table is indexed by the
// reduced index m in [0,n): mathematically identical since cos/sin are 2*pi periodic, and numerically
// cleaner than forming the angle from the float32 k*j product.
var twiddleCache sync.Map // key uint64 (n<<1 | signbit) -> []cpx of length n

func twiddles(n int, sign float32) []cpx {
	key := uint64(n) << 1
	if sign > 0 {
		key |= 1
	}
	if v, ok := twiddleCache.Load(key); ok {
		return v.([]cpx)
	}
	t := make([]cpx, n)
	for m := 0; m < n; m++ {
		ang := float64(sign) * 2.0 * math.Pi * float64(m) / float64(n)
		t[m] = cpx{re: float32(math.Cos(ang)), im: float32(math.Sin(ang))}
	}
	twiddleCache.Store(key, t)
	return t
}

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

// fftRec is the recursive mixed-radix Cooley-Tukey DFT. sign is -1 forward, +1
// inverse (unnormalized). x holds n inputs at the given stride; out is contiguous.
func fftRec(x []cpx, stride, n int, sign float32, out []cpx) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L362-L405
	if n == 1 {
		out[0] = x[0]
		return
	}
	p := smallestFactor(n)
	if p == n {
		w := twiddles(n, sign) // W[m] = exp(sign*2pi*i*m/n); index by (k*j) mod n
		for k := 0; k < n; k++ {
			var acc cpx
			for j := 0; j < n; j++ {
				acc = acc.add(x[j*stride].mul(w[(k*j)%n]))
			}
			out[k] = acc
		}
		return
	}
	m := n / p
	sub := make([]cpx, n)
	for q := 0; q < p; q++ {
		fftRec(x[q*stride:], stride*p, m, sign, sub[q*m:(q+1)*m])
	}
	w := twiddles(n, sign) // twiddle exp(sign*2pi*i*k*q/n) -> index (k*q) mod n
	for k := 0; k < n; k++ {
		kmod := k % m
		var acc cpx
		for q := 0; q < p; q++ {
			acc = acc.add(sub[q*m+kmod].mul(w[(k*q)%n]))
		}
		out[k] = acc
	}
}

// cfft computes the complex FFT of a mixed-radix length into out. sign=-1 forward,
// +1 inverse.
func cfft(input, out []cpx, sign float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L408-L412
	fftRec(input, 1, len(input), sign, out)
}

// rfftForwardOrdered is the forward real FFT of n real samples, re-packed into the
// ordered REAL layout: f[0]=DC.re, f[1]=Nyquist.re, then [re,im] pairs for bins
// 1..n/2-1. Output length is n.
func rfftForwardOrdered(time, f []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L416-L432
	n := len(time)
	cin := make([]cpx, n)
	for i := 0; i < n; i++ {
		cin[i].re = time[i]
	}
	spec := make([]cpx, n)
	cfft(cin, spec, -1.0)
	f[0] = spec[0].re
	f[1] = spec[n/2].re
	for i := 1; i < n/2; i++ {
		f[2*i] = spec[i].re
		f[2*i+1] = spec[i].im
	}
}
