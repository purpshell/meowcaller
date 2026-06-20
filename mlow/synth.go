package mlow

import (
	"bytes"
	"compress/zlib"
	_ "embed"
	"io"
	"math"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/purpshell/meowcaller/mlow/internal/tables"
)

// Low-band synthesis: NLSF reconstruction, NLSF→LPC, gain linearization, LTP/ACB
// excitation prediction, and the per-internal-frame synthesis that turns decoded
// parameters into PCM. Validated end-to-end via the decoder (module #15).

const (
	SmplOrder      = 16
	SmplSubfrLen   = 80  // 5 ms @ 16 kHz
	SmplIntfLen    = 320 // 20 ms internal frame
	SmplSubfrCount = 4
	SmplLtpHist    = 728
)

const (
	smplPiF32            = float32(3.1415927410125)
	smplNLSFWeightWMax   = float32(999.9999)
	smplNLSFWeightEps    = float32(0.0009999999)
	smplStabilizeMaxLoop = 1000
	smplStabilizeEps     = float32(9.5367431640625e-07)
)

// smplSynthTablesBlob is the runtime synthesis tables as a zlib-compressed
// SmplSynthTables protobuf — the reference's byte-identical smpl_synth_tables.bin,
// embedded at the package root (production asset, reference filename).
//
//go:embed smpl_synth_tables.bin
var smplSynthTablesBlob []byte

// --- NLSF reconstruction / synthesis tables ---

// SmplSynthTables is the runtime synthesis table set (the smpl_synth_tables dump).
type SmplSynthTables struct {
	Valtables      [][][][][]float32 // [stage1][config][grid][coeff][sym]
	Centroids      [][][]float32     // [stage1][grid][16]
	Matrices       [][][][]float32   // [stage1][grid][row][col]
	MinSpacing     [][]float32       // [stage1][17]
	Grid16W        [][]float32
	Grid16Alpha    []float32
	Grid16Matrices [][][]float32 // [sig][config][256]
}

var (
	smplSynthOnce   sync.Once
	smplSynthTables *SmplSynthTables
)

// LoadSmplSynthTables decodes the embedded synthesis tables once and returns the shared set.
func LoadSmplSynthTables() *SmplSynthTables {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L97-L104
	smplSynthOnce.Do(func() {
		zr, err := zlib.NewReader(bytes.NewReader(smplSynthTablesBlob))
		if err != nil {
			panic("mlow: open synth table blob: " + err.Error())
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			panic("mlow: inflate synth table blob: " + err.Error())
		}
		_ = zr.Close()
		var pb tables.SmplSynthTables
		if err := proto.Unmarshal(raw, &pb); err != nil {
			panic("mlow: decode synth table blob: " + err.Error())
		}
		smplSynthTables = &SmplSynthTables{
			Valtables:      f5ToGo(pb.GetValtables()),
			Centroids:      f3ToGo(pb.GetCentroids()),
			Matrices:       f4ToGo(pb.GetMatrices()),
			MinSpacing:     f2ToGo(pb.GetMinSpacing()),
			Grid16W:        f2ToGo(pb.GetGrid16W()),
			Grid16Alpha:    pb.GetGrid16Alpha(),
			Grid16Matrices: f3ToGo(pb.GetGrid16Matrices()),
		}
	})
	return smplSynthTables
}

func f4ToGo(m *tables.F4) [][][][]float32 {
	d := m.GetD()
	out := make([][][][]float32, len(d))
	for i, r := range d {
		out[i] = f3ToGo(r)
	}
	return out
}

func f5ToGo(m *tables.F5) [][][][][]float32 {
	d := m.GetD()
	out := make([][][][][]float32, len(d))
	for i, r := range d {
		out[i] = f4ToGo(r)
	}
	return out
}

// smplNLSFLaroiaWeights: inverse-gap weights w[k] = invgap[k] + invgap[k+1] (silk_NLSF_VQ_weights_laroia).
func smplNLSFLaroiaWeights(nlsf, out []float32) {
	var inv [SmplOrder + 1]float32
	clamp := func(gap float32) float32 {
		if gap > smplNLSFWeightEps {
			return 1.0 / gap
		}
		return smplNLSFWeightWMax
	}
	inv[0] = clamp(nlsf[0])
	prev := nlsf[0]
	for k := 1; k < SmplOrder; k++ {
		inv[k] = clamp(nlsf[k] - prev)
		prev = nlsf[k]
	}
	inv[SmplOrder] = clamp(smplPiF32 - nlsf[SmplOrder-1])
	for k := 0; k < SmplOrder; k++ {
		out[k] = inv[k] + inv[k+1]
	}
}

// smplNLSFDecorr: out[r] = sum_c mat[c*16 + r] * vec[c] (column-major decorrelation matrix).
func smplNLSFDecorr(mat, vec, out []float32) {
	var scr [SmplOrder]float32
	v0 := vec[0]
	for r := 0; r < SmplOrder; r++ {
		scr[r] = v0 * mat[r]
	}
	for c := 1; c < SmplOrder; c++ {
		v := vec[c]
		base := c * SmplOrder
		for r := 0; r < SmplOrder; r++ {
			scr[r] += mat[base+r] * v
		}
	}
	copy(out[:SmplOrder], scr[:])
}

// smplStabilizeNLSF enforces minimum spacing + ordering in the margin domain (silk_NLSF_stabilize).
func smplStabilizeNLSF(nlsf, minSpacing []float32) {
	const L = SmplOrder
	var marg [L + 1]float32
	marg[0] = nlsf[0] - minSpacing[0]
	for i := 1; i < L; i++ {
		marg[i] = nlsf[i] - nlsf[i-1] - minSpacing[i]
	}
	marg[L] = smplPiF32 - nlsf[L-1] - minSpacing[L]
	argmin := func() (float32, int) {
		m := marg[0]
		idx := 0
		for i := 1; i < L+1; i++ {
			if marg[i] < m {
				m = marg[i]
				idx = i
			}
		}
		return m, idx
	}
	min, sel := argmin()
	loopN := 0
	for min < 0.0 {
		d := float32(loopN)*smplStabilizeEps - min
		if sel == 0 {
			marg[0] += d
			marg[1] -= d
		} else if sel == L {
			marg[L] += d
			marg[L-1] -= d
		} else {
			marg[sel] += d
			half := d * 0.5
			marg[sel-1] -= half
			marg[sel+1] -= half
		}
		m, s := argmin()
		min = m
		sel = s
		if min < 0.0 {
			loopN++
			if loopN == smplStabilizeMaxLoop {
				break
			}
		}
	}
	nlsf[0] = minSpacing[0] + marg[0]
	run := nlsf[0]
	for i := 1; i < L; i++ {
		run = run + marg[i] + minSpacing[i]
		nlsf[i] = run
	}
}

// SmplReconstructNLSF rebuilds the quantized NLSF from the stage indices and the
// previous frame's NLSF (the envelope the decoder synthesizes from).
func SmplReconstructNLSF(t *SmplSynthTables, stage1, config, grid int, stage2 *[16]int32, prevNLSF []float32) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L176-L234
	val := t.Valtables[stage1][config][grid]
	var resid [SmplOrder]float32
	for k := 0; k < SmplOrder; k++ {
		sym := stage2[k]
		if sym >= 0 && int(sym) < len(val[k]) {
			resid[k] = val[k][sym]
		}
	}

	out := make([]float32, SmplOrder)
	if grid == 16 {
		// grid==16: interpolate base between prevNLSF and the inverted grid16 base table.
		var base [SmplOrder]float32
		baseTbl := t.Grid16W[1-stage1]
		alpha := t.Grid16Alpha[stage1]
		for k := 0; k < SmplOrder; k++ {
			var pv float32
			if k < len(prevNLSF) {
				pv = prevNLSF[k]
			}
			base[k] = pv + alpha*(baseTbl[k]-pv)
		}
		var w [SmplOrder]float32
		smplNLSFLaroiaWeights(base[:], w[:])
		for i := range w {
			w[i] = float32(math.Sqrt(float64(w[i])))
		}
		var decorr [SmplOrder]float32
		smplNLSFDecorr(t.Grid16Matrices[stage1][config], resid[:], decorr[:])
		for k := 0; k < SmplOrder; k++ {
			out[k] = base[k] + decorr[k]/w[k]
		}
		smplStabilizeNLSF(out, t.MinSpacing[stage1])
		return out
	}

	// matrix case (grid < 16): NLSF[r] = 2*centroid[r] + sum_c mat[c][r]*resid[c].
	cent := t.Centroids[stage1][grid]
	mat := t.Matrices[stage1][grid]
	for r := 0; r < SmplOrder; r++ {
		acc := 2.0 * cent[r]
		for c := 0; c < SmplOrder; c++ {
			acc += mat[c][r] * resid[c]
		}
		out[r] = acc
	}
	smplStabilizeNLSF(out, t.MinSpacing[stage1])
	return out
}

// SmplNLSF2A converts NLSF to the monic LPC coefficient vector A[0..16].
func SmplNLSF2A(nlsf []float32) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L293-L311
	// TODO
	// agent suggestion: port smpl_nlsf2a — cosine-of-NLSF → P/Q polynomials → A; f64
	//   accumulation truncated to f32, keep the exact widening.
	// human input:
	panic("mlow: SmplNLSF2A not yet implemented (scaffold)")
}

// SmplGainLin maps the quantized log-gain to a linear gain (f64).
func SmplGainLin(gainQ int32) float64 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L350-L362
	// TODO
	// agent suggestion: port smpl_gain_lin — the math.Float32frombits raw reinterpret
	//   plus the saturating y-as-i32 cast with the 2147483648.0 bounds guarded.
	// human input:
	panic("mlow: SmplGainLin not yet implemented (scaffold)")
}

// SmplLTPFracGain maps the normalized LTP gain to the fractional gain.
func SmplLTPFracGain(normGain float64) float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L482-L484
	// TODO
	// agent suggestion: port smpl_ltp_frac_gain (the short scalar map).
	// human input:
	panic("mlow: SmplLTPFracGain not yet implemented (scaffold)")
}

// --- low-band synthesis (WASM func 3597 core) ---

// SmplExcGainState is the 2-tap excitation-gain smoother state.
type SmplExcGainState struct {
	S0 float32
	S1 float32
}

// SmplPitchSynth carries the per-internal-frame pitch synthesis inputs.
type SmplPitchSynth struct {
	Voiced   bool
	LagSubfr [4]float64
	NormGain float64
}

// SmplFrameSynth is the cross-internal-frame low-band synthesis state (LPC state,
// LTP/excitation history, gain state, excitation + HP postfilter state).
type SmplFrameSynth struct{}

// NewSmplFrameSynth allocates a zeroed low-band synthesis state.
func NewSmplFrameSynth() *SmplFrameSynth {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L493-L517
	// TODO
	// agent suggestion: zero-init the history/gain/LPC state buffers to the reference
	//   default lengths (SMPL_LTP_HIST + intf + 64, etc.).
	// human input:
	panic("mlow: NewSmplFrameSynth not yet implemented (scaffold)")
}

// SmplLTPSubframePred runs the fractional LTP/ACB prediction for one subframe,
// writing predOut from the history at the fractional lag.
func SmplLTPSubframePred(hist []float32, histPos int32, lagF, gainFrac float32, gst *SmplExcGainState, predOut []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L487-L506
	// TODO
	// agent suggestion: port smpl_ltp_subframe_pred — integer+fractional lag via the
	//   8-tap interpolation kernel, negative-offset history indexing into one slice.
	// human input:
	panic("mlow: SmplLTPSubframePred not yet implemented (scaffold)")
}

// SynthInternalFrame synthesizes one internal (20 ms) frame, returning the PCM
// signal and the reconstructed nlsf (which becomes the next frame's prevNLSF).
func SynthInternalFrame(
	t *SmplSynthTables,
	st *SmplFrameSynth,
	stage1, config, grid int,
	stage2 *[16]int32,
	prevNLSF []float32,
	pulses []int32,
	gainQ *[4]int32,
	pitch *SmplPitchSynth,
) (signal []float32, nlsf []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_synth.rs#L543-L662
	// TODO
	// agent suggestion: port synth_internal_frame — NLSF reconstruct → nlsf2a → per-
	//   subframe LTP/excitation build → LPC synthesis (f64→f32). Region-1 comb and HP
	//   postfilter are gated off (SMPL_TAIL_REGION1/SMPL_HP_POSTFILTER = false).
	// human input:
	panic("mlow: SynthInternalFrame not yet implemented (scaffold)")
}

// --- C-float-domain CELP synthesis (smpl_core_decoder.c) ---

// CelpDecParams holds the per-frame CELP decode parameters.
type CelpDecParams struct {
	Voiced       bool
	SfPulses     [4]int32
	FcbgIdx      [4]int32
	NrgresDbqQ14 [4]int32
	AcbgIdx      [4]int32
	BlockLags    [8]float32 // per-40-block pitch lag (codec units), 0 for unvoiced
	TotalPulses  int32
}

// CelpDecState is the cross-frame CELP decoder state (noise generator, ACB state,
// LPC synthesis memory, prev LSF, HP postfilter state).
type CelpDecState struct{}

// NewCelpDecState allocates a zeroed CELP decoder state.
func NewCelpDecState() *CelpDecState {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_celpdec.rs#L337-L349
	// TODO
	// agent suggestion: zero-init the ACB/LPC/noise/postfilter state to the reference
	//   default lengths.
	// human input:
	panic("mlow: NewCelpDecState not yet implemented (scaffold)")
}

// SynthFrame synthesizes one frame in the C-float CELP domain, writing PCM to out.
func (s *CelpDecState) SynthFrame(
	nlsf []float32,
	lsfInterpolIdx int,
	pulses []int32,
	params *CelpDecParams,
	lowRate bool,
	frameLength16 int32,
	out []float32,
) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_celpdec.rs#L372-L488
	// TODO
	// agent suggestion: port CelpDecState::synth_frame — LSF interpolation, per-
	//   subframe ACB(pitch)+FCB(pulses)+noise excitation with gains, LPC synthesis,
	//   then the HP postfilter (always run on this path). Reaches into noise (#13),
	//   postfilter (#11), and the ACB gain tables.
	// human input:
	panic("mlow: CelpDecState.SynthFrame not yet implemented (scaffold)")
}

// --- unvoiced residual-energy quantizer (smpl_quant_nrg_res.c) ---

// NrgResQuant is the quantized residual-energy result; DbqQ14 is what the decoder
// reads as gainQ.
type NrgResQuant struct {
	FrameQi int32
	ShapeQi int32
	DbqQ14  [4]int32
}

// QuantNrgRes4 quantizes the 4-subframe residual-energy vector.
func QuantNrgRes4(nrgres *[4]float32) NrgResQuant {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_nrgres.rs#L61-L101
	// TODO
	// agent suggestion: port quant_nrg_res_4 — frame-energy + shape-codebook search
	//   against NRGRES_SHAPE_CB_4_Q10, producing FrameQi/ShapeQi and the per-subframe
	//   DbqQ14 the decoder consumes as gainQ.
	// human input:
	panic("mlow: QuantNrgRes4 not yet implemented (scaffold)")
}
