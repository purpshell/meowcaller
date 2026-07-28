//go:build arm && cgo

package mlow

// NEON bridge for the coarse CELP kernels (webOS TouchPad build). Pure-Go fallback in celp_fallback.go.

// #cgo CFLAGS: -O3 -mfpu=neon -ffast-math -g0
// void wa_celp_mult_symtoepl2(const float *c, int lResp, const float *x, float *y, int n);
// void wa_celp_filt_ma(const float *x, int xBase, int n, const float *coef, int coefLen, float *y);
// void wa_pe_calc_ce2(float *c, float *e2, const float *ltpbuf, int ltpbufLen, int numsubfrs, int lagSubfrlen, int minpitch, int numlags);
// void wa_pe_corr40_row(float *c, int cbase, const float *h, int hbase, const float *x, int xoff, int lagbase, int n, float hthres);
import "C"

import "unsafe"

const celpNeonAvail = true

func neonMultSymtoepl2(c []float32, lResp int, x, y []float32, n int) {
	C.wa_celp_mult_symtoepl2((*C.float)(unsafe.Pointer(&c[0])), C.int(lResp),
		(*C.float)(unsafe.Pointer(&x[0])), (*C.float)(unsafe.Pointer(&y[0])), C.int(n))
}

func neonFiltMa(x []float32, xBase, n int, coef []float32, coefLen int, y []float32) {
	C.wa_celp_filt_ma((*C.float)(unsafe.Pointer(&x[0])), C.int(xBase), C.int(n),
		(*C.float)(unsafe.Pointer(&coef[0])), C.int(coefLen), (*C.float)(unsafe.Pointer(&y[0])))
}

func neonPeCalcCE2(c, e2, ltpbuf []float32, ltpbufLen, numsubfrs, lagSubfrlen, minpitch, numlags int) {
	C.wa_pe_calc_ce2((*C.float)(unsafe.Pointer(&c[0])), (*C.float)(unsafe.Pointer(&e2[0])),
		(*C.float)(unsafe.Pointer(&ltpbuf[0])), C.int(ltpbufLen), C.int(numsubfrs),
		C.int(lagSubfrlen), C.int(minpitch), C.int(numlags))
}

func neonPeCorr40Row(c []float32, cbase int, h []float32, hbase int, x []float32, xoff, lagbase, n int, hthres float32) {
	C.wa_pe_corr40_row((*C.float)(unsafe.Pointer(&c[0])), C.int(cbase),
		(*C.float)(unsafe.Pointer(&h[0])), C.int(hbase), (*C.float)(unsafe.Pointer(&x[0])),
		C.int(xoff), C.int(lagbase), C.int(n), C.float(hthres))
}
