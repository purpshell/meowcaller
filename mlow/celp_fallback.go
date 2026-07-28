//go:build !(arm && cgo)

package mlow

// Non-ARM / no-cgo builds keep the pure-Go CELP kernels.
const celpNeonAvail = false

func neonMultSymtoepl2(c []float32, lResp int, x, y []float32, n int) {}
func neonFiltMa(x []float32, xBase, n int, coef []float32, coefLen int, y []float32) {}
func neonPeCalcCE2(c, e2, ltpbuf []float32, ltpbufLen, numsubfrs, lagSubfrlen, minpitch, numlags int) {}
func neonPeCorr40Row(c []float32, cbase int, h []float32, hbase int, x []float32, xoff, lagbase, n int, hthres float32) {
}
