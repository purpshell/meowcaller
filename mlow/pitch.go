package mlow

// Pitch / LTP parameters. The decode side (DecodeSmplPitch) reads the LTP gains and
// pitch lags from the bitstream and is the KAT-verified path; the estimator side
// (SmplPitch) is the encoder analysis and is a known soft-divergence (see datasheet).

const (
	// NumSubframes is the estimator's 8 pitch sub-blocks per 20 ms internal frame.
	NumSubframes = 8
	// MaxLTPBufLen is the perceptually-weighted speech buffer length the estimator reads.
	MaxLTPBufLen = 659
)

// ---- Decode side ----

// SmplPitchResult is the decoded LTP/pitch parameters for one internal frame.
type SmplPitchResult struct {
	GainIdx     [4]int32
	FiltIdx     [4]int32
	Lag         int32
	Contour     int32
	SampleLagQ6 [8]int32 // per-segment reconstructed pitch lag in Q6 (1/64-sample)
	NumSeg      int32
	IntLagQ6    [4]int32 // per-subframe pitch lag in Q6
	BlockLags   [8]int32 // per-40-sample-block lags (8 per 20 ms frame)
	NumSubfr    int32
}

// DecodeSmplPitch decodes the LTP gains and pitch lags. p3 = num subframes,
// p6 = config, subfrCounts = per-subframe pulse counts (from the pulse decode).
func DecodeSmplPitch(dec *RangeDecoder, mem *SmplMem, st *SmplLsfState, p2, p3, p6 int32, subfrCounts [4]int32) SmplPitchResult {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_pitch.rs#L32-L198
	res := SmplPitchResult{FiltIdx: [4]int32{-1, -1, -1, -1}}
	gp := mem.GPitch

	// --- LTP gains loop --- (both selects key on p6; WB takes the first operand)
	weightTab := uint32(0xe85b0)
	gainCdfBase := gp + 0x302
	if p6 != 0 {
		weightTab = 0xe8460
		gainCdfBase = gp + 0xc0
	}
	filtCdf0 := gp + 0xdc4 // 35-sym, when prev_filt_idx == -1
	filtCdf1 := gp + 0xe4c // 35-sym, indexed by -prev_filt_idx*2

	var gainAccum int32
	take := int(p3)
	if take > 4 {
		take = 4
	}
	for sf := 0; sf < take; sf++ {
		cnt := subfrCounts[sf]
		row := gainCdfBase + uint32(st.PrevGainIdx*0x22) + 0x22
		gi := dec.DecodeCDF(mem.CDFAt(row, 17))
		res.GainIdx[sf] = gi
		st.PrevGainIdx = gi

		w0 := int32(mem.I16(weightTab + uint32(gi)*4))
		w2 := int32(mem.I16(weightTab + uint32(gi)*4 + 2))
		gainAccum += w0 + 2*w2

		if cnt > 0 {
			var fi int32
			if st.PrevFiltIdx == -1 {
				fi = dec.DecodeCDF(mem.CDFAt(filtCdf0, 35))
			} else {
				fi = dec.DecodeCDF(mem.CDFAt(filtCdf1-uint32(st.PrevFiltIdx)*2, 35))
			}
			res.FiltIdx[sf] = fi
			st.PrevFiltIdx = fi
		}
	}
	avgGain := gainAccum / p3 // drives the fractional-lag segment select

	// --- Lag block ---
	pcfg := mem.GClk + 0x5704
	numContours := int32(mem.U32(pcfg + 22240))
	lagCdf := mem.U32(pcfg + 22248)
	contourMap := mem.U32(pcfg + 22244)
	fracBase := mem.U32(pcfg + 22252)
	deltaCdf := mem.U32(pcfg + 22268)

	// primary lag:
	var lag int32
	if st.PrevLag < 0 {
		cnt := numContours + 1
		if cnt < 0 {
			cnt = 0
		}
		lag = dec.DecodeCDF(mem.CDFAt(lagCdf, int(cnt)))
	} else {
		di := dec.DecodeCDF(mem.CDFAt(deltaCdf+uint32(st.PrevLag)*20, 10))
		lo := int32(mem.U8(0xe7ef0 + uint32(di)*2))
		hi := int32(mem.U8(0xe7ef0 + uint32(di)*2 + 1))
		rN := (hi - lo) + 2
		if rN < 2 {
			res.Lag = -1
			return res // malformed delta interval
		}
		sym := dec.DecodeCDF(mem.CDFAt(lagCdf+uint32(lo)*2, int(rN)))
		lag = sym + lo
	}

	// contour-map search: find index where contour_map[i] == lag+1.
	target := lag + 1
	contour := int32(-1)
	for i := int32(0); i < 217; i++ {
		if int32(mem.U8(contourMap+uint32(i))) == target {
			contour = i
			break
		}
	}
	res.Lag = lag
	res.Contour = contour
	if contour < 0 || contour >= numContours {
		return res // out-of-range; stop consuming pitch bits
	}

	ctrBase := pcfg + uint32(contour)*0x44
	baseLag := mem.I32(ctrBase + 0x1d38) // contour base lag

	// (a) 64-symbol fine lag — read UNLESS prev_lag>=0 && -1 <= (base_lag-prev_lag) < 3.
	curLag2 := baseLag
	readFine := true
	if st.PrevLag >= 0 {
		delta := baseLag - st.PrevLag
		if delta >= -1 && delta < 3 {
			readFine = false
		}
	}
	var subfrW int32
	if readFine {
		sym := dec.Decode64FineSym()
		curLag2 = (baseLag << 6) + sym
		st.PrevFracLag = curLag2
		st.PrevLag = baseLag
		segLen0 := mem.I32(ctrBase + 0x1d58)
		for i := int32(0); i < segLen0; i++ {
			if subfrW < 4 {
				res.IntLagQ6[subfrW] = curLag2
			}
			if subfrW < 8 {
				res.BlockLags[subfrW] = curLag2
			}
			subfrW++
		}
		if subfrW < 4 {
			res.IntLagQ6[subfrW] = curLag2 // trailing write, subfr_w not incremented
		}
		if subfrW < 8 {
			res.BlockLags[subfrW] = curLag2
		}
	}

	// (b) fractional per-segment loop:
	cnt2 := mem.I32(ctrBase + 0x1d78)
	var segSel int32
	if avgGain >= 10007 {
		if avgGain < 14085 {
			segSel = 1
		} else {
			segSel = 2
		}
	}
	fracSegBase := fracBase + uint32(segSel)*0x280
	l3 := st.PrevFracLag
	l2 := curLag2
	startSeg := int32(0)
	if readFine {
		startSeg = 1
	}
	res.NumSeg = cnt2
	for seg := startSeg; seg < cnt2; seg++ {
		segLag := mem.I32(ctrBase + 0x1d38 + uint32(seg)*4)
		nl2 := ((l2 << 6) - l3) + ((segLag - l2) << 6)
		off := fracSegBase + uint32(nl2*2) + 0xfe
		sym := dec.DecodeCDF(mem.CDFAt(off, 65))
		l3 = sym + st.PrevFracLag + nl2
		if seg < 8 {
			res.SampleLagQ6[seg] = l3
		}
		segLen := mem.I32(ctrBase + 0x1d58 + uint32(seg)*4)
		for i := int32(0); i < segLen; i++ {
			if subfrW < 4 {
				res.IntLagQ6[subfrW] = l3
			}
			if subfrW < 8 {
				res.BlockLags[subfrW] = l3
			}
			subfrW++
		}
		l2 = segLag
		st.PrevFracLag = l3
		st.PrevLag = segLag
	}
	res.NumSubfr = subfrW
	return res
}

// ---- Estimator side ----

// PitchEstState is the per-stream estimator state (cross-frame lag-block predictor).
type PitchEstState struct {
	PrevLag       float32
	PrevPitchCorr float32
	PrevLagblk    int32
	PrevLagidx    int32
}

// ResetCond clears the cross-frame lag-block predictor (smpl_pitch_reset_cond).
func (s *PitchEstState) ResetCond() {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_pitch_enc.rs#L337-L341
	// TODO
	// agent suggestion: set PrevLagblk = -1 and PrevLagidx = -1 (the cond_coding=FALSE reset).
	// human input:
	panic("mlow: PitchEstState.ResetCond not yet implemented (scaffold)")
}

// PitchResult is the pitch estimator result for one internal frame.
type PitchResult struct {
	Pitchcorr    float32
	Lags         [NumSubframes]float32
	Laginds      [NumSubframes]int32
	AvgLag       float32
	HarmStrength float32
	BlocksegIdx  int
}

// pitchBlockSeg / pitchBlockTrack mirror the reference PitchTables sub-records.
type pitchBlockSeg struct {
	Nblocks int
	Blocks  []int
	Seglens []int
}

type pitchBlockTrack struct {
	Track       [NumSubframes]int
	Meanblock   float32
	Trackdeltas float32
}

// PitchTables holds the loaded constant tables (the smpl_pitch_tables dump).
type PitchTables struct {
	Blocksegs          []pitchBlockSeg
	Blocktracks        []pitchBlockTrack
	Blocksegs2idx      []int
	BlocksegIdxCmf     []uint32
	DeltaLagCmfs       [][]uint32
	BlocksegsIx        [][2]int
	FirstblockRange    [][2]int
	BlockTransitionCmf [][]uint32
}

// LoadPitchTables decodes the embedded pitch tables once and returns the shared set.
func LoadPitchTables() *PitchTables {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_pitch_enc.rs#L87-L92
	// TODO
	// agent suggestion: same protobuf-asset path as LoadLsfCb — embed the reference's
	//   smpl_pitch_tables.bin (zlib+protobuf tables.proto PitchTables) at the package
	//   root, inflate + proto.Unmarshal + narrow (usize<-u32), memoized with sync.Once.
	// human input:
	panic("mlow: LoadPitchTables not yet implemented (scaffold)")
}

// SmplPitch is the full pitch estimator. ltpBuf is the perceptually-weighted speech of
// length MaxLTPBufLen; f2 is the LPC power spectrum; codedAsActiveVoice gates the search.
func SmplPitch(st *PitchEstState, ltpBuf []float32, f2 *[SmplFLen]float32, codedAsActiveVoice bool) PitchResult {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/ed12f35/wacore/src/voip/mlow/smpl_pitch_enc.rs#L848-L1215
	// TODO
	// agent suggestion: faithful f32 port of smpl_pitch — autocorrelation upsample
	//   search, get_maxi/get_maxi_k survivors (strict >, lowest-index-wins), the
	//   harmonicity cache keyed on the rounded harmonic bin, and the block-track lag
	//   selection. NOTE: encoder soft-divergence (~0.03 vs C); not a byte-exact target.
	// human input:
	panic("mlow: SmplPitch not yet implemented (scaffold)")
}
