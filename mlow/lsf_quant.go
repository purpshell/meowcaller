package mlow

// LSFCBCentroids is the number of stage-1 LSF codebook centroids. SmplLPCOrder
// (the 16-tap LPC order) is shared with lpc.go.
const LSFCBCentroids = 16

// LsfQuantResult is one LSF quantization: Qi[0] (=grid), Qi[1..16] (=stage2), and
// the reconstructed quantized NLSF — the same envelope the decoder rebuilds.
type LsfQuantResult struct {
	Qi   [SmplLPCOrder + 1]int32
	QLsf [SmplLPCOrder]float32
}

// st1Tables is the per-codebook (voiced/unvoiced) stage-1 table set, mirroring the
// reference St1Json layout dumped from the C smpl_get_lsf_CBks().
type st1Tables struct {
	Cbhalf   [][]float32   // [16][16]
	CInv     [][]float32   // [16][16]
	BitsCond []float32     // [17]
	Rotcond  [][][]float32 // [2][16][16]
	CbCinv   [][]float32   // [16][16]
	We       [][][]float32 // [16][16][16]
	Bits     []float32     // [16]
	Wie      [][][]float32 // [16][16][16]
}

// st2Tables is one stage-2 table set (per voiced/lowRate/qi1); the per-coeff Qlvls
// and NumBits rows are ragged, so they stay slices.
type st2Tables struct {
	NumQlvls []int32
	Qlvls    [][]float32 // [16][numQlvls[i]]
	NumBits  [][]float32 // [16][numQlvls[i]]
}

// LsfCb holds the loaded LSF codebook tables (the C smpl_get_lsf_CBks() output plus
// the static smpl_lsf_tables.c constants).
type LsfCb struct {
	St1       []st1Tables     // [2]
	St2       [][][]st2Tables // [2][2][17]
	MinQi     [][][][]int32   // [2][2][17][16]
	MaxQi     [][][][]int32   // [2][2][17][16]
	Qstep     [][]float32     // [2][2]
	MeanV     []float32       // [16]
	MeanUV    []float32       // [16]
	RegCond   []float32       // [2]
	MinDistV  []float32       // [17]
	MinDistUV []float32       // [17]
}

// LoadLsfCb decodes the embedded LSF codebook once and returns the shared,
// read-only set.
func LoadLsfCb() *LsfCb {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/c697c36ffa7875c304ceea9154be30b66cada914/wacore/src/voip/mlow/smpl_lsf_quant.rs#L79-L84
	// TODO
	// agent suggestion: same asset path as LoadSmplTables — the reference's
	//   lsf_cb_dump is postcard (Go can't read it), so it needs the protobuf
	//   treatment: a new tables.proto message, regen lsf_cb_dump.bin as
	//   zlib+protobuf in the reference (push), copy to mlow/ (package root), then
	//   inflate + proto.Unmarshal + narrow here. Asset task, pending your go-ahead.
	// human input:
	panic("mlow: LoadLsfCb not yet implemented (scaffold)")
}

// LsfWeightsLaroia is the Laroia LSF weighting (inverse adjacent-spacing sum) used
// by the conditional path's rotation weighting.
func LsfWeightsLaroia(lsf []float32) [SmplLPCOrder]float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/c697c36ffa7875c304ceea9154be30b66cada914/wacore/src/voip/mlow/smpl_lsf_quant.rs#L203-L216
	// TODO
	// agent suggestion: inv_delta over consecutive spacings clamped to 1e-3 (with
	//   lsf[0] and PI-lsf[15] as the end gaps); lsfw[i] = inv_delta[i]+inv_delta[i+1].
	// human input:
	panic("mlow: LsfWeightsLaroia not yet implemented (scaffold)")
}

// LsfQuant is the non-conditional LSF quantization (smpl_lsf_quant). a is the monic
// LPC A[0..16] (A[0]=1); nlsf is the analysis NLSF; voiced/lowRate select the
// codebook; surv is the RD beam width.
func LsfQuant(a, nlsf []float32, voiced, lowRate int, rdWAdj float32, surv int) LsfQuantResult {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/c697c36ffa7875c304ceea9154be30b66cada914/wacore/src/voip/mlow/smpl_lsf_quant.rs#L478-L488
	// TODO
	// agent suggestion: load the codebook, then call the shared core with no cond
	//   params (this is the bit-exact float port — its RD beam logic is the
	//   load-bearing engineering content; awaiting your direction).
	// human input:
	panic("mlow: LsfQuant not yet implemented (scaffold)")
}

// LsfQuantCond is the conditional LSF quantization given the previous frame's
// quantized NLSF (smpl_lsf_quant_cond). a is the monic LPC A[0..16] (A[0]=1).
func LsfQuantCond(a, nlsf, lsfqPrev []float32, voiced, lowRate int, rdWAdj float32, surv int) LsfQuantResult {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/c697c36ffa7875c304ceea9154be30b66cada914/wacore/src/voip/mlow/smpl_lsf_quant.rs#L492-L525
	// TODO
	// agent suggestion: build the cond centroid (reg-blended prev NLSF, half-centroid,
	//   C_inv projection, rot_apply_wght for we/wie) then call the shared core with it.
	// human input:
	panic("mlow: LsfQuantCond not yet implemented (scaffold)")
}
