# Changelog

All notable changes to meowcaller, tracked per module. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/). Each entry notes the module's
**validation state**: `scaffolded` (signatures + KAT test, bodies are TODO),
`implemented` (bodies written), or `KAT-verified` (its reference vector passes).

## [Unreleased]

### stun — module #21 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New `stun` package: the RFC 5389 TLV encoder (`EncodeStunRequest` with HMAC-SHA1
  MESSAGE-INTEGRITY + CRC-32 FINGERPRINT), the WASM/APK allocate builders
  (`BuildWasmStunAllocateRequest`/`BuildAndroidStunAllocateRequest`), the WhatsApp
  ping, the response classifiers/parsers (`IsStunPacket`, `StunMessageType`,
  `ParseStunAttributes`, `ParseStunErrorCode`, pong matching, ...), and the minimal
  protobuf subscription/descriptor encoders (`CreateVoip/ApkSenderSubscriptions`,
  `CreateApkStreamDescriptors`). Implemented over stdlib `crypto/hmac`+`crypto/sha1`,
  `hash/crc32.ChecksumIEEE` (same reflected IEEE poly as the verbatim bitwise loop),
  and `encoding/binary` varints — no new deps. `Option` returns map to Go
  `(val, bool)` classifications (no panics; `hmac.New` is infallible). KAT
  (`kats.json` stun + stun_proto sections, synthetic tx/keys — no PII) passes
  byte-exact across all eight cases: CRC-32, attr/endpoint/native-sub, MI-only and
  MI+FINGERPRINT requests, WASM allocate + ping, attribute parse round-trip, the
  three protobuf blobs, APK allocate attrs, and pong matching. CodeRabbit: clean.
  Datasheet envelope refreshed (dropped the removed
  `build_native`/`build_minimal`/`rust_stun_allocate_request`; `Option`-shaped
  returns). **KAT-verified.**

### srtp/sframe — module #20 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- `srtp` package gains SFrame E2E media encryption: per-participant key derivation
  (`FormatSframeParticipantID`/`SframeInfoLabel`/`DeriveE2eSframeKeyForParticipant`),
  the `SframeSession` with `Encrypt`/`Decrypt`, and the AES-128-GCM (non-standard
  16-byte nonce, via `cipher.NewGCMWithNonceSize`) + varint-header machinery.
  Implemented over stdlib `crypto/aes`+`crypto/cipher`+`encoding/binary` (no new
  deps); the reference's `encode_varint`/`decode_varint` are the identical stdlib
  unsigned LEB128 (`binary.AppendUvarint`/`Uvarint`), and the shared mod.rs
  `format_participant_id` is ported. **Error-based** (no panics): the 32-byte callKey
  check yields `errBadCallKeyLen`, AES invariants bubble. `Decrypt` returns
  `([]byte, bool)` mapping the `SframeIn` enum — `ok=false` is the plain-Opus
  pass-through classification (GCM auth is the sole discriminator, fail-closed). KAT
  (`kats.json` sframe section, synthetic — no PII) passes byte-exact: participant
  id/label, peer key32, counter→IV, varint header + round-trip, encrypt_out, plus
  encrypt/decrypt round-trip, wrong-key fail-closed, and plain-Opus pass-through.
  CodeRabbit: clean (one doc-comment finding was a false positive — the comment is
  present). Datasheet envelope refreshed (dropped the removed `MbedtlsHKDFSHA256`;
  error returns). **KAT-verified.** `DeriveWarpAuthKey` is left a stub — no KAT here;
  it is implemented and validated under #24 warp.

### srtp/hbh — module #19 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- `srtp` package gains the hop-by-hop SRTP path: `SrtpKeyingMaterial` /
  `LibsrtpSessionKeys` types, the two-stage WA-SFU KDF derivation
  (`DeriveHbhSrtpKeyUplink`/`Downlink`, `KeyingFromHbhKey*`), libsrtp session-key
  expansion (`ExpandLibsrtpSessionKeys`), the RTP AES-ICM nonce (`BuildRtpICMNonce`),
  and the libsrtp AES-ICM cipher (`CryptRtpPayload`). Implemented over stdlib
  `crypto/aes` (block-by-block, no new deps). **Error-based** (no panics): the
  30-byte length check yields `errBadHbhKeyLen`, AES invariants bubble the
  `crypto/aes` error. The AES-ICM counter is libsrtp's 2-byte-carry variant
  (byte 15 → carry into 14), **not** a 128-bit CTR — ported exactly via per-block
  AES so the vectors match (a `cipher.NewCTR` would carry across all 16 bytes and
  diverge). KAT (`kats.json` hbh_srtp section, synthetic `hbhKey` — no PII) passes
  byte-exact: uplink key30, master key/salt split, session key/salt/auth expansion,
  ICM nonce, and AES-ICM cipher_out + round-trip. CodeRabbit: clean. **KAT-verified**.

### srtp/e2e — module #18 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New `srtp` package: `E2eSrtpKeys` + `DeriveE2eKeys`/`DeriveE2eKeysFromRaw`
  (HKDF-SHA256 master via the #17 `util.HKDFSHA256` → AES-CM PRF session keys),
  `BuildE2eRtpIV`, `CryptPayload` (AES-128-CTR), and the `RocTracker` (send,
  monotonic) / `RecvRocTracker` (recv, RFC 3711 guess-index) ROC trackers. Bodies
  implemented over stdlib `crypto/aes`+`crypto/cipher` (no new deps). **Error-based
  throughout** (no panics): the `<32`-byte guards return `errShortKey`, and the AES
  16-byte key/IV invariants bubble the `crypto/aes` error rather than aborting —
  matching the hkdf decision. KAT (`srtp/testdata/kats.json`, copied verbatim from
  the reference; synthetic callKey/LIDs, no PII) passes byte-exact: peer/self key
  derivation, RTP IV, AES-CTR cipher_out + round-trip, and both ROC trackers across
  wraps/reorder/late-packet. CodeRabbit: clean. **KAT-verified**.

### util/hkdf — module #17 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New `util` package: `HKDFSHA256(salt, ikm, info, length) ([]byte, error)` — the
  single HKDF-SHA256 extract-and-expand primitive every VoIP key schedule reduces
  to. Implemented over the **Go 1.25 stdlib `crypto/hkdf`** (zero new deps;
  `x/crypto` avoided per the protobuf-only mandate). **Deviates from the reference**:
  where the Rust `.expect()`/`debug_assert`s on the >8160-byte (255*32) bound, this
  forwards the `crypto/hkdf` error so a bad length bubbles up instead of aborting the
  caller — `crypto/hkdf.Key` already returns `([]byte, error)`, so the wrapper just
  passes it through. KAT (`util/testdata/rfc5869_hkdf_sha256.json`, RFC 5869 Appendix
  A Test Cases 1-3) passes byte-exact. Datasheet refreshed and pinned. CodeRabbit:
  clean. **KAT-verified**.

### mlow — seed-ROM table architecture (port of the upstream refactor)
- **pitch tables** now expand from a 2.3 KB seed ROM (`pitch_seed.bin`) instead of
  the ~33 KB `smpl_pitch_tables.json` blob. `pitch_seed.go` ports `smpl_pitch_seed.rs`:
  manual protobuf parse → range-decode the blocksegs bitstream (217 blocksegs) →
  `gen_blocktracks` (187) → integer `dcmf_to_cmf` for the idx/delta-lag/transition
  CDFs. `LoadPitchTables` now calls `buildPitchTablesFromSeed`. Validated **byte-
  identical** to the old JSON tables (all 8 `PitchTables` fields DeepEqual); full KAT
  suite still green. (cc + lsf seeds follow.)
- **cc tables builder** (`cc_tables.go`, port of `smpl_cc_tables.rs`): expands the
  2.1 KB `cc_seed.bin` into the nrgres/gains (A/E), LTP-gain (C), and pulse split/
  runlen (B) CDFs — integer `dcmf_to_cmf` + the SILK fixed-point split/runlen model
  (`lin2log`/`log2lin`/`sigm_Q15`/`stirling`) + carried gain-reconstruction rodata.
  Cross-checked **byte-identical to the old `cc_blob`** for every group it replaces
  (`TestCcTablesVsOldBlob`). Decode/encode rewiring to these accessors + the Group-D
  `SmplMem` rebuild + dropping `smpl_cc_blob.bin` follow.
- **cc seed wired + `smpl_cc_blob.bin` dropped**: decode (`gains.go`/`pulse.go`/
  `pitch.go` gain loop) and encode (`encoder.go`) now read the `CcTables` accessors
  instead of the heap window for Groups A/B/C/E. `SmplMem` is rebuilt from the pitch
  seed (`buildSmplMemFromSeed`) serving only the Group-D pitch lag/contour window
  (GCC/GNrg = 0). The ~102 KB `smpl_cc_blob.bin` is removed. All decode/encode KATs
  stay bit-exact (e2e decode corr 0.9867, byte-exact entropy, pitch contour, tone
  round-trip). Every new function carries its `// Source of truth:` pin.
- **LSF seed wired + 3 LSF blobs dropped** (`lsf_seed.go`, port of `smpl_lsf_seed.rs`):
  the 30 KB `lsf_seed.bin` expands into all three LSF runtime structs — `LsfCb`
  (quantizer codebook), `SmplTables` (stage-1/2 decode CDFs), and `SmplSynthTables`
  (decoder synthesis tables) — replacing `lsf_cb_dump.bin` (136 KB) +
  `smpl_tables.bin` (20 KB) + `smpl_synth_tables.bin` (65 KB). The float expansion
  is load-bearing: cInv symmetric fill, `matrix_mult_transp_16`, `laroia` →
  sqrt-then-reciprocal `rot_apply_wght`, integer `dcmf_to_cmf`, scalar `unpack8`,
  and the stage-2 flat-pointer walks (Qlvls/cmf/numBits, exactly `ST2_LEN`=9593).
  **FMA hazard:** Go contracts `a*b + c` into a fused multiply-add on amd64/arm64
  (one rounding), diverging 1 ULP from the reference's separate rounding; every
  load-bearing product is wrapped in an explicit `float32(…)` conversion to force
  the intermediate rounding. Validated against the reference's own golden `to_bits`
  constants (bit-exact) and field-by-field against the old blobs: every int +
  non-transcendental f32 **bit-identical**, sqrt-derived `we`/`wie`/`matrices`
  within 3 ULP, log2-derived `bits`/`num_bits` within 1 ULP (matches the upstream
  note). The seed-build intentionally trims two synth tables vs the pre-seed blob
  (`valtables` width = `numQlvls`, `centroids` omits the never-read grid==16 row);
  values bit-exact on every overlapping entry. `LoadLsfCb`/`LoadSmplTables`/
  `LoadSmplSynthTables` now delegate to the seed builder; the protobuf blob loaders
  and helpers are removed. Full decode/encode KAT suite stays bit-exact.

### mlow — upstream sync (reference `ed12f35..41095d4`): robustness guards
- Ported the two codec-behavioral fixes from the upstream review commit `543302e`
  (everything else in the 9 new reference commits is non-behavioral — see below):
  - `pulse.go`: zero the whole subframe split when either half's `smplSplit3537`
    returns the corrupt `-1` sentinel (C `smpl_pulse_coding`), instead of copying
    `-1` into `Subfr`.
  - `vad.go`: reject a short capture buffer up front in `ProcessPacket` so the
    fixed-stride frame loop can't index out of range.
  - tightened `TestEncodeRoundTripsATone` to `> 0.7` (matches upstream; we get 0.89).
- The other 8 upstream commits are non-behavioral for our port: a table-storage
  refactor (seed ROM vs blob — same table values), per-frame perf (scratch reuse,
  in-place CDF reads, FFT twiddle precompute — "codec output byte-identical"), typed
  errors / dead-code, and test-vector regeneration + comment cleanup.

### tooling — `mlowtest` CLI + file test script
- `cmd/mlowtest`: `encode` (raw s16le mono 16 kHz → MLow `.bin`) and `decode`
  (`.bin` → WAV, or `-raw` s16le). The `.bin` container is `"MLW1"` + per-frame
  uint16 length-prefixed MLow frames.
- `scripts/mlow_file_test.sh`: `enc <audio> <out.bin>`, `dec <in.bin> <out.wav>`,
  `roundtrip <audio> <out.wav>` — ffmpeg decodes any input (mp3/m4a/wav/...) to
  16 kHz mono PCM, then this repo's `cmd/mlowtest` encodes/decodes. Self-contained
  (Go only). The Rust build (whatsapp-rust-voip) ships an identical script over its
  own binary; the two interoperate **by file** — a `.bin` from one decodes with the
  other — without either codebase referencing the other. Verified: same `.bin`
  decoded by Go vs Rust → corr 1.000000.

### mlow/encoder — module #16 classifier + entropy coder KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Ported the voiced/unvoiced classifier 1:1 from `smpl_signal_mode.rs`:
  `SmplGetSignalMode` (five voicing strengths — pitch correlation, VAD, spectral
  tilt, harmonicity, short lag — plus per-stream `VuvMode` hysteresis →
  `voicing_strength`), `BuildF2w`, `HarmStrengthAt`, `spectralHarmonicity`. KAT
  `TestSignalModeGroundTruth` threads one `VuvMode` over the C dump
  (`sigmode_ground_truth.json`): voicing_strength **max_err 1.2e-07** (< 1e-4),
  voiced decision matches C every frame; `HarmStrengthAt` within 0.034.
- Implemented the full entropy encoder (`EncodeSmplFrame`) — the exact inverse of
  the byte-exact decoder — from `encode.rs`: `encodeSmplLsf`, `encodeSmplPulses`
  (+`encodeSplit3537`), `encodeSmplGains`, and the voiced `encodeSmplPitch` with
  the lag-contour wire coder (`encodeLagsWire` / `smplLagsPredictorAfter`) over
  the embedded pitch tables (`LoadPitchTables`, `smpl_pitch_tables.json`). The
  decode path now records the raw entropy symbols it reads (pulse `MagRuns`/
  `SignSyms`, gain `GainMain`/`GainDelta`) so the encoder replays them exactly.
- KAT `TestEntropyEncoderByteExact`: decode→re-encode is **byte-exact on 61
  fully-unvoiced active frames** from the real capture (LSF + pulses + gains),
  modulo trailing range-coder zero padding the peer encoder trims (the decoder
  never reads it; verified by re-decode). KAT `TestPitchBlockRoundTripsContour`
  (the reference's own test): the voiced lag encode round-trips through
  `DecodeSmplPitch` — reconstructed `BlockLags` == encoded `laginds`.
- Remaining: `Encode` (pcm→wire) still returns `ErrEncodeUnimplemented` — it needs
  the analysis DSP front-end (`smpl_analyze_frame_st`: LPC analysis, pitch
  estimator, perceptual weighting, bitrate control, CELP/LSF quantization), a
  large soft-divergent effort (no byte-exact vector; only a tone-correlation
  round-trip). The entropy coder it would feed is done and verified.
- This is the last codec (mlow) module; modules #17+ are the crypto/transport/
  signaling stack.

#### encoder front-end build (toward Encode pcm→wire)
- **smpl_perc ported + KAT-verified** (perc.go): the perceptual-weighting model
  (`PercModelState`/`SmplPercModel`/`SmplPercAc2a` — mixed-radix FFT power spectrum
  → bidirectional mel masking → perceptual LPC response) and the bitrate controller
  (`BitrateController` — per-subframe pulse budget + importance). KATs: FFT
  round-trip, perc-model smoke (zero→0, DC→R[0]>0, A[0]=1), and the active-unvoiced
  pulse budget = 23/subframe matching the C dump. (Reuses the existing fft.go.)
- **smpl_pitch_enc estimator ported + KAT-verified** (pitch_enc.go): the full
  multi-stage `SmplPitch` — HP-filter + 2x downsample, stage-1 autocorrelation,
  coarse upsample, block-track survivor search (`get_maxi_k`), full-res per-block
  refinement, fractional upsample, and the rate/prev-lag/spectral-harmonicity
  survivor biases. `LoadPitchTables` now also parses `blocktracks`; `ResetCond`
  implemented. KAT `TestPitchEstimatorGroundTruth` (pitchio_ground_truth.json, 48
  active frames): **exact** `laginds`/`blockseg_idx`, pitchcorr max_err 7e-07,
  avg_lag exact, harm within 1.8e-07.
- **smpl_celp CelpEncoder ported** (celp_enc.go, datasheet datasheets/mlow-celp.md):
  the closed-loop excitation encoder — perceptual impulse response, ACB/LTP gain
  search (`calcAcbGain`), greedy + delayed-decision beam FCB pulse search
  (`smplFcbSearch`/`smplFcbSearchDeldec` with pitch-sharpening cross-terms +
  signature dedup), gain quant (`calcGainsV`/`celpQuantGainUv`), and the per-subframe
  orchestrator `EncodeSubframe` returning pulses/indices/reconstructed excitation.
  Smoke KATs (`encode_{unvoiced,voiced,voiced_fractional_greedy}_runs`) pass: all
  search paths run and produce correctly-shaped output. Reuses cbAcbgains/acbgN/M.
  Full bit-correctness arrives with the end-to-end tone round-trip after wiring.
- **analysis wiring → `Encode(pcm)` complete** (analysis.go): ported
  `smpl_analyze_frame_st` 1:1 — per internal frame it runs the VAD, encoder HP
  (ARMA2), LPC analysis, the bit-exact LSF quantizer (+ conditional coding), the
  perceptual model, the multi-stage pitch estimator + voicing classifier, the CELP
  excitation encode, and candidate selection (voiced LTP / unvoiced nrgres / silent),
  committed to a shadow synth (`SynthInternalFrame`) for warm history.
  `MlowEncoder.Encode` now sanitizes → analyzes → `EncodeSmplFrame`. **KAT
  `TestEncodeRoundTripsATone`: encode a 550 Hz tone → decode through the byte-exact
  decoder → reconstruction tracks the input at correlation 0.89 (> 0.5).** This is
  the full codec round-trip — the mlow encoder is complete. The shadow-synth chain
  (`SynthInternalFrame`, `SmplLTPSubframePred`, `SmplNLSF2A`, `SmplGainLin`,
  `SmplLTPFracGain`, `QuantNrgRes4`) is now exercised e2e — the last `NOT VALIDATED`
  markers are cleared. CodeRabbit clean.

### mlow/decoder — module #15 KAT-verified (audible milestone) (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the top-level `MlowDecoder` 1:1 from `decoder.rs`: RED strip → TOC
  routing (std-opus / SID / inactive → silence) → active-frame decode (3 chained
  internal frames: LSF → pulses → pitch/gains → reconstruct → CELP `SynthFrame`) →
  per-packet harmonic postfilter → clamped 60 ms PCM, with cross-frame state
  (`SmplDecoderState`) persisting across calls. Added `SmplDecoderState`
  (wiring CelpDecState + HarmPostfilterState, now that both exist).
- KAT `TestE2EDecodeMatchesUseSmpl` decodes the real `inbound_capture_frames.json`
  stream and matches the libopus useSmpl reference PCM
  (`ref_usesmpl_expected.raw`): exact length + **lag-0 Pearson correlation 0.9867**
  (> 0.95; not bit-exact due to noise PRNG + reference -ffast-math). This is the
  first audible milestone — the full decode pipeline produces correct PCM.
- This also validates synth's full CELP output end-to-end; synth #13 → verified
  (CELP path), `SynthInternalFrame` (WASM-domain alt, unused on the decode path)
  remains the only `// NOT VALIDATED` body. CodeRabbit: 2 findings (per-function
  Source-of-truth pins; correlation div-by-zero guard) → fixed, re-review clean.


### mlow/red — module #14 KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented `DepackSplitRed` 1:1 from `red.rs`: the SplitRed header run (redundant
  blocks `0x80|code`,`size`), the main marker, and frame extraction as zero-copy
  subslices, with the four sentinel errors. KAT `TestDepackSplitRed` covers the
  reference's inline cases (one redundant+main, header-only+main, empty, bare-frame
  rejection). CodeRabbit: 0 findings.


### mlow/vad — module #12 KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the SILK VAD fixed-point port 1:1 from `smpl_vad.rs`: the SILK
  primitives (smulwb/smlawb/smulww/smulbb/smlabb, sat16, clz/ror/lin2log/sqrt_approx/
  sigm_q15, rshift_round), the 2-band allpass filterbank (incl. the in-place stages),
  HP filter, GetNoiseLevels, GetSA_Q8, and the per-packet DTX hangover.
- KAT `TestVadGroundTruth` matches the C enc_dump (smpl_VAD_GetSA_Q8_c) on
  `mic_clip.raw`: per-frame `spact` (<1e-4) and packet `coded_as_active_voice` exact.
  CodeRabbit: 0 findings. (Reused `silkInt16Max` from lpc.go.)
- Also folds in two earlier doc changes (no separate commit, all local): the AGENTS.md
  rule that MODULES.md Status must track KAT reality (CR flags / agent fixes), and the
  stale-status corrections (rangecoder/mem/toc/lpc → verified).


### mlow/celpdec — CELP synthesis: excitation verified, full output e2e (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the decoder-side C-float CELP synthesis (`CelpDecState.SynthFrame` +
  `lpcInterpol`, `acbDequant`/`acbSynthesize`, `pitchSharp`, `synLTPBasis`,
  `celpDecode`, `filtAR16`, `fcbGains`) and `CelpDecParams`, ported 1:1 from
  `smpl_celpdec.rs`. Transcribed the small ACB-gain codebooks (`cbAcbgains{HR,LR}Q14`)
  as a prerequisite. Reuses `SmplNLSF2A`, the noise generator, and the HP postfilter.
- KAT `TestExcPre` drives the full decode chain (LSF→pulses→pitch/gains→reconstruct→
  SynthFrame) and validates the deterministic pre-noise excitation against the C
  `exc_pre` dump per subframe: unvoiced 752/0, voiced 292/0, worst 1.86e-9. The noise
  and HP-postfilter stages SynthFrame composes are each KAT-verified in their modules;
  the full combined PCM is validated end-to-end by the decoder. CodeRabbit: 0 findings.
- Moved the CELP types out of synth.go into `celpdec.go`. `inbound_capture_frames.json`
  + `exc_pre_lags.json` copied into testdata.


### mlow/noise — gennoise core KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the CELP noise-generator core 1:1 from `smpl_gennoise.rs`:
  `SmplGetNormalizedBitrate`, `SmplDecodeResnrg`, `NewNoiseGenerator`, and
  `SmplCelpGenNoise` with all its helpers (`smplRand` LCG, `smplGenRandPulses`,
  `smplGetEnv`/`smplGetEnv0`, the MA1/AR1/ARMA1/MA2 filters, `smplSpecFact2`
  spectral factorization, the noise DCT + matrix mults, `addNoiseUV`) — voiced and
  unvoiced paths.
- KAT `TestGenNoise` passes **bit-exact** against the instrumented-C
  `gennoise_vectors.json` (copied into testdata) — noise[80], the output generator
  state (env_last, out_state_uv/v), and the PRNG seed transition, across all three
  paths (voiced / unvoiced-no-pulses / unvoiced-with-pulses). CodeRabbit: 0 findings.
- Reused `smplResNrgBias` (synth.go); named the noise matrix mults distinctly to
  avoid the `matrixMultTransp16` collision with lsf_quant. The datasheet's bundled
  perc front-end + bitrate controller remain for the encoder module.

### mlow/postfilter — HP comb + harmonic KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the post-LPC HP (pitch-harmonic) comb 1:1 from `smpl_harmcomb.rs`
  (`SmplHpPostfilter` + `SmplPfFir3`/`SmplFiltArma2`/`SmplGetHpCoefs` + the unrolled
  `pfFiltAR1`/`pfFiltAR2`/`pfFiltMA1`, `smplCalcHPCoefs`/`newCoefs`/`rampDn`) and the
  per-packet harmonic postfilter from `smpl_harm_postfilter.rs` (`SmplHarmPostfilter`
  + `harmPostfilterCore`, the LP-filter bank, `harmFiltMA16Sym`).
- KATs `TestHpPostfilter` (hp_postfilter_vectors.raw) and `TestHarmPostfilter`
  (harm_postfilter_vectors.raw, both copied into testdata) pass within the i16 output
  LSB (1/32768) — the reference is -ffast-math so it's not bit-exact through the
  near-unit-circle pitch comb; the harmonic transition zero-input response is bounded
  by 6e-4 on near-silent voiced→silence boundaries, as in the reference. CodeRabbit: 0.
- `SmplCombPostfilter` (the Region-1 excitation comb) stays a stub: it's gated off
  (`SMPL_TAIL_REGION1` == false), never invoked on the decode path, and has no
  standalone vector. Named the harmonic helpers distinctly (`harmDotProd`/`harmNrg`)
  to avoid clashing with lsf_quant's `dotProd` / noise's `smplNrg`.

### mlow/synth — module #10 scaffold + NLSF reconstruction verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Scaffolded the full low-band synthesis envelope (TODO stubs with `Source of truth:`
  pins spanning smpl_synth.rs / smpl_celpdec.rs / smpl_nrgres.rs): `SmplNLSF2A`,
  `SmplGainLin`, `SmplLTPFracGain`, `SmplExcGainState`, `SmplPitchSynth`,
  `SmplFrameSynth`+`New…`, `SmplLTPSubframePred`, `SynthInternalFrame`, the C-float
  CELP path (`CelpDecParams`, `CelpDecState`+`New…`+`SynthFrame`), and `QuantNrgRes4`.
- **Implemented + verified `LoadSmplSynthTables` and `SmplReconstructNLSF`** (with the
  helpers `smplNLSFLaroiaWeights`/`smplNLSFDecorr`/`smplStabilizeNLSF`). The loader
  decodes the embedded `mlow/smpl_synth_tables.bin` (zlib+protobuf, `internal/tables`
  regenerated with the `SmplSynthTables` message + `f4ToGo`/`f5ToGo` helpers). KAT
  `TestSmplReconstructNLSF` quantizes each `lsf_quant_io.json` record and requires the
  reconstruction to match the captured `qlsf` (≤1e-3; rec 3 excluded as in the
  reference). CodeRabbit: 0 findings.
- The frame-synthesis bodies (`SynthInternalFrame`, `SynthFrame`, etc.) remain stubs:
  no standalone vector — validated end-to-end (`e2e_vectors.json`) by module #15
  decoder; `TestSynth` skips with that reason.
- `SmplDecoderState` intentionally **omitted** — its `Harm` field is a
  `HarmPostfilterState` from module #11 (not built); lands at #11 / #15 integration.

### mlow/synth — module #10 synth bodies implemented (NOT VALIDATED) (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Ported the remaining self-contained synth bodies 1:1: `SmplNLSF2A` (+`smplNLSFPoly`),
  `SmplGainLin`, `SmplLTPFracGain`, `SmplLTPSubframePred` (+`smplFracLTP`/
  `smplExcGainApply`/`smplFir8`/`smplFloorF32`/`smplLPCSynthesis`), `SynthInternalFrame`,
  `NewSmplFrameSynth`, and `QuantNrgRes4` (+ the `nrgresShapeCB4Q10` codebook). Each
  carries a `// NOT VALIDATED:` marker — no passing KAT exercises them yet (they're
  e2e-gated via #15); landed ahead of their vector per explicit human direction.
- `SynthInternalFrame` omits the reference's Region-1 comb / HP postfilter (gated off
  by `SMPL_TAIL_REGION1`/`SMPL_HP_POSTFILTER`), which need module #11.
- Enabled the previously-skipped `TestDecoderReconstructsCQlsf` (its prereqs #06 +
  #10-reconstruct are now built) — passes; removed the duplicate `TestSmplReconstructNLSF`.
- Still stubbed: `CelpDecState.SynthFrame` / `NewCelpDecState` — the C-float CELP path
  always runs noise (#13) + postfilter (#11), so it needs those scaffolded first
  (directive #5). CodeRabbit: 0 findings.

### mlow/gains — module #09 KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented `DecodeSmplGains` 1:1 from `decode_smpl_gains`: main+delta gain CDFs,
  the gain reconstruction (deliberate adjacent-rodata read via the heap window), and
  the per-subframe bucketed nrgres CDF with the gain-derived sign-mask address shift.
  Signed arithmetic shifts (`>>14`, `>>31`) kept on `int32`; address math `wrapping`
  on `uint32`.
- KAT `TestDecodeSmplGains` passes: LSF(0)→pulses(0)→gains reproduces `gain_q[]` and
  `nrg_res[]` for every `gains_vectors.json` frame. CodeRabbit: 0 findings.
- No unbuilt requisites — the chain (LSF #05, pulses #08) was already done.

### mlow/pulse — module #08 KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the excitation pulse decode 1:1 from `decode_smpl_pulses`: the
  triangular pulse-count prior (NB/config-0 path), the recursive subframe split
  (`smplSplit3537` via `mem.CDFAt` on g_cc-relative bases), the run-length magnitude
  block, and the batched uniform sign reads — all `wrapping` arithmetic as plain Go
  `uint32`/`int32`. `Mem8Static` reads the one static rodata table.
- KAT `TestDecodeSmplPulses` passes: LSF(0)→pulses(0) reproduces the per-subframe
  counts and full signed pulse vector for every `pulse_vectors.json` frame.
- CodeRabbit review: one minor finding (divide-by-`p3` zero-guard) resolved with an
  `// ASSUMPTION:` note (the reference divides unguarded; we stay bit-faithful).
- This unblocks module #07 pitch's decode KAT (the range coder now reaches the pitch
  block).

### mlow/pitch — module #07 decode KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the decode side `DecodeSmplPitch` 1:1 from `decode_smpl_pitch`: the LTP
  gains loop (gain/filter CDFs from `mem.GPitch`+offsets, keyed on p6 and the
  `prev_*` predictors), the primary lag (absolute vs delta off `st.PrevLag`), the
  217-entry contour-map search, the optional 64-symbol fine lag, and the fractional
  per-segment Q6 reconstruction. All `wrapping` address/count arithmetic as plain Go
  `uint32`/`int32`.
- KAT `TestDecodeSmplPitch` passes (now unblocked by #08): LSF(0)→pulses(0)→pitch(0)
  reproduces lag/contour/gain_idx/filt_idx/int_lag_q6 for every `pitch_vectors.json`
  frame. CodeRabbit review: 0 findings.
- **Estimator side stays scaffolded** (`SmplPitch`/`LoadPitchTables`/`ResetCond` are
  still stubs): it's the known encoder soft-divergence (~0.03 vs C) and needs the
  pitch-tables protobuf asset, the `pitchio_ground_truth.json` vector, and a human
  tolerance decision before it can be done.

### reference — all mlow runtime tables migrated to protobuf (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Drove a reference refactor (`refactor(voip): store all mlow runtime tables as
  protobuf`, `ed12f359a086b28e807ba236f0977af1000859fe`, pushed) migrating the three remaining postcard table blobs —
  `smpl_synth_tables`, `lsf_cb_dump`, `smpl_pitch_tables` — to zlib+protobuf
  (`tables.proto`), joining `smpl_tables` and the cc_blob. **Every** mlow runtime
  constant table is now protobuf, so each byte-identical blob loads in the Go port
  (postcard is Rust-only). Added reusable nested wrapper messages (`F1..F5` float,
  `U1`/`I1..I4` int) plus per-table messages (`SmplSynthTables`, `PitchTables`,
  `LsfCb`); the runtime structs keep their native shape, converted at the load/gen
  boundary. Dropped the now-unused `postcard` dependency and the dead
  `load_blob`/`make_blob` helpers. Blobs regenerated; decode is bit-identical (full
  reference suite green except the pre-existing golden encoder-path divergence).
- Datasheets `mlow-synth`, `mlow-pitch`, `mlow-lsf_quant` updated: loaders shown as
  `load_blob_prost` + prost-mirror note, and the Go-asset `TODO(human)`s resolved to
  the settled convention (production blob at the package root under the reference's
  filename; KAT JSON stays in `testdata/`). No meowcaller code change yet — the
  per-module proto messages/blobs land when each Go module is built.

### mlow/lsf_quant — module #06 KAT-verified (reference `ed12f359a086b28e807ba236f0977af1000859fe`)
- Implemented the encoder-side LSF vector quantizer: the VQ_temp Mahalanobis
  shortlist, the RD beam (`0.5*order*log2(werr)*RDw_adj + bits`) with per-coeff
  stage-2 clamps and the one-coeff-flip refinement, and the conditional path
  (`LsfQuantCond` — reg-blended prev NLSF → cond centroid via `rot_apply_wght`).
  Bit-exact vs the C reference over all `lsf_quant_io.json` vectors
  (`TestLsfQuant`): `qi[]` exact, `qlsf` within 1e-4. A faithful f32 port — all
  arithmetic stays `float32`, transcendentals computed in f64 and narrowed (matches
  the reference closely enough that no `qi` tie flips).
- `LoadLsfCb` decodes the codebook from `mlow/lsf_cb_dump.bin` (the reference's
  byte-identical zlib+protobuf blob, embedded at the package root). `internal/tables`
  regenerated with the shared `F1..F5`/`U1`/`I1..I4` wrappers and the `LsfCb` message.
- Note: `lsf_quant` is encoder-side; the float-comparison `qi[]` decisions inherit
  the reference's exactness here, distinct from the known encoder pitch-estimator
  soft-divergence (module #07/#16).

### mlow/lsf — module #05 KAT-verified + protobuf LSF table asset (reference `c697c36ffa7875c304ceea9154be30b66cada914`)
- **Reference change (pushed):** `refactor(voip): store the smpl LSF tables as
  protobuf` (`c697c36ffa7875c304ceea9154be30b66cada914` on `feat/voip-media-stack`). Re-encoded the reference's
  `smpl_tables.bin` from zlib+postcard to zlib+protobuf (`tables.proto`
  `SmplLsfTables`), mirroring the cc_blob, so the byte-identical blob is decodable
  in Go (postcard is Rust-only). Verified bit-identical decode: the protobuf
  round-trip equals the old postcard blob; the only suite failure
  (`golden_roundtrip_no_regression`) pre-exists on clean HEAD (known encoder-path
  divergence) and is unaffected.
- **meowcaller:** module #05 `lsf` **implemented + KAT-verified**. `DecodeSmplLsf`
  (selector → grid → 16 stage-2 → extra, with the no-match predictor reset) and the
  encoder-mirror `SmplAdvanceLsfState` are bit-exact against `testdata/lsf_vectors.json`
  (`TestDecodeSmplLsf`). `SmplLsfState` carries the reference's two extra encoder-only
  lag-predictor fields (`PrevLagblk`/`PrevLagidx`), which the advance-mirror resets but
  the decoder does not.
- `LoadSmplTables` inflates + `proto.Unmarshal`s the production blob `mlow/smpl_tables.bin`
  — the reference's own filename, at the **package root** (not `testdata/`, a fixture
  dir), mirroring `smpl_cc_blob.bin`. Convention: KAT inputs live in `testdata/`;
  production assets keep the reference name at the package root. `TestLoadSmplTables`
  cross-checks the decoded blob against the captured `testdata/smpl_tables.json`.
  `internal/tables` regenerated for the new messages; datasheet refreshed to `c697c36ffa7875c304ceea9154be30b66cada914`.

### mlow/mem — protobuf table blob (reference `b90291b1ae979d504adf71d9555b3daf5c7325b1`)
- The reference now stores the cc_blob heap window as a zlib-compressed protobuf
  (`tables.proto`). meowcaller adopts the **shared schema**: embeds the reference's
  exact `smpl_cc_blob.bin` and decodes it through the generated `internal/tables`
  package (zlib inflate + `proto.Unmarshal`). Dropped the JSON embed and the local
  `genmem` generator. New (sole) third-party dep `google.golang.org/protobuf` — as
  whatsmeow uses; PLAN.md updated. KAT still green (pointers/accessors unchanged);
  mem SOT permalinks re-pinned to `b90291b1ae979d504adf71d9555b3daf5c7325b1`.

### reference sync — local checkout to `oxidezap/whatsapp-rust-private`@`674e85164b35ca19115dfebcf605708d15951ee7`
- Converted the local Rust reference into a real git checkout of
  `oxidezap/whatsapp-rust-private` (branch `feat/voip-media-stack`) and reset to the
  tip `674e85164b35ca19115dfebcf605708d15951ee7` (== our SOT pin; the public-repo permalinks are unchanged — commits
  cherry-pick onto `oxidezap/whatsapp-rust`).
- Verified every datasheet's embedded verbatim against the current tree. Result:
  **all current except `mlow-encoder`**. The supposedly-stale `pitch`/`synth`/
  `noise`/`decoder` and `call`/`relay`/`session` are fully current — their sources
  just span multiple files (and the orchestration ones live in the tokio `src/`
  crate). All built-module datasheets (toc, rangecoder, mem, lpc) are current.
- `mlow-encoder` (#16, unbuilt): ~208 verbatim lines diverged because the encoder
  source was **reorganized** — old combined `analysis.rs` split into `analysis.rs`
  + `smpl_pitch_enc.rs`, and the pitch estimator changed (the known ~0.03
  divergence). Faithful refresh = restructuring to the new file layout, deferred to
  when module #16 is built (local reference is now current, so it ingests correctly
  then).

### reference sync (patch `d441e5fa…current`)
- Applied the upstream `wacore/src/voip/mlow/*.rs` source changes to the local
  reference. Net effect on **built** modules: none functional.
  - `smpl_mem.rs`: loader refactored (runtime tables now zlib+postcard `.bin` via
    new `smpl_tables_blob::load_blob`; the inline JSON parse became a `#[cfg(test)]`
    generator helper). The `SmplMem` memory model and **all accessors are
    byte-identical** → `mlow/mem` Go and tests unchanged. The heap-window data is
    verified identical (same regions + `g_cc/g_nrg/g_pitch/clk`), so our embedded
    `smpl_cc_blob.json` stays valid. Datasheet `mlow-mem.md` updated to the current
    source and the packaging change; SOT permalinks stay pinned to the ported
    commit `674e85164b35ca19115dfebcf605708d15951ee7…`.
  - `toc.rs`, `rangecoder.rs`, `smpl_lpc.rs`, `silk_lsf_cos_tab.rs`, `smpl_perc.rs`
    are **not** in the patch → `toc`, `rangecoder`, `mem` cosine table, and the
    `lpc` scaffold/FFT-dependency are unaffected.
- Not applied: the binary `.bin` blobs (patch lacks full index lines) and the
  `smpl_cc_blob.json` / `smpl_tables.json` deletions — we keep the JSON as our data
  source (the `.bin` are an upstream re-encoding of identical data).
- Flag: the patch also changed the reference for not-yet-built modules
  (`smpl_decode`, `smpl_lsf_quant`, `smpl_synth`, `smpl_pitch_enc`, `analysis`,
  `encode`). Their pre-written datasheets now carry stale verbatim source; they will
  be re-ingested from the (now-current) local reference when each module is built.

### mlow/lpc
- implemented: smplLPCInterpol/Idx (per-subframe NLSF interpolation) + lpcIsStable
  / lpcStabilize. nlsf2a is a caller-supplied closure (the encoder #16 passes
  synth's smpl_nlsf2a), so no synth dependency here. No direct vector — verified by
  1:1 port (build/vet + the module's other KATs stay green); exercised transitively
  by the encoder. **Module #04 is now fully implemented** (only the interpolation
  pair lacks a direct KAT).
- implemented: the analysis front-end — smplWindowLPC20 (sin/cos window) and
  smplLPCAnalyzeWithF2 (zero-pad → real FFT → power spectrum → brute_dct autocorr
  → Schur ac2rc → rc2a → bandwidth expand). The shared portable mixed-radix FFT
  (rfftForwardOrdered + cfft/fftRec/smallestFactor) landed in mlow/fft.go, ported
  from smpl_perc.rs. TestFrontEndAMatchesC passes: windowing exact (|dwin|≈5e-10),
  A within 5e-3 on above-floor frames (FFT-internal rounding only, as documented).
  Only smplLPCInterpol/Idx remain stubbed (need the decoder's nlsf2a closure).
- implemented: smplA2NLSF16 — the fixed-point silk forward A→NLSF — plus its
  helpers (silk_rshift_round / smlaww / div32 / bwexpander_32 / a2nlsf_trans_poly /
  eval_poly / init / a2nlsf). KAT-verified **bit-exact** against lsf_quant_io.json
  (TestA2NLSFMatchesC, worst abs err 0.0). smplLPCAnalyzeWithF2 (FFT-blocked) and
  the interpolation funcs remain scaffolded.
- scaffolded: constants + the five public envelope functions (smplWindowLPC20,
  smplLPCAnalyzeWithF2, smplLPCInterpol/Idx, smplA2NLSF16) with three-line stubs.
  Tests wired to lsf_quant_io.json (forward A→NLSF, bit-exact) and fe_dump.json
  (windowing exact + FFT-autocorr tolerance); both fail until implemented. The
  cross-module qlsf round-trip test is skipped pending #06/#10.
  Open: (a) smplLPCAnalyzeWithF2 needs a 512-pt real FFT (no module/datasheet yet);
  (b) the registry's lsf_vectors.json pins the LSF wire (#05/#06), not this
  front-end — lpc validates against lsf_quant_io.json + fe_dump.json.

### mlow/mem
- implemented: SmplMem accessors (LE U8/U16/U32, signed I16/I32, out-of-region
  zero fallback, CDFAt 2-byte stride). Heap ROM loaded via go:embed from
  mlow/smpl_cc_blob.json (moved out of testdata per review) behind a sync.Once
  singleton. Load/pointer + accessor-semantics + cosine-transcription tests pass;
  byte-exact CDF KAT skipped — mem has no direct vector in the reference, so
  smpl_tables.json is verified transitively by the decode modules.
- scaffolded: SmplMem type + accessor signatures; cosine table
  (silkLSFCosTabFIXQ12, 129 entries) transcribed verbatim.

### mlow/rangecoder
- KAT-verified: decoder replays the 2000-op and 1500-op CDF scripts to the listed
  values; encoder re-encodes both byte-identically to rc_vectors.json (4/4 tests).
- implemented: full RangeDecoder + RangeEncoder bodies (ec_dec/ec_enc) as a
  uint32-modular port; sticky Err/err fields, no error returns.
- scaffolded: RangeDecoder + RangeEncoder types and all method signatures; four
  KAT tests wired to testdata/rc_vectors.json (decode + re-encode).

### mlow/toc
- KAT-verified: ParseSmplTOC matches toc_vectors.json (256/256 byte values).
- implemented: ParseSmplTOC body + standardOpusFrameMs helper.
- scaffolded: SmplTOC type + ParseSmplTOC signature + exhaustive KAT test wired
  to testdata/toc_vectors.json (256 byte values).

### Planning
- Datasheets for all 28 modules under `datasheets/`: each carries the reference
  source verbatim, the Go envelope (signatures only), and implementation
  suggestions. Verbatim source verified complete (line-count match vs source);
  7 initially-truncated sheets re-pasted in full.
- Project framework: `PLAN.md` (engineering plan), `AGENTS.md` (human-audited
  module-by-module build protocol), `MODULES.md` (module registry + build order),
  per-module datasheets under `datasheets/`.

<!--
Entry template (newest first), grouped by module:

### mlow/toc
- KAT-verified: smpl TOC parser matches toc_vectors.json (256/256 byte values).
- implemented: ParseSmplTOC body.
- scaffolded: SmplTOC type + ParseSmplTOC signature + KAT test.
-->
