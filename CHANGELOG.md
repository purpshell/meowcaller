# Changelog

All notable changes to meowcaller, tracked per module. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/). Each entry notes the module's
**validation state**: `scaffolded` (signatures + KAT test, bodies are TODO),
`implemented` (bodies written), or `KAT-verified` (its reference vector passes).

## [Unreleased]

### meowcaller — refine the video API to mirror the audio Source/Sink model
- Reshaped the ad-hoc video surface into the same shape as audio (whatsmeow-style callback
  registration + a Sink interface), so it reads like any mainstream media API:
  - **Receive**: `Call.ReceiveVideo(sink VideoSink)` (mirrors `Call.Receive`), with `VideoSink`
    (`WriteVideo`/`Close`), a `VideoSinkFunc` callback adapter (mirrors `SinkFunc`), and a
    built-in `AnnexBRecorder(path)` that records the peer's H.264 to a `.h264` file — the video
    analog of `WAVRecorder`.
  - **Send**: `Call.SendVideo(accessUnit []byte) error` — push one encoded H.264 access unit
    (Annex-B), the way you'd write a sample to a track; returns an error if media isn't up.
  - **State**: `Call.OnVideoState(func(VideoState))` with a typed `VideoState{Active, Upgrade,
    Orientation, Raw}`, replacing the raw `(state, orientation int)` tuple.
  - `Call.IsVideo()` unchanged.
  Replaces `OnVideoFrame` / `SendVideoFrame` / the int-tuple `OnVideoState`. Library carries
  only the types (`video.go`); the WebCodecs bridge stays in `examples/cli/video.go`. Lib +
  CLI build, tests green.

### meowcaller — ack the video upgrade with `type="video"` (fix mid-call audio→video upgrade)
- **Diagnosis** (live diag dump): on a mid-call `<video state="11">` upgrade, meowcaller acked
  with whatsmeow's generic **typeless** `<ack class="call">`, whereas the real WhatsApp client
  acks with **`type="video"`**. Without the typed ack the sender treats the upgrade as
  un-accepted and reverts to `state="0"` after ~5 s, never streaming video — the dump showed
  the upgrade arrive, the typeless ack, then revert, with **0 PT-97 packets**. (From-start
  video calls are unaffected: video is negotiated in the `<offer>`/`<accept>` handshake, so the
  upgrade-ack path is never used — which is why only from-start worked.)
- **Fix**: `onCallRaw` now sends a typed `<ack class="call" type="video">` for inbound
  `<video>` stanzas and skips whatsmeow's generic ack for them (returns "handled"). NOT
  VALIDATED pending a live upgrade re-test; if the typed ack alone is insufficient, the next
  candidate is an explicit `<video>` accept reply.

### mlow — decode the 0x92 SplitRed multi-frame container (video-call audio fix) — `KAT-verified`
- `MlowDecoder.Decode` now detects the `0x92 <count>` SplitRed container WhatsApp uses in
  video calls (DTX on): several sequential 60 ms MLow frames packed length-delimited in one
  RTP payload. meowcaller previously decoded the raw container as a bare frame —
  range-decoding the header bytes as audio → near-silence/noise (in a live video-call dump
  only **12 of 397** peer-audio frames had non-zero RMS). New `splitContainer` splits it;
  each sub-frame is decoded in order and concatenated. Additive and gated on the `0x92`
  marker, so the bare-frame KATs are unchanged (mlow KATs green). Surfaced by WaCalls
  `feat/video-calls` `decoder.go`; origin is the whatsapp-rust MLow decoder.

### meowcaller — bidirectional video media + orientation, based on WaCalls
- **Receive (confirmed live)**: `engine.runMedia` demuxes the recv loop by RTP payload type
  — H.264 (PT 97) → a second WARP pipeline on the video SSRC (participant slot 2) →
  SRTP-unprotect → `rtp.H264Depacketizer` → Annex-B reassembly on the marker →
  `Call.OnVideoFrame`. `onOffer` flags a video call via `signaling.OfferHasVideo`
  (`Call.IsVideo()`).
- **Send**: `Call.SendVideoFrame(annexB)` fragments an encoded H.264 access unit
  (`rtp.SplitAnnexB` → `PackageH264NALU`) into PT-97 RTP (video SSRC, marker on the last
  NAL, 90 kHz / 15 fps), E2E-SRTP-protects it via the new `MediaPipeline.ProtectRTP`, and
  sends it to the relay. Ported from WaCalls `callmanager_video.FeedCapturedVideo`.
  meowcaller carries *encoded* H.264 — no pure-Go encoder (the browser encodes).
- **Orientation**: inbound standalone `<video>` stanzas are dispatched in `onCallRaw` →
  `engine.onVideoStanza` → `Call.OnVideoState(state, orientation)`; the example bridge
  rotates the displayed canvas by orientation × 90°.
- **Video bridge moved to the example, not the library**: the ephemeral WebCodecs HTTP
  bridge now lives in `examples/cli/video.go` (package main). The library boundary stays the
  Call API (`OnVideoFrame` / `SendVideoFrame` / `OnVideoState`); the demo server (SSE `/in`
  display + orientation, POST `/out` camera) is example code. `autoaccept` starts one per
  call and prints its URL. Mirrors WaCalls's React + WebCodecs client (see README Credits).
- The send path and orientation rotation are implemented but not yet confirmed on a live call.
  Receive demux carries `// Source of truth:` links to WaCalls `callmanager_video.go` and
  emits a `video` diag stream per inbound frame.

### signaling/video — `<video>` advertise + inbound detect, ported from WaCalls — `KAT-verified`
- New `signaling/video.go`: optional `<video enc=h264 dec=h264 …>` advertisement on
  `BuildOffer`/`BuildAccept` (additive `Video bool` on the params — the audio path is
  unchanged), and `OfferHasVideo(node)` to detect an inbound video call by the `<video>`
  child. **Ported from WaCalls (jotadev66, MIT) `feat/video-calls` `2d6a1f6`** with
  `// Source of truth:` links; KAT (`video_test.go`) covers the advertise positions +
  detection. meowcaller's `CapabilityOffer` is already `…e4bb13`, matching the branch.

### rtp/h264 — H.264 RTP packetization, ported from WaCalls — `KAT-verified`
- New `rtp/h264.go`: `PackageH264NALU` (single payload / FU-A fragmentation),
  `PackageH264STAPA`, `SplitAnnexB`, and `H264Depacketizer` — RFC 6184 H.264
  packetization/depacketization, the codec layer for video calls. **Ported verbatim from
  [WaCalls](https://github.com/JotaDev66/WaCalls) (jotadev66, MIT) `feat/video-calls`
  `2d6a1f6`**; each function carries a `// Source of truth:` permalink to its WaCalls
  origin, and the KAT (`h264_test.go`, also ported) covers single-NALU / FU-A / STAP-A
  roundtrip and Annex-B split. First module of the WaCalls-based video support (see README
  Credits). Framing-agnostic, so it drops in cleanly; the SRTP/relay integration follows.

### diag — engine emissions: keying/ssrc/srtp/rtp/media/relay/stun/meta streams
- Wired exact `e.c.diag.Emit(...)` calls at the engine boundaries (nil-safe, so zero
  cost when diagnostics are off). `engine.go`: **keying** (outbound generated callKey,
  inbound decrypted callKey — raw hex) and **meta** (offer_sent/offer_received).
  `engine_media.go` `connectAndAllocate`/`runMedia`: **relay** (endpoint, relay keying
  material, every inbound relay packet), **stun** (allocate, consent ping, 1 Hz
  keepalive, binding-success), **ssrc** (derived participant SSRC + info string),
  **srtp** (media-key derivation inputs; per-frame `frame_unprotected` + decrypted
  payload; unprotect failures), **rtp** (inbound header: ssrc/seq/ts/pt/marker),
  **media_out** (per-frame source RMS + encoded payload + protected packet, hex),
  **media_in** (per-frame decoded sample count + RMS), and **meta** milestones
  (media_start, first_rtp_sent, first_rtp_in). Added an `rmsFloat32` helper and a
  keepalive tick counter; `UnprotectAudio` now binds its header for the `rtp` stream.
  Deferred to v2 (would touch session/srtp/rtp signatures): derived SRTP subkeys,
  per-packet ROC/IV/header struct, and raw PCM sidecar files (RMS only for now). Added
  a `diag` recorder KAT (JSONL output + nil-safety). build/vet/suite green.

### examples/cli — --diagdump <dir> flag (xmpp + log capture via a logger tee)
- New developer flag `--diagdump <dir>` (parsed out of `os.Args` before the positional
  dispatch). When set, the CLI builds a `diag.Recorder`, tees the zerolog stream via
  `zerolog.MultiLevelWriter(console, diagSplitter)`, and passes
  `WithDiagnostics(rec)` to `NewClient`. `diagSplitter` json-decodes each event and
  routes whatsmeow's `wa/Recv`/`wa/Send` stanza dumps (raw XML) to `xmpp.jsonl` and
  everything else to `log.jsonl`. Forces ≥ debug level so stanzas are captured, and
  prints a one-line warning that the dir holds raw key/media material. Captures the
  **xmpp** stream end-to-end; the engine-side exact streams (keying/srtp/rtp/media)
  follow. CLI build/vet green.

### diag — developer diagnostics recorder + WithDiagnostics client option
- New `meowcaller/diag` package: a `Recorder` that writes exact, per-category call
  diagnostics to per-stream JSONL files (`<dir>/<stream>.jsonl`). Stdlib-only,
  thread-safe, nil-safe (every method no-ops on a nil `*Recorder`), lazy file open,
  injects a `ts_ms`, and swallows write errors so diagnostics can never break a live
  call. New additive `Client` option `WithDiagnostics(*diag.Recorder)` (config/Client
  `diag` field) so the engine reaches it as `e.c.diag`. **Dev-only carve-out:** the
  recorder may dump raw secrets/media (callKey, SRTP/RTP bytes, PCM) — it is opt-in
  and off by default; the library's production zerolog logging stays sanitized.
  Foundation only; CLI flag and engine emissions follow. build/vet/test green.

### meowcaller — accept only on the first mute_v2 (not on later mute-state changes)
- The deferred `<accept>` is sent on the **first** `mute_v2` only (it arrives right
  after the relaylatency/transport). `onCallRaw` now gates on `acceptPending`: a later
  `mute_v2` — an in-call mute-state change (e.g. 1→0) — is logged at debug and ignored
  instead of re-running the accept path and re-logging "sending deferred accept" on
  every mute toggle. `sendAccept` stays the authoritative one-shot (re-checks
  `acceptPending` under the lock). Live-path glue; covered by call behavior, no unit
  test.

### opus — implement voip_settings parse + codec selection
- Landed the bodies scaffolded below. `ParseVoipSettings` json-decodes the
  stringly-typed blob (`encode.use_mlow_codec_v1`/`frame_ms`, `rc.target_bitrate`),
  defaulting `UseMlowCodecV1` to true unless the key is literally `"false"` (empty blob
  → MLow). `selectAudioCodec` returns Opus only for a present, explicit
  `use_mlow_codec_v1=false`. KATs enabled and passing
  (`TestParseVoipSettings`/`...Opus`, `TestSelectAudioCodec`). State:
  **implemented; KAT-verified**. build/vet/suite green. (CodeRabbit review skipped at
  maintainer request.)

### opus — scaffold voip_settings parse + codec selection (mlow vs Opus lever)
- First module of basic Opus support, picked when the server sets
  `encode.use_mlow_codec_v1=false`. New compiling surface, bodies are TODO:
  `signaling.VoipSettings` + `signaling.ParseVoipSettings` (the codec-relevant subset
  of the `<voip_settings>` JSON blob) and `AudioCodec` (`AudioCodecMlow`/`AudioCodecOpus`)
  + `selectAudioCodec` in the root package. `engineCall` gains a `codec` field, and
  `onOffer` (inbound) / `onCallAck` (outbound) now run `applyVoipSettingsCodec` to find
  the blob (`findChild`), parse it, and record the per-call codec — inert today (parser
  stub → defaults to MLow), so the live MLow path is unchanged. **Original glue, not a
  port:** the whatsapp-rust reference does not read `use_mlow_codec_v1` (it steers onto
  Opus by advertising only `<audio rate=8000>`), so the parser/selector carry no
  `// Source of truth:` line. KATs (`TestParseVoipSettings`/`...Opus`,
  `TestSelectAudioCodec`) wired to the captured sample blob and `t.Skip`-blocked on the
  stubs. State: **scaffolded**. build/vet clean, suite green.

### meowcaller — preaccept eagerly on inbound offer (preparation step)
- `<preaccept>` is now sent the moment an inbound offer arrives (in `onOffer`),
  independent of the later Answer/Reject decision — it is a preparation step that keeps
  the offer alive and joins the relay election while the integrator decides (a call the
  user goes on to decline has usually already been preaccepted). `Answer` now only
  commits to the call (marks accept-pending → `<accept>` on `<mute_v2>`, starts media);
  `Reject` declines after the preaccept already went out. Restores the original working
  recipe (preaccept → relaylatency → wait-for-`mute_v2` → accept).

### mlow — move per-frame encode/decode logs from debug to trace
- The routine per-frame encode/decode logs (`encode frame`/`encode frame: done`,
  `decode frame`/`decode active frame`, the per-frame "emitting silence" outcomes,
  `red depack: done`) fired ~50×/sec at debug level and flooded a live call. Moved
  them to **trace** (per the documented granularity: trace = per-frame state). Default
  debug output is now quiet; genuine rare events (buffer overflow, wrong frame size,
  RED-depack failure, unexpected standard-Opus packet) stay at debug. Full per-frame
  detail remains under `MEOW_LOG_LEVEL=trace`. KATs green.

### meowcaller — implement the managed calling API; collapse the CLI example
- Filled the managed engine (`engine.go` + `engine_media.go`) by lifting the entire
  calling orchestration out of `examples/cli` into the library: signaling (offer /
  preaccept / accept-deferred-until-`mute_v2` / relaylatency election / ack-relay /
  terminate), the `<ack>`/`<call>` node interception, and the per-frame relay media
  loop — driven by the `Call`'s `Player` (outbound) and sink (inbound) instead of
  mic/speaker. The hard-won relay recipe is preserved exactly (consent ping at t+0
  before any RTP, 1 Hz allocate+ping keepalive, no STUN binding-requests; the live
  path is `NOT VALIDATED`). Added the pure-Go audio decoders (`WAVFile`/`MP3File` via
  go-mp3 / `OpusFile` via pion/opus, with downmix+resample) and the `WAVRecorder`
  sink, plus a new opt-in cgo module `meowcaller/audio/malgo` (`Mic`/`Speaker`) so the
  core stays cgo-free. **`examples/cli` collapsed to a single `main.go`** (call /
  play / listen / autoaccept) — the loopback mode and all hand-rolled orchestration
  removed. All three modules build/vet clean; KATs green. Preaccept is deferred to
  `Call.Answer()` (the integrator decides Answer vs Reject); inbound calling still
  uses whatsmeow `DangerousInternals` pending its promotion to stable upstream API.

### meowcaller — scaffold the managed calling API (Client/Call/Player/audio)
- Began lifting the entire calling orchestration out of `examples/cli` into a managed,
  high-level library API so consumers write a handful of lines instead of hand-rolling
  signaling/relay/media. New compiling surface: `Client` (`NewClient(wa)`, `Call`,
  `OnIncomingCall`), `Call` (`Answer/Reject/Hangup`, `Subscribe/Play/Receive`, typed
  `OnReady/OnEnd/OnStateChange`), `Player` (discord.js-style audio player with
  `OnFinish`), and the `AudioSource`/`AudioSink` model (pure-Go `PCMStream`/`SinkFunc`;
  WAV/MP3/Opus decoders and the CGO `Mic`/`Speaker` follow). The internal `engine`
  (port of the example's coordinator + media loop) is stubbed; the public contract is
  locked. Wrapping the whatsmeow client pulls its tree into the library go.mod (root is
  the calling API; the codec stays light under `meowcaller/mlow`). Build/vet/KATs green.

### examples — move mlowtest under examples/mlow; rename voip example to cli
- Moved `cmd/mlowtest` → `examples/mlow` (stays in the root module — it only imports
  `mlow`) and removed the now-empty `cmd/`. Renamed the `examples/voip` example module
  → `examples/cli` (go.mod module path, README, and the in-tool command name updated;
  the hardcoded `wa-voip.db`/`meowcaller.db` session files are unchanged). Updated
  `scripts/mlow_file_test.sh` (`./cmd/mlowtest` → `./examples/mlow`) and the root
  `.gitignore` (`/mlowtest` → `/mlow`). Both modules build/vet/test clean.

### docs — remove datasheets, PLAN/MODULES/GLOSSARY; prune coderabbit config
- Removed the internal build-protocol docs: the `datasheets/` directory (30 files),
  `PLAN.md`, `MODULES.md`, and `GLOSSARY.md`. Dropped the now-obsolete
  `!datasheets/**` path filter (and its comment) from `.coderabbit.yaml`, keeping the
  generated-protobuf exclusion. No code touched; build/tests unaffected.

### docs — codify code style + logging conventions in AGENTS.md
- Added a binding **Code style and logging** section (and matching "what never
  happens here" bullets) to `AGENTS.md`: the style supplements (`any`, initialism
  casing, `var x T`, indent-error-flow), errors-over-crashes (library never panics
  on runtime/wire input), and the full zerolog logging contract — field-on-type /
  variadic plumbing defaulting to `zerolog.Nop()`, the zero-value-logger hazard, the
  hard no-secrets sanitization rule, boundary (not hot-loop) granularity, structured
  no-emoji form, and the level definitions. Documentation only.

### lib — propagate sanitized opt-in zerolog debug/trace across the stack
- Rolled the `session` logging convention out to every library package — `mlow`,
  `srtp`, `rtp`, `stun`, `signaling`, `relay`, `util` — so the whole call + codec
  path emits debug/trace. Stateful types (`RtpStream`, `SframeSession`,
  `RelayMediaChannel`, `MlowDecoder`, `MlowEncoder`) carry a `log zerolog.Logger`
  field set via an additive `WithLogger` option; stateless functions (HKDF, STUN
  encode/parse, stanza builders, RTP header/SSRC, SRTP key-derivation/crypt) take a
  trailing variadic `...zerolog.Logger` resolved by `pickLog`. Both default to
  `zerolog.Nop()` — silent and zero-cost unless the top-level program wires a logger,
  and **no existing exported signature or call site changed** (variadic/option are
  source-compatible). Granularity is per-frame / per-packet / per-key-derivation;
  no logging inside per-sample/per-symbol hot loops (rangecoder, FFT, filters stay
  silent). Logs are **sanitized** — only lengths, counts, `ssrc`/`seq`/`roc`,
  message/packet types, LIDs, flags; never key material, payload, ciphertext, PCM,
  tokens, or IVs (verified by an independent per-package adversarial secret-leak
  audit). All 28 module KATs stay green; `go build`/`vet`/`test`/`gofmt` clean.

### session — opt-in sanitized zerolog diagnostics (field-on-type)
- Established the repo-wide library logging convention on the root package
  (`MediaPipeline`, `CallSession`): a `log zerolog.Logger` field set via an additive
  `WithLogger(l)` functional option (`logging.go`), defaulting to `zerolog.Nop()` so
  the library stays silent and zero-cost unless the top-level program wires a logger.
  Added debug/trace at every boundary: session lifecycle + phase transitions (debug),
  pipeline init + key-derivation failures (debug), and per-frame protect/unprotect
  (trace). Logs are **sanitized** — only `ssrc`/`seq`/`roc`/byte-lengths/JIDs, never
  key material, payload, or PCM. Constructors stay source-compatible (variadic opts);
  KAT green, no behavior change.

### examples/voip — migrate CLI logging to structured zerolog (no emoji)
- Replaced the stdlib `log`/`Printf` calls (and all decorative emoji) across
  `main.go`, `call.go`, `media.go`, `loopback.go` with structured **zerolog** per
  the Beeper Go Guidelines. As the top-level program the command configures one
  console logger and embeds it in the `context`; callees resolve it with
  `zerolog.Ctx(ctx)`; the `coordinator` carries a logger field; whatsmeow's own
  logs bridge in via `waLog.Zerolog(...).Sub(...)`. Log keys are `snake_case`,
  errors carry `.Err(err)`, and levels span info/debug/warn/error. Logs are
  **sanitized**: callKey, relay key, and tokens are logged as byte-lengths only,
  never their contents. No library code or KATs touched (examples is its own
  module); `go build`/`vet`/`test` clean.
- Wired the context logger into the library calls so the demo surfaces the whole
  stack's debug/trace: `WithLogger` on `NewMediaPipeline`/`NewMlowEncoder`/
  `NewMlowDecoder`/`ConnectRelayMedia`, and the variadic logger on the `rtp`/`stun`
  calls. Added a `MEOW_LOG_LEVEL` env control (default `debug`) so
  `MEOW_LOG_LEVEL=trace voip call …` shows the per-frame trace across mlow, srtp,
  rtp, stun, relay, and the pipeline.

### audit — behavioral validation against the Rust reference (multi-agent)
- Ran a 28-module Go-vs-Rust behavioral audit (KAT + line-for-line fidelity +
  adversarial refutation). Result: **0 real behavioral divergences**; the flagged
  items were datasheet staleness, a provable CDF-accessor equivalence (#16 LR
  filt), and one genuine stub (#20). Fixes below.

### mlow/celp — drop dead smplCelpUvGain; pin + refresh mlow-celp datasheet
- Refreshing `mlow-celp.md` to the current `smpl_celp.rs` surfaced that the
  reference deleted three unused helpers as dead code (`e7b106d`):
  `smpl_reverse_into`, `smpl_interpol`, `smpl_celp_uv_gain`. Two were never ported;
  `smplCelpUvGain` existed in `celp_enc.go` with no callers (linter-flagged) —
  removed to mirror the reference. No behavior change (it was unused; `fcbgainsUV`
  /`uvGainIdxLen` keep their live users). Datasheet pinned at 41095d4.

### datasheets — pin + refresh all mlow datasheets to 41095d4
- All 16 mlow datasheets (#01–#16) now carry the `Reference pinned at:
  41095d4e6ba4610e054e9ede3af1d5e88a83faee` line. 8 had current verbatim and only
  needed the pin (rangecoder, toc, lpc, pulse, gains, lsf_quant, vad, red); 8 had
  drift and were refreshed to byte-identical current reference source: lsf, mem
  (smpl_mem seed-build + table relocated to silk_lsf_cos_tab.rs), noise (perc FFT
  twiddle restructure), decoder (had_error flag + cc args, params reshape),
  encoder (MlowError + cc args + seed pitch loader), postfilter, pitch, synth
  (smpl_nrgres comment cleanup). Documentation hygiene only — no behavior changed;
  the Go was already KAT-verified against the current reference.

### srtp/sframe — implement DeriveWarpAuthKey (#20, KAT-verified)
- Ported `derive_warp_auth_key`: `len==32` guard then HKDF-SHA256(empty salt, ikm
  = callKey, info = "warp auth key", 32). Was a `(nil,nil)` stub. Added a KAT
  against an independently computed HKDF-SHA256 vector — passes. Closes the #20
  functional gap.

### mlow/noise — FMA-defeat casts in smplGetEnv (#11)
- Wrapped the four load-bearing products in `smplGetEnv`'s loop in `float32(...)`
  so Go can't fuse `a*b + c` into a single-rounding FMA (the reference rounds each
  multiply separately). No observed divergence before, but the protective casts
  AGENTS.md mandates were missing. gennoise KAT still passes.

### MODULES.md — status corrections from the audit
- #08 pitch: was `verified (decode; estimator scaffolded)` — the estimator is
  KAT-verified (`pitchio_ground_truth.json`); corrected to reflect that.
- #13 synth: was `verified (...)` but `TestSynth` is `t.Skip`'d (no standalone
  `SynthInternalFrame` vector); corrected to `partial` per the status rule.

### call — module #28 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- Implement the `CallRegistry` (root package `meowcaller`, porting `src/voip/registry.rs`):
  the thread-safe per-call map with `Insert`/`SetMediaTask`/`Phase`/`Transition`/
  `Snapshot`/`ActiveCount`/`Remove`/`AbortAll`, over a `sync.Mutex` + `map[string]*callEntry`.
  The `tokio::AbortHandle` model maps to **`context.CancelFunc`** (human-chosen): the
  media goroutine is spawned with a cancellable context and the registry stores
  `cancel`. Both pinned cancel behaviors are preserved — replace-and-cancel the old
  handle, and cancel an orphan handle attached to an unknown/removed call. Cancels run
  outside the lock (non-blocking). KAT (inline: bookkeeping contract + cancellation on
  remove/abort-all/replace/orphan, observed via `ctx.Done()`) passes, including under
  `-race`. CodeRabbit: clean. MODULES.md: #28 -> verified. **This completes the module
  registry (#01–#28).**

### session — module #27 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New root package `meowcaller` porting `src/voip/session.rs`: the `CallSession`
  phase state machine (validated transitions — `Ended` sink, `Idle→Calling` only when
  outgoing, the linear chain, idempotent self-loop) and `MediaPipeline`, which
  composes the verified `rtp` + `srtp` modules into the protect/unprotect path (RTP
  WARP header → E2E-SRTP encrypt → WARP MI tag, and the reverse; recv ROC tracked
  internally via `RecvRocTracker`). Built on whatsmeow `types.JID`. **Error-based**
  per the lower modules: `NewMediaPipeline`/`ProtectAudio` return `error`;
  `UnprotectAudio` keeps the reference's `Option` shape as `(rtp.RtpHeader, []byte,
  bool)`. KAT (inline, synthetic LIDs — no PII) passes: both lifecycle tables, the
  pipeline round-trip, and the **send=self-LID / recv=peer-LID ciphertext pinning**
  (the interop-load-bearing key direction). Composition only — the byte-level crypto
  is vector-pinned in its own modules. Datasheet refreshed to the current source
  (internal `RecvRocTracker`, `Option` new, roc-less unprotect, the new
  `recv_uses_peer_lid_for_recv` test) and pinned; deps corrected (it composes
  e2e_srtp/rtp/ssrc/warp, not the codec/relay/stanza the registry had guessed).
  **KAT-verified.**

### relay — module #26 (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New `relay` package porting `src/voip/transport.rs`: `ClassifyRelayPacket` (the
  pure first-byte STUN/RTCP/RTP demux) is **KAT-verified** against the reference's
  inline assertions. The media transport — `ConnectRelayMedia`
  (UDP→DTLS→SCTP→DataChannel) + `RelayMediaChannel.Send`/`Recv`/`Close` — is a
  faithful port over **pion** (`pion/dtls/v3`, `pion/sctp`, `pion/datachannel`;
  adopted by human decision, now direct deps). pion's `dtls.Conn` is a `net.Conn`, so
  the reference's util-version `Conn` bridge isn't needed. These transport bodies
  carry `// NOT VALIDATED:` — like the reference (`connect_relay_media` "not
  exercised in CI"), there is **no vector**; they are validated only against a live
  relay. Added (beyond the reference's Rust-Drop cleanup) explicit error-path rollback
  in `ConnectRelayMedia` and a `Close` that tears the stack down in reverse — fixes a
  CodeRabbit resource-leak finding. CodeRabbit otherwise clean (its pion/dtls
  CVE-2026-26014 flag is moot: that affects ≤ v3.1.0; we pin v3.1.2). Relay datasheet
  re-mapped to `src/voip/transport.rs` (was mis-flagged UNMAPPED).

### signaling/stanza — module #25 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New `signaling` package porting `stanza.rs`: the call-control builders
  (`BuildOffer`/`Accept`/`Preaccept`/`Transport`/`RelayLatency`/`Heartbeat`/
  `Terminate`/`MuteV2`/`Reject`) + `EncodeLatency` + the capability blobs. Built on
  **whatsmeow's** `binary.Node` + `types.JID` (adopted by human decision; `go.mau.fi/whatsmeow`
  is now a direct dep — PLAN.md dependency policy amended accordingly). whatsmeow has
  no fluent `NodeBuilder`, so builders construct `Node` structs directly; JID params
  are passed **by value** (`types.JID`) — the reference's `&Jid` is always present, and
  value semantics avoid a nil-deref panic while keeping pure-builder signatures (no
  `error`). The load-bearing `<offer>` child order and the transport `protocol=0`
  (omitted only for type "9") rule are preserved. KAT (inline, mirrors the reference's
  six child-order/attr tests — synthetic LIDs, no PII) passes. CodeRabbit: clean
  (one nil-deref finding fixed by the value-JID change). **KAT-verified.**

### srtp/warp — module #24 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- Complete the `srtp/warp` module: `WarpExtProfile`/`WarpAudioPiggybackExt`/
  `WarpMITagLen` constants, `AudioPiggybackExtensionFor` (now implemented — fills the
  #22 rtp piggyback prerequisite), and `ComputeWarpMITag`/`AppendWarpMITag` (the
  HMAC-SHA1 WARP MESSAGE-INTEGRITY tag over `packet || roc_be32`). Implemented over
  stdlib `crypto/hmac`+`crypto/sha1`+`encoding/binary` (no new deps; SHA-1 is
  protocol-mandated by WARP, not a security choice). `AudioPiggybackExtensionFor`
  returns `*uint32` (the Go mapping of `Option<u32>`) so the rtp sequencer assigns it
  directly to `RtpHeader.ExtensionWord`. KAT (`kats.json` `warp_mi_tag4` over the
  sample packet + piggyback gating, synthetic — no PII) passes byte-exact.
  CodeRabbit: clean. Datasheet envelope corrected to `*uint32`. **KAT-verified.**
  Note: `sframe.DeriveWarpAuthKey` remains a stub — warp's MI tag uses the SRTP auth
  key, not the warp-auth key, so that helper still has no vector (validate at
  session/relay).

### rtp/ssrc — module #23 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- `rtp` package gains SSRC derivation + participant-LID helpers:
  `DeriveWasmParticipantSsrc` (HKDF-SHA256 with salt=slot-word LE32, ikm=callId,
  info=lid → LE u32) via the #17 `util.HKDFSHA256`, `DeriveWasmRelayStreamSsrcs`
  (all 9 slots), `FormatE2ESrtpParticipantID` (delegates to the extracted
  `util.FormatParticipantID`), and `E2EParticipantIDVariants` (deduped recv-path
  LID variants). Per the standing convention the derivation returns
  `(uint32, error)` — the error is impossible for 4-byte output but bubbles rather
  than panics. KAT (`kats.json` voip_crypto ssrc_slot0/1 + the participant-id format
  rules + a variants check, synthetic — no PII) passes. CodeRabbit: clean.
  **KAT-verified.**

### rtp — module #22 KAT-verified (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- New `rtp` package porting `rtp.rs` + `rtcp.rs`: the WhatsApp RTP header (16-byte
  speech / 20-byte `0xdebe` DTX) encode/parse, the Opus payload classifiers
  (`IsOpusDtxPayload`/`IsOpusMlowSpeechPayload`/`IsOpusPrimingPayload`/...), the
  on-wire size estimator, the send-side sequencer (`RtpStream` with marker latch +
  seq/timestamp wrap), and RTCP compact reports (208/209) + Sender Report (200).
  Implemented over stdlib `encoding/binary`+`bytes` (no new deps). `Option` returns
  map to `(val, bool)` classifications; the SR NTP fraction uses faithful `float64`
  truncation (the KAT's `nowMs` lands on a whole second, so frac=0). The sequencer's
  piggyback branch calls the scaffolded `srtp.AudioPiggybackExtensionFor` (lands with
  #24; not on the rtp KAT path, `warpPiggyback=false`). KAT (`kats.json` rtp + rtcp,
  synthetic — no PII) passes byte-exact across all eight cases. CodeRabbit: clean.
  Adds an `rtp → srtp` package dep (no cycle — `warp.rs` doesn't import rtp).
  **KAT-verified.**

### srtp/warp — prerequisite scaffold (reference `41095d4e6ba4610e054e9ede3af1d5e88a83faee`)
- Scaffolded `AudioPiggybackExtensionFor` + `WarpPiggybackStartPacket` in the `srtp`
  package so #22 rtp compiles against the real warp surface (AGENTS.md directive #5).
  Body is a TODO stub; lands with module #24. **scaffolded.**

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
