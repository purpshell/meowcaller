package mlow

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

// LoadSmplSynthTables decodes the embedded synthesis tables once and returns the shared set.
func LoadSmplSynthTables() *SmplSynthTables {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L97-L104
	// TODO
	// agent suggestion: same protobuf-asset path as LoadSmplTables/LoadLsfCb — embed
	//   the reference's smpl_synth_tables.bin (zlib+protobuf tables.proto
	//   SmplSynthTables) at the package root, inflate + proto.Unmarshal via
	//   internal/tables, memoized with sync.Once.
	// human input:
	panic("mlow: LoadSmplSynthTables not yet implemented (scaffold)")
}

// SmplReconstructNLSF rebuilds the quantized NLSF from the stage indices and the
// previous frame's NLSF (the envelope the decoder synthesizes from).
func SmplReconstructNLSF(t *SmplSynthTables, stage1, config, grid int, stage2 *[16]int32, prevNLSF []float32) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L176-L234
	// TODO
	// agent suggestion: port smpl_reconstruct_nlsf — centroid + decorrelation-matrix
	//   reconstruction (grid==16 inverted by signal type), then the min-spacing
	//   stabilize loop; f64 accumulation truncated to f32 per the datasheet.
	// human input:
	panic("mlow: SmplReconstructNLSF not yet implemented (scaffold)")
}

// SmplNLSF2A converts NLSF to the monic LPC coefficient vector A[0..16].
func SmplNLSF2A(nlsf []float32) []float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L293-L311
	// TODO
	// agent suggestion: port smpl_nlsf2a — cosine-of-NLSF → P/Q polynomials → A; f64
	//   accumulation truncated to f32, keep the exact widening.
	// human input:
	panic("mlow: SmplNLSF2A not yet implemented (scaffold)")
}

// SmplGainLin maps the quantized log-gain to a linear gain (f64).
func SmplGainLin(gainQ int32) float64 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L350-L362
	// TODO
	// agent suggestion: port smpl_gain_lin — the math.Float32frombits raw reinterpret
	//   plus the saturating y-as-i32 cast with the 2147483648.0 bounds guarded.
	// human input:
	panic("mlow: SmplGainLin not yet implemented (scaffold)")
}

// SmplLTPFracGain maps the normalized LTP gain to the fractional gain.
func SmplLTPFracGain(normGain float64) float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L482-L484
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
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L493-L517
	// TODO
	// agent suggestion: zero-init the history/gain/LPC state buffers to the reference
	//   default lengths (SMPL_LTP_HIST + intf + 64, etc.).
	// human input:
	panic("mlow: NewSmplFrameSynth not yet implemented (scaffold)")
}

// SmplLTPSubframePred runs the fractional LTP/ACB prediction for one subframe,
// writing predOut from the history at the fractional lag.
func SmplLTPSubframePred(hist []float32, histPos int32, lagF, gainFrac float32, gst *SmplExcGainState, predOut []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L487-L506
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
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_synth.rs#L543-L662
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
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_celpdec.rs#L337-L349
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
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_celpdec.rs#L372-L488
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
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_nrgres.rs#L61-L101
	// TODO
	// agent suggestion: port quant_nrg_res_4 — frame-energy + shape-codebook search
	//   against NRGRES_SHAPE_CB_4_Q10, producing FrameQi/ShapeQi and the per-subframe
	//   DbqQ14 the decoder consumes as gainQ.
	// human input:
	panic("mlow: QuantNrgRes4 not yet implemented (scaffold)")
}
