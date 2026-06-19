# Changelog

All notable changes to meowcaller, tracked per module. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/). Each entry notes the module's
**validation state**: `scaffolded` (signatures + KAT test, bodies are TODO),
`implemented` (bodies written), or `KAT-verified` (its reference vector passes).

## [Unreleased]

### mlow/pulse — module #08 scaffold (reference `ed12f35`)
- Scaffolded the excitation pulse decode: `SmplPulseResult`, `Mem8Static` (the
  static 0xe8990 rodata helper, with the verbatim count-byte table), and
  `DecodeSmplPulses` — TODO stubs with `Source of truth:` pins.
- KAT `TestDecodeSmplPulses` (pulse_vectors.json) is wired to the real
  LSF(0)→pulses(0) chain (LSF decode already exists) and **fails until the body
  lands** — the honest scaffold state, no skip needed. This is the module #07 pitch
  KAT was waiting on: once pulses decode, the range coder reaches the pitch block.

### mlow/pitch — module #07 scaffold (reference `ed12f35`)
- Scaffolded the pitch/LTP envelope: decode side (`SmplPitchResult`,
  `DecodeSmplPitch`) and estimator side (`PitchEstState`+`ResetCond`, `PitchResult`,
  `PitchTables`, `LoadPitchTables`, `SmplPitch`) — all TODO stubs with
  `Source of truth:` pins. `NumSubframes`/`MaxLTPBufLen` added; `SmplFLen` reused for
  the f2 spectrum.
- **KAT blocked on ordering:** `pitch_vectors.json` verifies the chain
  LSF(0)→pulses(0)→pitch(0) — the range coder must be advanced past the pulse symbols
  before the pitch block, so the decode KAT (`TestDecodeSmplPitch`) **skips until
  module #08 pulse (`decode_smpl_pulses`) exists**. Recommend building #08 before
  KAT-verifying #07's decode.
- Estimator side is the known encoder soft-divergence (~0.03 vs C); acceptance
  tolerance is a human decision, not a byte-exact target.

### reference — all mlow runtime tables migrated to protobuf (reference `ed12f35`)
- Drove a reference refactor (`refactor(voip): store all mlow runtime tables as
  protobuf`, `ed12f35`, pushed) migrating the three remaining postcard table blobs —
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

### mlow/lsf_quant — module #06 KAT-verified (reference `ed12f35`)
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

### mlow/lsf — module #05 KAT-verified + protobuf LSF table asset (reference `c697c36`)
- **Reference change (pushed):** `refactor(voip): store the smpl LSF tables as
  protobuf` (`c697c36` on `feat/voip-media-stack`). Re-encoded the reference's
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
  `internal/tables` regenerated for the new messages; datasheet refreshed to `c697c36`.

### mlow/mem — protobuf table blob (reference `b90291b`)
- The reference now stores the cc_blob heap window as a zlib-compressed protobuf
  (`tables.proto`). meowcaller adopts the **shared schema**: embeds the reference's
  exact `smpl_cc_blob.bin` and decodes it through the generated `internal/tables`
  package (zlib inflate + `proto.Unmarshal`). Dropped the JSON embed and the local
  `genmem` generator. New (sole) third-party dep `google.golang.org/protobuf` — as
  whatsmeow uses; PLAN.md updated. KAT still green (pointers/accessors unchanged);
  mem SOT permalinks re-pinned to `b90291b`.

### reference sync — local checkout to `oxidezap/whatsapp-rust-private`@`674e851`
- Converted the local Rust reference into a real git checkout of
  `oxidezap/whatsapp-rust-private` (branch `feat/voip-media-stack`) and reset to the
  tip `674e851` (== our SOT pin; the public-repo permalinks are unchanged — commits
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
    commit `674e851…`.
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
