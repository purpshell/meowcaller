# MODULES.md — registry and build order

The spine of the build. Each module is a discrete, human-approved unit with a
**datasheet** under [`datasheets/`](datasheets/) that carries the reference source
verbatim and the Go target. The abstract protocol spec lives in **wacrg** (the
RFC); a datasheet links to the relevant wacrg page. The reference column names the
source to ingest **into the datasheet** — it never appears in the Go code.

Status: `planned` → `scaffolded` → `implemented` → `verified` (KAT passes).
Scope: **1:1 calls only.**

**Build order is dependency-topological:** the table is sorted so that every
module's prerequisites sit above it — building top-to-bottom, a module's deps are
always already built (no forward prereqs). The `Deps` column names those
prerequisites (module names, the stable identifier — refer to modules by name, not
number, since the `#` is just the build position and can shift if the order is
re-sorted). Deps for not-yet-built modules are inferred from the reference and are
confirmed when the module is reached.

| # | Module | Package | Deps | Datasheet | Reference (ingest) | KAT | Status |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 01 | rangecoder | `mlow` | — | rangecoder.md | `mlow/rangecoder.rs` | `rc_vectors.json` | planned |
| 02 | mem (heap ROM) | `mlow` | — | mlow-mem.md | `mlow/smpl_mem.rs` | `smpl_tables.json` | planned |
| 03 | toc | `mlow` | rangecoder | [mlow-toc.md](datasheets/mlow-toc.md) | `mlow/toc.rs` | `toc_vectors.json` | planned |
| 04 | lpc | `mlow` | — | mlow-lpc.md | `mlow/smpl_lpc.rs`, `mlow/smpl_perc.rs` (FFT) | `lsf_quant_io.json`, `fe_dump.json` | implemented |
| 05 | lsf | `mlow` | rangecoder, mem | mlow-lsf.md | `mlow/smpl_decode.rs` | `lsf_vectors.json` | verified |
| 06 | pulse | `mlow` | rangecoder, mem | mlow-pulse.md | `mlow/smpl_pulse.rs` | `pulse_vectors.json` | verified |
| 07 | gains | `mlow` | rangecoder, mem, pulse | mlow-gains.md | `mlow/smpl_gains.rs` | `gains_vectors.json` | verified |
| 08 | pitch | `mlow` | rangecoder, mem, lsf, pulse | mlow-pitch.md | `mlow/smpl_pitch.rs` | `pitch_vectors.json` | verified (decode; estimator scaffolded) |
| 09 | lsf_quant | `mlow` | lpc | mlow-lsf_quant.md | `mlow/smpl_lsf_quant.rs` | `lsf_quant_io.json` | verified |
| 10 | postfilter | `mlow` | — | mlow-postfilter.md | `mlow/smpl_*postfilter.rs`, `smpl_harmcomb.rs` | (e2e) | scaffolded |
| 11 | noise | `mlow` | — | mlow-noise.md | `mlow/smpl_gennoise.rs` | `gennoise_vectors.json` | verified (gennoise core; perc/bitrate scaffolded w/ encoder) |
| 12 | vad | `mlow` | — | mlow-vad.md | `mlow/smpl_vad.rs` | `vad_ground_truth.json` | planned |
| 13 | synth | `mlow` | lsf, lsf_quant, postfilter, noise | mlow-synth.md | `mlow/smpl_synth.rs` | (e2e) | partial (reconstruct-nlsf verified; synth bodies impl/NOT-VALIDATED; CELP SynthFrame needs postfilter+noise) |
| 14 | red | `mlow` | rangecoder, toc | mlow-red.md | `mlow/red.rs` | (inline) | planned |
| 15 | decoder | `mlow` | lsf, pulse, pitch, gains, synth, postfilter, noise, red | mlow-decoder.md | `mlow/decoder.rs` | `e2e_vectors.json`, `inbound_capture_frames.json` | planned |
| 16 | encoder | `mlow` | lpc, lsf_quant, pitch, vad | mlow-encoder.md | `mlow/encode.rs`, `analysis.rs` | `sigmode_ground_truth.json` | planned |
| 17 | hkdf | `util` | — | util-hkdf.md | (stdlib) | RFC 5869 | planned |
| 18 | e2e_srtp | `srtp` | hkdf | srtp-e2e.md | `e2e_srtp.rs` | inline | planned |
| 19 | hbh_srtp | `srtp` | hkdf | srtp-hbh.md | `hbh_srtp.rs` | inline | planned |
| 20 | sframe | `srtp` | hkdf | srtp-sframe.md | `sframe.rs` | inline | planned |
| 21 | stun | `stun` | — | stun.md | `stun.rs` | inline | planned |
| 22 | rtp | `rtp` | — | rtp.md | `rtp.rs`, `rtcp.rs` | inline | planned |
| 23 | ssrc | `rtp` | rtp | rtp-ssrc.md | `ssrc.rs` | inline | planned |
| 24 | warp | `srtp` | stun, e2e_srtp, hbh_srtp | srtp-warp.md | `warp.rs` | inline | planned |
| 25 | stanza | `signaling` | — | signaling-stanza.md | `stanza.rs` | inline | planned |
| 26 | relay | `relay` | warp, stun, rtp | relay.md | `src/voip/transport.rs` | (integration) | planned |
| 27 | session | `meowcaller` | decoder, encoder, sframe, relay, stanza | session.md | `src/voip/session.rs` | (integration) | planned |
| 28 | call | `meowcaller` | session, stanza | call.md | `src/voip/*`, `stanza.rs` | (integration) | planned |

First audible milestone: **decoder (#15)** decodes the real
`inbound_capture_frames.json` to PCM matching `e2e_vectors.json`. Everything above
it in the table serves that.

> Re-sorted into dependency order (was a flatter sequence). Notable moves: `pulse`
> now precedes `pitch`/`gains` (which consume it); `postfilter`/`noise` precede
> `synth` (its CELP path runs them); `stun`/`rtp` precede `warp`/`relay`. Built
> modules kept their names; their `#` shifted — historical commit/CHANGELOG entries
> reference the **old** numbers, so cite modules by **name**.

## Reference KAT status (verified 2026-06)

The reference implementation's own test suite was run (`cargo test --features
voip`). Result: the **decoder/receive path, keying, signaling, and transport all
pass** their vectors — including the end-to-end decode milestone. **One test
fails:** the **encoder** pitch estimator (reference `smpl_pitch_enc`,
`pitch_estimator_matches_c_ground_truth`) diverges from the C ground truth by
`max_err ≈ 0.030`. So:

- The decoder-path vectors (the decode pipeline above `decoder`, #01–#15) are
  trustworthy byte/precision targets.
- The encoder pitch estimator (part of **pitch** (#08) / **encoder** (#16)) is a
  **known soft-divergence in the reference itself** — do not treat it as a
  byte-exact target; a faithful port inherits the same ~0.03 gap. Flagged in the
  affected datasheets.

Datasheets are written **one at a time, when we reach the module** — not
bulk-generated ahead. The exemplar [`datasheets/mlow-toc.md`](datasheets/mlow-toc.md)
is the quality bar.
