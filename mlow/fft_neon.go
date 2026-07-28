//go:build arm && cgo

package mlow

// NEON FFT bridge. On ARM with cgo (the webOS TouchPad build) route the FFT through the
// auto-vectorized C kernel in fft_neon.c; everywhere else the build-tag fallback (fft_fallback.go)
// keeps the pure-Go path. cpx is {re,im float32} with no padding, so a []cpx aliases a contiguous
// interleaved float32 buffer that the C side reads as its cpxf.

// #cgo CFLAGS: -O3 -mfpu=neon -ffast-math -g0
// void wa_neon_cfft(const float *in, float *out, int n, float sign);
import "C"

import "unsafe"

func neonCfft(input, out []cpx, sign float32) bool {
	n := len(input)
	if n == 0 || len(out) < n {
		return false
	}
	C.wa_neon_cfft(
		(*C.float)(unsafe.Pointer(&input[0])),
		(*C.float)(unsafe.Pointer(&out[0])),
		C.int(n), C.float(sign))
	return true
}
