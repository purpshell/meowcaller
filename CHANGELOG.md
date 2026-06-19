# Changelog

All notable changes to meowcaller, tracked per module. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/). Each entry notes the module's
**validation state**: `scaffolded` (signatures + KAT test, bodies are TODO),
`implemented` (bodies written), or `KAT-verified` (its reference vector passes).

## [Unreleased]

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
