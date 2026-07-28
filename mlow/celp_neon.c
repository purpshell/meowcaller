// NEON-accelerated coarse CELP DSP kernels (webOS device build). These are the self-contained
// dot-product / FIR loops the encoder runs many times per frame; compiled with -mfpu=neon
// -ftree-vectorize -ffast-math the inner contiguous loops auto-vectorize. Only the COARSE functions
// live here (one cgo call does ~80+ dot products or a whole FIR pass) so the cgo call overhead is
// negligible; the tiny leaf dot-products stay in Go. Ports of the same-named Go functions; NOT
// bit-identical (-ffast-math reorders the accumulation), validated by PESQ.

// celpMultSymtoepl2: symmetric-Toeplitz matrix x vector (smpl_celp.rs). Three runs of a sliding
// dot-product with varying length/offset, exactly mirroring the Go.
void wa_celp_mult_symtoepl2(const float *c, int lResp, const float *x, float *y, int n) {
	int length = lResp;
	int nn = 0;
	for (; nn < lResp - 1; nn++) {
		const float *a = c + (lResp - 1 - nn);
		float r = 0.0f;
		for (int i = 0; i < length; i++) r += a[i] * x[i];
		y[nn] = r;
		length++;
	}
	length = 2 * lResp;
	for (; nn < n - lResp; nn++) {
		const float *b = x + (nn + 1 - lResp);
		float r = 0.0f;
		for (int i = 0; i < length; i++) r += c[i] * b[i];
		y[nn] = r;
	}
	for (; nn < n; nn++) {
		length--;
		const float *b = x + (nn + 1 - lResp);
		float r = 0.0f;
		for (int i = 0; i < length; i++) r += c[i] * b[i];
		y[nn] = r;
	}
}

// peCalcCE2: stage-1 pitch cross-correlation C + target energy E2 (smpl_pitch). For each subframe,
// numlags dense 20-tap dot-products of the target against lagged history -> the dominant coarse pitch
// kernel (one call/frame). The 20-tap inner loops vectorize.
void wa_pe_calc_ce2(float *c, float *e2, const float *ltpbuf, int ltpbufLen, int numsubfrs,
                    int lagSubfrlen, int minpitch, int numlags) {
	int t = ltpbufLen - lagSubfrlen * numsubfrs;
	for (int sf = 0; sf < numsubfrs; sf++) {
		const float *tgt = ltpbuf + t;
		int reg0 = t - minpitch;
		for (int i = 0; i < numlags; i++) {
			const float *r = ltpbuf + (reg0 - i);
			float s = 0.0f;
			for (int k = 0; k < 20; k++) s += tgt[k] * r[k];
			c[sf * numlags + i] = s;
		}
		t += lagSubfrlen;
		float e = 0.0f;
		for (int k = 0; k < 20; k++) e += tgt[k] * tgt[k];
		e2[sf] = e > 1e-9f ? e : 1e-9f;
	}
}

// wa_pe_corr40_row: the stage-2 40-tap correlation row (smpl_pitch): for each lag i whose harmonicity
// h passes the threshold, c[cbase+i] = 0.5 * dot40(x[xoff], x[xoff-(lagbase+i)]). One call per
// subframe-block (~32/frame) instead of per-lag; the 40-tap dot vectorizes.
void wa_pe_corr40_row(float *c, int cbase, const float *h, int hbase, const float *x, int xoff,
                      int lagbase, int n, float hthres) {
	for (int i = 0; i < n; i++) {
		if (h[hbase + i] > hthres) {
			const float *a = x + xoff;
			const float *b = x + xoff - (lagbase + i);
			float s = 0.0f;
			for (int k = 0; k < 40; k++) s += a[k] * b[k];
			c[cbase + i] = 0.5f * s;
		}
	}
}

// celpFiltMa: FIR (moving-average) filter y[k] = sum_i coef[i]*x[xBase+k-i]. The per-tap inner loops
// over k are contiguous and vectorize.
void wa_celp_filt_ma(const float *x, int xBase, int n, const float *coef, int coefLen, float *y) {
	int i;
	if (coef[0] == 1.0f) {
		for (int k = 0; k < n; k++) y[k] = x[xBase + k] + coef[1] * x[xBase + k - 1];
		i = 2;
	} else {
		for (int k = 0; k < n; k++) y[k] = coef[0] * x[xBase + k];
		i = 1;
	}
	for (; i < coefLen; i++) {
		float ci = coef[i];
		for (int k = 0; k < n; k++) y[k] += ci * x[xBase + k - i];
	}
}
