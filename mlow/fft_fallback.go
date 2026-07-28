//go:build !(arm && cgo)

package mlow

// Non-ARM / no-cgo builds have no NEON kernel; cfft uses the pure-Go fftRec.
func neonCfft(input, out []cpx, sign float32) bool { return false }
