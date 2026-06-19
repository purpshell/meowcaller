# Changelog

All notable changes to meowcaller, tracked per module. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/). Each entry notes the module's
**validation state**: `scaffolded` (signatures + KAT test, bodies are TODO),
`implemented` (bodies written), or `KAT-verified` (its reference vector passes).

## [Unreleased]

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
