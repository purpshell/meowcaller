// NEON-accelerated mixed-radix FFT for the MLow encoder's hot path.
//
// A straight port of the Go fftRec (same recursion, same twiddle convention W[m]=exp(sign*2pi*i*m/n)
// indexed by the reduced index m), compiled with -mfpu=neon -ftree-vectorize -ffast-math so the
// compiler auto-vectorizes the contiguous radix-2 butterfly (the whole tree for the 512-pt LPC FFT)
// and the radix-p combine (the 576 = 2^6*3^2 perceptual FFT). One cgo call per transform, so the
// call overhead is negligible against O(n log n) work.
//
// NOT bit-identical to the Go path: -ffast-math reorders float ops. Validated by PESQ instead.
#include <math.h>
#include <stdlib.h>
#include <pthread.h>

typedef struct { float re, im; } cpxf;

// twiddle cache: W[m] = exp(sign*2*pi*i*m/n), one table per (n, sign). The recursion touches only a
// handful of sizes (512,256,..2 and 576,288,..3), so a tiny linear cache under a mutex is plenty; it
// is read-mostly after the first frame and callable from the concurrent encode/decode goroutines.
#define WA_FFT_MAXTW 48
static struct { int n; float sign; cpxf *t; } wa_tw[WA_FFT_MAXTW];
static int wa_tw_n = 0;
static pthread_mutex_t wa_tw_mx = PTHREAD_MUTEX_INITIALIZER;

static const cpxf *wa_get_tw(int n, float sign) {
	pthread_mutex_lock(&wa_tw_mx);
	for (int i = 0; i < wa_tw_n; i++) {
		if (wa_tw[i].n == n && wa_tw[i].sign == sign) {
			const cpxf *r = wa_tw[i].t;
			pthread_mutex_unlock(&wa_tw_mx);
			return r;
		}
	}
	cpxf *t = (cpxf *)malloc(sizeof(cpxf) * n);
	for (int m = 0; m < n; m++) {
		double a = (double)sign * 2.0 * M_PI * (double)m / (double)n;
		t[m].re = (float)cos(a);
		t[m].im = (float)sin(a);
	}
	const cpxf *r = t;
	if (wa_tw_n < WA_FFT_MAXTW) {
		wa_tw[wa_tw_n].n = n;
		wa_tw[wa_tw_n].sign = sign;
		wa_tw[wa_tw_n].t = t;
		wa_tw_n++;
	} // else: leak the (bounded) overflow table rather than risk unbounded growth
	pthread_mutex_unlock(&wa_tw_mx);
	return r;
}

static int wa_smallest_factor(int n) {
	if (n % 2 == 0) return 2;
	for (int p = 3; (long)p * p <= n; p += 2)
		if (n % p == 0) return p;
	return n;
}

// out[0:n] = DFT of x[0:n:stride]; scratch is a caller-owned arena (>= ~2n) reused down the recursion.
static void wa_fftrec(const cpxf *x, int stride, int n, float sign, cpxf *out, cpxf *scratch) {
	if (n == 1) {
		out[0] = x[0];
		return;
	}
	int p = wa_smallest_factor(n);
	const cpxf *w = wa_get_tw(n, sign);
	if (p == n) { // prime (only small n=2,3 in practice) -> direct DFT
		for (int k = 0; k < n; k++) {
			float accre = 0.0f, accim = 0.0f;
			for (int j = 0; j < n; j++) {
				cpxf xj = x[j * stride];
				cpxf wk = w[(k * j) % n];
				accre += xj.re * wk.re - xj.im * wk.im;
				accim += xj.re * wk.im + xj.im * wk.re;
			}
			out[k].re = accre;
			out[k].im = accim;
		}
		return;
	}
	int m = n / p;
	cpxf *sub = scratch;              // sub[0:n] holds the p child transforms
	cpxf *child = scratch + n;        // transient scratch for each child (reused; children run serially)
	for (int q = 0; q < p; q++)
		wa_fftrec(x + q * stride, stride * p, m, sign, sub + q * m, child);
	if (p == 2) {
		// radix-2 butterfly: out[k] = even[kmod] + W[k]*odd[kmod]. Contiguous -> vectorizes.
		for (int k = 0; k < n; k++) {
			int km = k >= m ? k - m : k;
			cpxf e = sub[km], o = sub[m + km], tw = w[k];
			float orr = o.re * tw.re - o.im * tw.im;
			float oii = o.re * tw.im + o.im * tw.re;
			out[k].re = e.re + orr;
			out[k].im = e.im + oii;
		}
	} else {
		for (int k = 0; k < n; k++) {
			int km = k % m;
			float accre = 0.0f, accim = 0.0f;
			for (int q = 0; q < p; q++) {
				cpxf s = sub[q * m + km];
				cpxf tw = w[(k * q) % n];
				accre += s.re * tw.re - s.im * tw.im;
				accim += s.re * tw.im + s.im * tw.re;
			}
			out[k].re = accre;
			out[k].im = accim;
		}
	}
}

// Entry point called from Go. in/out are interleaved re,im (== Go []cpx). n <= 576 in MLow.
void wa_neon_cfft(const float *in, float *out, int n, float sign) {
	cpxf arena[576 * 4]; // recursion arena; the depth-summed size for n<=576 is ~1150 cpx
	wa_fftrec((const cpxf *)in, 1, n, sign, (cpxf *)out, arena);
}
