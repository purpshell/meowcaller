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

// fftRec is the recursive mixed-radix Cooley-Tukey DFT. sign is -1 forward, +1
// inverse (unnormalized). x holds n inputs at the given stride; out is contiguous.
func fftRec(x []cpx, stride, n int, sign float32, out []cpx, scratch []cpx) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L362-L405
	if n == 1 {
		out[0] = x[0]
		return
	}
	tw := twiddles(n, sign)
	p := smallestFactor(n)
	if p == n {
		for k := 0; k < n; k++ {
			var acc cpx
			idx := 0
			for j := 0; j < n; j++ {
				acc = acc.add(x[j*stride].mul(tw[idx]))
				idx += k
				if idx >= n {
					idx -= n
				}
			}
			out[k] = acc
		}
		return
	}
	m := n / p
	var sub []cpx
	if len(scratch) >= 2*n {
		sub = scratch[:n]
		scratch = scratch[n:]
	} else {
		sub = make([]cpx, n)
		scratch = nil
	}
	for q := 0; q < p; q++ {
		fftRec(x[q*stride:], stride*p, m, sign, sub[q*m:(q+1)*m], scratch)
	}
	for k := 0; k < n; k++ {
		kmod := k % m
		var acc cpx
		idx := 0
		for q := 0; q < p; q++ {
			acc = acc.add(sub[q*m+kmod].mul(tw[idx]))
			idx += k
			if idx >= n {
				idx -= n
			}
		}
		out[k] = acc
	}
}

// twiddleCache holds the roots of unity per (size, sign). Without it, fftRec
// called math.Cos/math.Sin once per (k,j) pair, even though only n distinct
// values exist. In a CPU profile of a 60 ms frame going through MlowEncoder,
// those two functions alone accounted for ~39% of the whole media path.
var twiddleCache sync.Map // map[twiddleKey][]cpx

type twiddleKey struct {
	n    int
	sign float32
}

// twiddles returns w[i] = e^{sign*2*pi*i/n}, indexed by (k*j) mod n. Indexing
// by the remainder is also more accurate than scaling the angle: the previous
// code computed sign*2*pi*k*j/n in float32, so the error grew with k*j.
func twiddles(n int, sign float32) []cpx {
	chave := twiddleKey{n: n, sign: sign}
	if v, ok := twiddleCache.Load(chave); ok {
		return v.([]cpx)
	}
	tab := make([]cpx, n)
	for i := 0; i < n; i++ {
		ang := float64(sign) * 2.0 * math.Pi * float64(i) / float64(n)
		tab[i] = cpx{re: float32(math.Cos(ang)), im: float32(math.Sin(ang))}
	}
	v, _ := twiddleCache.LoadOrStore(chave, tab)
	return v.([]cpx)
}

// scratchPool reuses the recursion buffer. Previously every node of the
// recursion tree allocated its own make([]cpx, n): ~3,800 allocations per 60 ms
// frame, or roughly 28 MB/s of garbage per concurrent call.
var scratchPool sync.Pool

func pegaScratch(n int) []cpx {
	if v := scratchPool.Get(); v != nil {
		b := v.([]cpx)
		if cap(b) >= n {
			return b[:n]
		}
	}
	return make([]cpx, n)
}

// cfft computes the complex FFT of a mixed-radix length into out. sign=-1 forward,
// +1 inverse.
func cfft(input, out []cpx, sign float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L408-L412
	n := len(input)
	scratch := pegaScratch(2 * n)
	fftRec(input, 1, n, sign, out, scratch)
	scratchPool.Put(scratch[:cap(scratch)]) //nolint:staticcheck // buffer is reused by capacity
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
