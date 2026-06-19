package mlow

// Excitation pulse decode (PVQ-style) for one internal frame: the total pulse
// count, the recursive split across subframes, the per-position magnitudes, and the
// signs — read straight from the range-coded bitstream against the heap-window ROM.

// smplPulseCountByte is the static gain-helper table at rodata 0xe8990, indexed by
// [config*3 + (p4+s1)]. Verbatim from the reference.
var smplPulseCountByte = [8]uint8{80, 160, 160, 16, 32, 32, 0, 0}

// Mem8Static reads the one static rodata table the pulse path needs (0xe8990..0xe8998);
// every other address reads as 0.
func Mem8Static(addr uint32) byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_pulse.rs#L13-L19
	// TODO
	// agent suggestion: if addr in [0xe8990,0xe8998) return smplPulseCountByte[addr-0xe8990], else 0.
	// human input:
	panic("mlow: Mem8Static not yet implemented (scaffold)")
}

// SmplPulseResult is the decoded excitation for one internal (20 ms) frame.
type SmplPulseResult struct {
	Pulses []int32  // signed pulse magnitudes per sample position (len = p2)
	Subfr  [4]int32 // per-subframe pulse counts
}

// DecodeSmplPulses decodes the pulse blocks of one internal frame. p2 = frame
// samples (320), p3 = num subframes (4), p4 = regular flag (1), p6 = config (0/1),
// s1 = LSF stage-1 selector.
func DecodeSmplPulses(dec *RangeDecoder, mem *SmplMem, p2, p3, p4, p6, s1 int32) SmplPulseResult {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_pulse.rs#L29-L206
	// TODO
	// agent suggestion: port decode_smpl_pulses read-for-read — the total-count prior
	//   (NB triangular tri_t for p6==0), the recursive subframe split (smpl_split_3537
	//   via mem.cdf_at on g_cc-relative bases), per-position magnitudes, and the sign
	//   block ((bitfield>>14)&2)-1. All u32/i32 wrapping arithmetic → plain Go
	//   uint32/int32 operators; pos_list/mag_list grow as slices, pulses is len p2.
	// human input:
	panic("mlow: DecodeSmplPulses not yet implemented (scaffold)")
}
