package mlow

// CELP decoder-side noise generator: builds the shaped residual noise the CELP
// synthesis mixes into the excitation (smpl_gennoise.rs). The perceptual-weighting
// front-end and bitrate controller the datasheet also bundles are encoder/analysis
// concerns and are scaffolded with the encoder module, not here.

// NoiseGenerator is the persistent decoder-side noise generator state.
type NoiseGenerator struct {
	EnvSmth       float32
	EnvLast       float32
	OutStateUV    [2]float32
	OutStateV     [2]float32
	CorrSmth      [3]float32 // NOISE_CORR_ORDER + 1
	ShapeState    [2]float32 // NOISE_CORR_ORDER
	PrevVoiced    bool
	SinceUnvoiced int32
	RandSeed      int32
}

// NewNoiseGenerator allocates a zeroed noise generator.
func NewNoiseGenerator() *NoiseGenerator {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_gennoise.rs#L359-L374
	// TODO
	// agent suggestion: return &NoiseGenerator{} — the reference Default is all-zero
	//   except any seed; confirm RandSeed's initial value against the reference.
	// human input:
	panic("mlow: NewNoiseGenerator not yet implemented (scaffold)")
}

// SmplGetNormalizedBitrate maps the per-frame pulse count to the normalized bitrate.
func SmplGetNormalizedBitrate(numPulses, frameLength16 int32) float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_gennoise.rs#L329-L332
	// TODO
	// agent suggestion: port smpl_get_normalized_bitrate (the short pulses→rate map).
	// human input:
	panic("mlow: SmplGetNormalizedBitrate not yet implemented (scaffold)")
}

// SmplDecodeResnrg maps the quantized residual-energy floor to a linear residual energy.
func SmplDecodeResnrg(nrgresFrameDbqQ14, fcbSubfrlen int32) float32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_gennoise.rs#L336-L343
	// TODO
	// agent suggestion: port smpl_decode_resnrg (Q14 dB floor → linear energy).
	// human input:
	panic("mlow: SmplDecodeResnrg not yet implemented (scaffold)")
}

// SmplCelpGenNoise builds the shaped residual noise for one subframe (writes l
// samples into noise).
func SmplCelpGenNoise(ng *NoiseGenerator, excLpc []float32, l int, voiced bool, numPulses int32, nrgres float32, fcbgIdx int32, lsf []float32, normalizedBitrate float32, fcbgainsUV []float32, noise []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f359a086b28e807ba236f0977af1000859fe/wacore/src/voip/mlow/smpl_gennoise.rs#L416-L611
	// TODO
	// agent suggestion: port smpl_celp_gen_noise — voiced path (LPC-residual
	//   autocorrelation → shaped noise) vs unvoiced (white→envelope-shaped), LCG
	//   randomness via ng.RandSeed, energy match to nrgres, smoothed across calls.
	// human input:
	panic("mlow: SmplCelpGenNoise not yet implemented (scaffold)")
}
