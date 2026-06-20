# MODULES.md — registry and build order

The spine of the build. Each module is a discrete, human-approved unit with a
**datasheet** under [`datasheets/`](datasheets/) that carries the reference source
verbatim and the Go target. The abstract protocol spec lives in **wacrg** (the
RFC); a datasheet links to the relevant wacrg page. The reference column names the
source to ingest **into the datasheet** — it never appears in the Go code.

Status: `planned` → `scaffolded` → `implemented` → `verified` (KAT passes).
Scope: **1:1 calls only.**

| # | Module | Package | Datasheet | wacrg spec | Reference (ingest) | KAT | Status |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 01 | rangecoder | `mlow` | rangecoder.md | codec/mlow/decode-pipeline | `mlow/rangecoder.rs` | `rc_vectors.json` | planned |
| 02 | toc | `mlow` | [mlow-toc.md](datasheets/mlow-toc.md) | codec/mlow/decode-pipeline §TOC | `mlow/toc.rs` | `toc_vectors.json` | planned |
| 03 | mem (heap ROM) | `mlow` | mlow-mem.md | codec/mlow | `mlow/smpl_mem.rs` | `smpl_tables.json` | planned |
| 04 | lpc | `mlow` | mlow-lpc.md | codec/mlow/decode-pipeline | `mlow/smpl_lpc.rs`, `mlow/smpl_perc.rs` (FFT) | `lsf_quant_io.json`, `fe_dump.json` | implemented |
| 05 | lsf | `mlow` | mlow-lsf.md | codec/mlow/decode-pipeline | `mlow/smpl_decode.rs` | `lsf_vectors.json` | verified |
| 06 | lsf_quant | `mlow` | mlow-lsf_quant.md | codec/mlow/decode-pipeline | `mlow/smpl_lsf_quant.rs` | `lsf_quant_io.json` | verified |
| 07 | pitch | `mlow` | mlow-pitch.md | codec/mlow/decode-pipeline | `mlow/smpl_pitch.rs` | `pitch_vectors.json` | verified (decode; estimator scaffolded) |
| 08 | pulse | `mlow` | mlow-pulse.md | codec/mlow/decode-pipeline | `mlow/smpl_pulse.rs` | `pulse_vectors.json` | verified |
| 09 | gains | `mlow` | mlow-gains.md | codec/mlow/decode-pipeline | `mlow/smpl_gains.rs` | `gains_vectors.json` | verified |
| 10 | synth | `mlow` | mlow-synth.md | codec/mlow/decode-pipeline | `mlow/smpl_synth.rs` | (e2e) | partial (reconstruct-nlsf verified; frame-synth e2e-gated) |
| 11 | postfilter | `mlow` | mlow-postfilter.md | codec/mlow/decode-pipeline | `mlow/smpl_*postfilter.rs`, `smpl_harmcomb.rs` | (e2e) | planned |
| 12 | vad | `mlow` | mlow-vad.md | codec/mlow | `mlow/smpl_vad.rs` | `vad_ground_truth.json` | planned |
| 13 | noise | `mlow` | mlow-noise.md | codec/mlow | `mlow/smpl_gennoise.rs` | `gennoise_vectors.json` | planned |
| 14 | red | `mlow` | mlow-red.md | codec/mlow | `mlow/red.rs` | (inline) | planned |
| 15 | decoder | `mlow` | mlow-decoder.md | codec/mlow/decode-pipeline | `mlow/decoder.rs` | `e2e_vectors.json`, `inbound_capture_frames.json` | planned |
| 16 | encoder | `mlow` | mlow-encoder.md | codec/mlow | `mlow/encode.rs`, `analysis.rs` | `sigmode_ground_truth.json` | planned |
| 17 | hkdf | `util` | util-hkdf.md | keying/srtp-key-schedule | (stdlib) | RFC 5869 | planned |
| 18 | e2e_srtp | `srtp` | srtp-e2e.md | keying/srtp-key-schedule | `e2e_srtp.rs` | inline | planned |
| 19 | hbh_srtp | `srtp` | srtp-hbh.md | keying/srtp-key-schedule | `hbh_srtp.rs` | inline | planned |
| 20 | sframe | `srtp` | srtp-sframe.md | keying/sframe-media-e2ee | `sframe.rs` | inline | planned |
| 21 | warp | `srtp` | srtp-warp.md | transport/warp-stun-relay | `warp.rs` | inline | planned |
| 22 | stanza | `signaling` | signaling-stanza.md | signaling/stanza-reference | `stanza.rs` | inline | planned |
| 23 | stun | `stun` | stun.md | transport/warp-stun-relay | `stun.rs` | inline | planned |
| 24 | rtp | `rtp` | rtp.md | transport/warp-stun-relay | `rtp.rs`, `rtcp.rs` | inline | planned |
| 25 | ssrc | `rtp` | rtp-ssrc.md | transport/warp-stun-relay | `ssrc.rs` | inline | planned |
| 26 | relay | `relay` | relay.md | transport/warp-stun-relay | `src/voip/transport.rs` | (integration) | planned |
| 27 | session | `meowcaller` | session.md | reconstruction | `src/voip/session.rs` | (integration) | planned |
| 28 | call | `meowcaller` | call.md | signaling | `src/voip/*`, `stanza.rs` | (integration) | planned |

First audible milestone: **#15 decoder** decodes the real
`inbound_capture_frames.json` to PCM matching `e2e_vectors.json`. Everything before
it serves that.

## Reference KAT status (verified 2026-06)

The reference implementation's own test suite was run (`cargo test --features
voip`). Result: the **decoder/receive path, keying, signaling, and transport all
pass** their vectors — including the end-to-end decode milestone. **One test
fails:** the **encoder** pitch estimator (reference `smpl_pitch_enc`,
`pitch_estimator_matches_c_ground_truth`) diverges from the C ground truth by
`max_err ≈ 0.030`. So:

- The decoder-path vectors (modules 01–15) are trustworthy byte/precision targets.
- The encoder pitch estimator (part of module **07 pitch** / **16 encoder**) is a
  **known soft-divergence in the reference itself** — do not treat it as a
  byte-exact target; a faithful port inherits the same ~0.03 gap. Flagged in the
  affected datasheets.

Datasheets are written **one at a time, when we reach the module** — not
bulk-generated ahead. The exemplar [`datasheets/mlow-toc.md`](datasheets/mlow-toc.md)
is the quality bar.
