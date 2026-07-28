# Changelog

All notable changes to meowcaller, tracked per module. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/). Each entry notes the module's
**validation state**: `scaffolded` (signatures + KAT test, bodies are TODO),
`implemented` (bodies written), or `KAT-verified` (its reference vector passes).

## [Unreleased]

### mlow/fft — `implemented`
- Optional NEON acceleration of the mixed-radix FFT for `arm && cgo` builds
  (`fft_neon.c` + cgo bridge), with a pure-Go fallback (`fft_fallback.go`) so
  every other build is byte-identical and unchanged. `cfft` routes through the C
  kernel when present. The C is an independent port of `fftRec` compiled
  `-mfpu=neon -ffast-math`; it is **not** bit-exact (fast-math reorders the FP),
  validated instead by PESQ over the MLow round-trip (unchanged MOS) and an
  on-device benchmark (webOS TouchPad ARMv7): the FFT was ~31% of the encode.

### media/group-runtime — `KAT-verified`

- Hardened live group-call teardown by closing and detaching audio endpoints,
  accepted timestamp-less encrypted rekey controls, and added sanitized
  diagnostics for group media readiness and key installation.
- Corrected RTP CVO display orientation to use the standardized receiver
  quarter-turn value directly. Android portrait no longer receives the inverse
  rotation while iPhone portrait remains unchanged.

### api/upstream-group-call-adapter — `partial`
- Added a latest-upstream-Whatsmeow compatibility layer for initial ad-hoc and
  group-bound calls, active-call participant invite/ring, active-group
  preaccept/accept, call links, approval waiting rooms, hand state, and
  screen-share state. Unsupported call actions use binary nodes through
  `DangerousInternals` and the existing raw call hook; no Titan fork or
  `replace go.mau.fi/whatsmeow` directive is required.
- Preserved the existing 1:1 signaling path. Raw group controls are parsed once
  and receive the capture-shaped typed ACK, while unrelated call actions remain
  under upstream whatsmeow.
- Added public transaction-ordered participant/waiting-room snapshots,
  participant-attributed reactions/video, group-ID dialing, and browser-console
  coverage. Builder/parser and adapter KATs pass; fresh live end-to-end
  validation remains pending.

### media/group_rtcp_feedback — `partial`
- Corrected the wire contract from authenticated group traffic. Native
  post-recreation audio reports are 60-byte SR-only plaintexts with one RFC
  reception block plus an opaque eight-byte extension, protected to 74 bytes;
  they are not the 108-byte 1:1 SR+SDES plaintext previously reused here.
  The exact sanitized plaintext vector is pinned, while opaque extension
  calculation, empty-set shape, multi-report policy, and live acceptance remain
  explicit validation boundaries.
- Added the capture-backed contract for participant-indexed audio reception
  feedback. Each authenticated group SSRC retains independent sequence, loss,
  jitter, and sender-report timing state. The initial integration reused the
  verified 1:1 single-reception-block wire format per participant; the
  correction above replaces that historical behavior.
- Scaffolded the synchronized SSRC-indexed reception set and its two-stream KAT,
  then enabled the KAT when the stub bodies were implemented.
- Implemented one synchronized reception tracker per authenticated audio SSRC,
  deterministic report ordering, and exact sender-report routing. The
  two-participant KAT passes.
- Integrated the corrected 60-byte group SR into the live audio receive/ticker
  path. Every authoritative active audio SSRC produces one independently
  indexed 74-byte protected packet without SDES; departed SSRC state is pruned
  and a rejoin starts fresh. The unavailable opaque extension is zeroed, while
  the pre-audio baseline retains the verified 1:1 report as an explicit
  empty-set assumption.
- Made periodic report failures observable and retryable. First-send and partial
  failures report sent progress, consume no reused SRTCP indexes, and retry all
  active reports with fresh indexes on the next tick. Exact plaintext, protected
  length, leave/rejoin, retry, nil-input, and ticker-continuation KATs pass.

### web/initial_group_call — `partial`
- Added the capture-backed web-console contract for one audio-only multi-person
  start through `Client.GroupCallWithOptions`, separate established-call
  participant invitations, and incoming roster replay before Answer-driven
  connecting state.
- Implemented a distinct `start_group_audio` controller/HTTP/page path with
  trimmed alias-deduplicated targets, two-person validation, exactly one
  empty-group-JID API delegation, call ownership reservation, attach rollback,
  and a separate group-dialing lifecycle state.
- Gated established-call participant invitations on `CallPhaseActive` before
  recording pending outcomes, and gated the two People controls in the browser
  so group start is idle-only while Add people is ready/active-only.
- Added deterministic incoming roster replay ordering through the controller's
  SSE bridge before Answer-driven connecting state without synthesizing ready.
  Focused and full nested web KATs pass. Live group audio E2E remains pending.

### api/initial_group_call — `partial`
- Added the Task 1 capture-pinned Meowcaller API contract for an audio-only
  preselected group start, a selected-only public roster seed, stable public
  peer identity, and authoritative connected-device media readiness.
- Implemented `GroupCall` and `GroupCallWithOptions` with ordered exact
  normalization/deduplication, strict optional group-JID parsing, and one-shot
  delegation to Whatsmeow while leaving PN-to-LID resolution authoritative
  there.
- Added explicit group-scoped engine state, a transaction-zero selected-only
  public seed, cloned pre-callback invite snapshots, stable public peer
  handling, connected-device media derivation, and deterministic one-shot
  roster-then-key queue activation. Synchronous pre-return roster/key events
  attach to the returned call without losing their authoritative state.
- Review follow-up adds bounded and pointer-safe placeholder cleanup with
  owned-key zeroing, deep-cloned retained relay credentials, deferred
  pre-attachment readiness, serialized backlog activation, and strict
  PN/LID-only explicit group targets.
- Re-review hardens activation around in-flight roster rejection and recovery,
  rejects empty call IDs before placeholder allocation, and separates
  engine-owned from media-goroutine-owned credential lifetimes so every cloned
  call/relay secret is zeroed at its final use.
- Final review adds exact-match public roster recovery: a media-accepted lower
  transaction may replace only the precise pre-published transaction that media
  rejected, while any concurrent different or newer public state stays intact.
- Focused review/compatibility KATs, full tests, the full race suite, vet, and
  diff checks pass. CodeRabbit review was unavailable because its CLI is not
  installed. Live WhatsApp group audio E2E remains pending.

### voip/initial_group_call — `partial`
- Added the immutable capture contract for one initial preselected group offer:
  call-scoped routing, ordered self-plus-selected roster, Opus 8/16 kHz,
  network medium 3, local-only capability, and optional group JID.
- Recorded the transaction-11 self-only/outgoing gate and transaction-21 first
  connected-remote-PID plus group-relay readiness boundary. The module must
  preserve direct-call and singular active-invite behavior, install no state on
  discovery/send failure, and emit media readiness at most once after a group
  key epoch exists.
- Whatsmeow commit `fe7e4ad` adds the capture-shaped low-level builder and ACK
  parser, public ordered multi-target start API, immediately call-scoped keyless
  state, transaction-monotonic group relay readiness, and cloned optional group
  snapshots on `CallOffer`.
- Focused capture/orchestration/readiness/compatibility KATs, full tests, the
  full race suite, and vet pass. The live WhatsApp offer-to-ACK-to-rekey-to-media
  transition remains explicitly unvalidated. CodeRabbit review was unavailable
  because its CLI is not installed.

### web/group_call_outcomes — `KAT-verified`
- Added the web-test contract for rendering every authoritative roster
  transaction and correlating a successful invite submission to a one-shot
  `participant_join` only after WhatsApp reports a connected PID-bearing device.
- Intermediate invited/outgoing/receipt states remain visible without being
  mislabeled as joined; PID zero is explicitly valid.
- Implemented transient `group_state` and one-shot `participant_join` events,
  successful-target tracking, PN/LID correlation, deterministic selected-device
  reporting, and a live roster status line in the page. Failed submissions are
  removed from correlation and group transients do not replace lifecycle replay.
- Hardened the controller against stale old-call callbacks, duplicate normalized
  targets, PN/LID alias double joins, failed-answer busy state, and SSE
  reconnects that previously lost the latest authoritative roster.
- Staged PID-bearing joins that race synchronously with an in-flight invite
  result, publishing them only after success and discarding them after failure
  so the console cannot report both a failed invite and a false join.
- Implemented call-scoped in-flight/candidate tracking and covered synchronous
  success and failure ordering with focused controller KATs.
- Specified exact-call result ownership, serialized overlapping submissions, and
  atomic roster publication so stale callbacks cannot mutate or repopulate a
  replacement call.
- Implemented those ownership gates with a serialized submission boundary,
  pointer-safe result handling, and lifecycle-atomic roster/join publication;
  deterministic old-call, overlap, and blocked-publication KATs pass.
  Focused/full tests, race, build, and vet pass. CodeRabbit review was unavailable
  because its CLI is not installed.

### api/group_call_state — `KAT-verified`
- Added the capture-backed public contract for replayable, sanitized group roster
  transactions. Only `connected` participants with a PID-bearing selected device
  prove a join; invited/outgoing/receipt remain intermediate states.
- Relay credentials, key material, ciphertext, identity blobs, and raw
  capabilities are deliberately excluded from the public view.
- Implemented `Call.GroupState` and replayable `Call.OnGroupState` with deep-copy
  isolation. The engine publishes accepted snapshots before or after media
  startup while suppressing stale, rejected, and ended-call updates. Focused/full
  tests, race, build, and vet pass. CodeRabbit review was unavailable because its
  CLI is not installed.

### voip/group_key_epoch_fanout — `KAT-verified`
- Added the two-sided-capture contract for generating one shared 32-byte group
  epoch when nominated by `rekey="1"`, Signal-encrypting it independently to
  every other connected PID-bearing device, sending one direct captured-shape
  `enc_rekey` per recipient, and handing the same root to local media.
- Added the missing added-participant boundary: a decrypted inbound epoch
  installs the key on the keyless active-call invite and triggers its first
  media-ready event; existing active calls rotate in place without a second
  media-ready event.
- Whatsmeow commit `9e3da89` adds the exact direct wire builder, deterministic
  connected-device selection, one-root Signal fan-out, delayed local handoff,
  inbound/local epoch state installation, and the added participant's
  media-ready transition. Stale and identical epochs are ignored, conflicts are
  rejected, and failed fan-out never rotates local media. Focused/full tests,
  full race, build, and vet pass; live WhatsApp E2E remains pending. CodeRabbit
  review was unavailable because its CLI is not installed.
- Whatsmeow follow-up `b2f7cf5` recursively replaces binary node bodies with
  byte-length markers at the send-log boundary, preventing group rekey
  ciphertext and other binary secrets from entering debug output.

### media/group_key_epoch — `KAT-verified`
- Added the corrective two-sided-capture contract for one transaction-wide raw
  media epoch. The elected `enc_rekey` author distributes one shared root; local
  send keys derive with the self device ID and every receive key derives with
  that sender's device ID.
- Required in-place, concurrency-safe installation preserves RTP stream identity,
  sequence/timestamp counters, sender ROC, and receiver ROC. The older
  participant-scoped module is retained as superseded history.
- Implemented concurrency-safe raw-epoch installation on both media directions.
  Focused KATs prove new-key authentication, old-key rejection, unchanged SSRC,
  continued sequence/timestamp state, and receive ROC continuity across
  `0xffff → 0`.
- Replaced the superseded author-only registry path with one transaction-wide
  epoch installed into the live sender and every active receiver. The registry
  buffers future/pre-roster epochs, carries the current epoch to new roster
  members, rejects conflicts and malformed roots without partial mutation,
  ignores stale epochs, and clears queued key material at call end. Focused/full
  tests, race, build, and vet pass; live WhatsApp E2E remains pending.
  CodeRabbit review was unavailable because its CLI is not installed.
- Extended the same accepted epoch through the SRTCP key schedule. Audio and
  video sender reports rotate in place without resetting their SRTCP index or
  CNAME, late-attached senders inherit the current epoch, and incoming control
  packets exact-route to active participants across all nine deterministic
  relay-stream SSRCs. Video and app-data RTP now rotate with the same epoch,
  exact-route through independent per-participant contexts, and preserve
  participant-specific reassembly/deduplication state. Focused SRTCP derivation,
  all-stream send/receive, direct fallback, roster carry/departure, unknown
  SSRC, late-attachment, and concurrent-rekey KATs pass.

### voip/group_invite_accept — `KAT-verified`
- Added the capture contract for accepting a directed invitation into an
  already-active ad-hoc call. The enriched offer seeds transaction-ordered group
  state before eager preaccept, and both preaccept and active-group accept use
  `CALLID@call`; ordinary 1:1 offers remain direct.
- Corrected the accept timing from the immutable capture: the added endpoint
  invokes `acceptCall` and immediately sends exactly `audio`, `net`, and
  `encopt`, with no direct-call metadata. Its first inbound `mute_v2` arrives
  later and cannot trigger acceptance; ordinary 1:1 deferred acceptance remains
  unchanged.
- Corrected the contract to preserve the capture's missing-key boundary: the
  enriched offer has no encrypted 1:1 call key, so it registers pending the
  selected endpoint's later group rekey and cannot emit media-ready early.
- Pinned the active-group Answer path to the captured immediate accept ordering
  and exact child set, with retryable/coalesced state transitions; ordinary 1:1
  acceptance remains deferred.
- Coalesced callers now wait for the shared wire attempt and receive its exact
  result, preventing an overlapping Answer from reporting success when the sole
  accept send failed.
- Whatsmeow commit `81ff60c` parses and validates the enriched offer snapshot,
  registers the keyless active ad-hoc invite, preserves an installed key across
  retransmissions, and targets both eager preaccept and deferred accept at
  `CALLID@call`. Ordinary keyless 1:1 offers remain rejected. Focused/full tests,
  race, build, and vet pass; CodeRabbit was unavailable because its CLI is not
  installed.
- Whatsmeow follow-up `50f40b0` applies the same call-scoped target to mute,
  video-state, and hangup controls while preserving direct 1:1 routing.

### voip/group_rekey_directive — `KAT-verified`
- Added the immutable two-sided capture contract for preserving the
  endpoint-personalized `group_info rekey="1"` nomination on the typed group
  snapshot. Key generation, recipient fan-out, retries, and media send-key
  installation remain separate modules.
- Whatsmeow commit `7c95b5a` adds `RekeyRequested` to the typed snapshot and
  parses only the captured literal `"1"` as true. The absent and nominated
  endpoint KATs, full tests, focused race, build, and vet pass. CodeRabbit review
  was attempted but unavailable because its CLI is not installed.

### media/group_relay_refresh — `partial`
- Added the capture-pinned contract and implementation for rotating group relay
  credentials over the existing active DataChannel while preserving RTP and
  stream identity.
- Refined the contract so the refreshed Allocate packet, committed transaction,
  and inferred binding-response integrity key advance together only after the
  immediate relay send succeeds; a failed send leaves the prior credentials
  retryable.
- The critical group update now selects the active relay's rotated token,
  rebuilds and immediately sends Allocate under the group relay key, and commits
  the one-second keepalive packet and binding-response key only after that send
  succeeds. Failed sends retain the prior credentials and remain retryable.
  The capture contains no post-rotation binding-success packet, so the
  binding-response key remains an explicit protocol inference pending live E2E.
- Serialized keepalive and binding-response build/send operations with relay
  refresh so a control packet using old credentials cannot leave after a rotated
  Apply commits. Deterministic blocked-send KATs cover both orderings; the
  binding-key choice remains synthetic and the module remains partial.

### media/group_enc_rekey — `partial`
- Added the capture-authoritative participant rekey state machine: transaction-
  ordered buffering, delayed per-author epochs, exact-device/unique-user author
  resolution, per-receiver raw-key installation, duplicate protection, and
  departure pruning. The target ROC reset remains explicitly unvalidated for an
  already-active rollover.
- Wired typed Whatsmeow rekeys through the engine's pre-media queue into the
  participant registry. Synthetic RTP KATs prove wrong-key rejection before
  install, authentication after install, send-key preservation, delayed rekey
  handling, exact author targeting, sole-remote rejection, other-participant ROC
  and decoder continuity, and key cleanup when a call ends. Live group audio
  remains the end-to-end gate.

### web/group_participant_invite — `KAT-verified`
- Added the capture-authoritative browser-console envelope for a multi-target
  control request and one transient submitted/failed result event per singular
  invite.
- Implemented `add_participants` request decoding, active/non-empty validation,
  ordered plural delegation, and one transient success/failure event per target
  without replacing replayed lifecycle state.
- Added the comma/newline people picker, lifecycle-header protection, and
  explicit audio-only “submitted, not joined” guidance in both the page and
  README. Focused/full example tests, race, build, vet, and manual diff review
  pass. CodeRabbit was invoked twice but blocked by its free CLI rate limit.

### api/group_participant_invite — `KAT-verified`
- Added the capture-authoritative envelope for singular
  `Call.AddParticipant(ctx, target)` delegation and an ordered plural
  convenience loop. The web example will retain one independent singular
  signaling result per selected person.
- Added the engine injection point, public singular/plural method stubs, and
  three skipped KATs for exact delegation, validation/error preservation, and
  ordered all-target results. Existing tests, build, and vet remain clean.
- Implemented the engine adapter with active-call validation, shared target
  parsing, context-preserving singular Whatsmeow delegation, and wrapped
  failures. Focused/full tests, race, build, and vet pass; CodeRabbit's one test
  hardening finding was applied.
- Implemented `Call.AddParticipant` as the context-preserving singular façade.
  Its validation/delegation KATs, full tests, race, build, vet, and CodeRabbit
  pass; only the plural convenience stub remains.
- Implemented `Call.AddParticipants` as an ordered all-target loop with
  index-aligned errors. All module KATs now run and pass; full tests, focused
  race, build, vet, and CodeRabbit are clean.
- Updated the public `Call` description to state that a direct call may become
  an ad-hoc group call without claiming group media support.

### voip/group_participant_invite — `partial`
- Added the capture-authoritative datasheet for one active-call participant
  invitation. The proposed control path captures the established direct-call
  device capabilities, switches to the latest server roster after group
  conversion, resolves and discovers one target, builds one verified singular
  offer, stamps one stanza ID, and sends once.
- The module is intentionally audio-only and does not invent the unresolved
  companion-session preparation, optimistic membership, relay, media, or rekey
  behavior.
- Whatsmeow commit `0b82057` adds the call-state fields, four approved function
  envelopes, two sanitized capture cases for capability and roster behavior,
  and an offline send-boundary KAT. All three KATs are skipped on explicit
  stubs.
- Whatsmeow commit `f4b6c54` implements active-device capability parsing with
  cloned bytes and passes both captured offer/preaccept cases plus malformed
  wire validation. Roster and send KATs remain skipped on their stubs;
  CodeRabbit, full tests, focused race tests, build, and vet are clean.
- Whatsmeow commit `35d2eb6` atomically attaches the parsed peer device to an
  existing call and passes owned-byte, unknown-call, and focused race tests.
  Roster and send KATs remain skipped on their stubs; CodeRabbit is clean.
- Whatsmeow commit `78a9421` deep-copies either the connected direct pair or
  latest canonical group snapshot and passes both capture cases, invalid-state
  cases, and focused race tests. The singular send KAT remains skipped on its
  stub; CodeRabbit is clean.
- Whatsmeow commit `ba1fb72` implements one-target resolution, device discovery,
  duplicate rejection, verified offer construction, independent stanza ID
  stamping, and one send. All current unit KATs run and pass, including the
  offline send boundary with no optimistic mutation; live lifecycle wiring
  remains pending. CodeRabbit, full tests, race tests, build, and vet are clean.
- Whatsmeow commit `b5f9d94` seeds outgoing call state with an owned clone of
  the exact local active-device capability advertised in the initial offer.
  Ownership, full tests, race tests, build, vet, and CodeRabbit are clean; peer
  and connected lifecycle wiring remain pending.
- Whatsmeow commit `b75442b` captures the selected outgoing peer device and
  capability through the existing preaccept dispatcher, preserving normal event
  delivery and using sanitized warnings on malformed input. Integration, race,
  full, build, vet, and CodeRabbit checks are clean; incoming seeding and the
  connected gate remain pending.
- Whatsmeow commit `3f55d2d` seeds both local and peer active devices from an
  incoming offer while preserving existing offer/retransmit behavior. Tests,
  race, build, and vet are clean. CodeRabbit's claimed missing voip helpers were
  verified as a false positive: all six definitions exist and both packages
  compile and pass. Only the connected gate remains pending.
- Whatsmeow commit `3355bf6` opens the participant-invite gate when an outgoing
  call receives `accept`, while preserving selected-peer routing and normal
  event dispatch. Focused and full tests, race, build, vet, and CodeRabbit are
  clean.
- Whatsmeow commit `7dc1db1` opens the incoming gate only after the deferred
  `accept` send succeeds and only while the same call state remains registered.
  Unit coverage proves the identity guard and failed-send behavior; full tests,
  race, build, vet, and CodeRabbit are clean. The successful live send boundary
  remains explicitly marked for end-to-end validation.

### voip/group_invite_offer — `KAT-verified`
- Added the capture-authoritative datasheet for the singular active-call invite
  offer. The two-person picker capture proves that each selected invitee gets an
  independent offer and transaction track; the proposed low-level builder
  therefore accepts one bare-LID target, that target's device fan-out, and the
  existing ordered roster.
- The proposed wire form is deliberately separate from the 1:1 offer builder:
  Opus/16000, network medium 2, unencrypted destination list, and `group_info`,
  with no privacy, call-key ciphertext, `encopt`, device identity, group JID, or
  top-level capability.
- Whatsmeow commit `7aba50a` adds the approved value-parameter envelope, an
  explicit builder stub, and two sanitized capture cases covering connected and
  absent participant state.
- Whatsmeow commit `f39eba1` implements the singular builder, removes the KAT
  skip, and passes both capture cases plus missing call ID, target, creator,
  target-device, and participant-roster validation cases. Focused race tests,
  full tests, build, vet, and CodeRabbit review are clean.

### voip/group_call_state — `planned`
- Added the capture-authoritative datasheet for transaction-ordered group roster,
  relay, and `CALLID@call` routing state. The proposed envelope stores one parsed
  snapshot, preserves direct routing until group state exists, and refuses to
  recreate a call for late post-terminate updates. No production code changed.

### voip/group_call_state — `scaffolded`
- Added whatsmeow commit `507b4bd`: the internal full-snapshot state envelope,
  `applyGroupUpdate` and `signalingTarget` three-line stubs, and a six-case
  capture-derived vector covering direct routing, transaction gaps, ad-hoc
  upgrades, pending participants without PIDs, stale updates, and late
  post-terminate delivery. `TestGroupCallStateCorpus` is intentionally skipped
  until both state bodies are implemented; build, vet, and all other tests pass.

### voip/group_call_state — `partial`
- Added whatsmeow commit `63dc174`: `applyGroupUpdate` now holds the call-state
  lock, refuses missing calls and equal/older transactions, and atomically stores
  the complete newer snapshot. `TestApplyGroupUpdateCorpus` runs and passes all
  six capture-derived cases. The separate `signalingTarget` KAT remains skipped
  on its scaffolded body. Build, vet, and the full suite pass; CodeRabbit reported
  no findings when based on the local scaffold commit.

### voip/group_call_state — `KAT-verified`
- Added whatsmeow commit `5a6350b`: `signalingTarget` preserves participant
  routing for direct calls and switches to `CALLID@call` after an authoritative
  group snapshot is accepted, including ad-hoc groups with no group JID.
  `TestApplyGroupUpdateCorpus` and `TestGroupCallSignalingTargetCorpus` both run
  and pass. Build, vet, the full suite, and the focused race-enabled tests pass;
  CodeRabbit reported no findings. The scaffold-to-final diff contains only the
  two reviewed function bodies and their KAT split.

### voip/group_update_ingest — `planned`
- Restored the minimal datasheet template and group-call module registry for the
  capture-driven build. The human reviewer approved the immutable capture corpus
  as authoritative; the datasheet pins four raw JSONL boundaries by SHA-256 and
  proposes the typed group-update event/dispatcher envelope for review. No
  production code changed.

### voip/group_update_ingest — `scaffolded`
- Added whatsmeow commit `285aa8b`: the `CallGroupUpdate` event envelope,
  three-line `onCallGroupUpdate` handler stub, and four-case sanitized capture
  vector. `TestGroupUpdateIngestionCorpus` is intentionally skipped until the
  handler body is implemented; build, vet, and all other tests pass. The live
  dispatcher is not wired while the handler remains a stub, preserving existing
  behavior.

### voip/group_update_ingest — `scaffolded`
- Reconciled the datasheet with the now-verified group-state module. The proposed
  handler parses each update, delegates monotonic acceptance to
  `applyGroupUpdate`, dispatches only accepted typed snapshots, and keeps
  deferred ACK behavior for accepted, stale, late, and malformed deliveries.
  Corrected the registry dependency so ingestion depends on group state. No
  production code changed.

### voip/group_update_ingest — `KAT-verified`
- Added whatsmeow commit `f676cf1`: `group_update` now routes through the call
  dispatcher, parses into the typed snapshot, delegates monotonic acceptance to
  `applyGroupUpdate`, and emits `CallGroupUpdate` only for accepted state.
  Duplicate and post-terminate deliveries remain ACKed but suppressed. Malformed
  input logs sanitized metadata, emits `UnknownCallEvent`, leaves state
  untouched, and remains ACKed.
- `TestGroupUpdateIngestionCorpus` runs all four capture-derived fixtures and
  validates identity, group metadata, ordered participant/device/PID state,
  event payload, state storage, duplicate suppression, late-update suppression,
  and ACK attempts. The malformed fallback test also passes. Build, vet, full
  tests, and focused race tests pass.
- CodeRabbit was run twice. Its findings were rejected as review-context false
  positives: every allegedly missing group type and parser helper exists in the
  selected local base, and the compile/test gates exercise them successfully.
  The scaffold-to-final diff matches the reviewed handler and dispatcher scope.

### meowcaller — use whatsmeow's first-class call API
- Whatsmeow now owns 1:1 call signaling, call-key exchange, relay election, mute events,
  and independent video-flow transitions. Meowcaller consumes the typed handoff events
  and remains responsible for RTP/SRTP, MLow, H.264 framing, reactions, and diagnostics.
- Removed the duplicated `signaling` package and all raw-node/unsafe ack interception.
- Moved the QR-pairing browser call console into the standalone `examples/web` module.

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
- Added receive-side WARP MESSAGE-INTEGRITY verification with constant-time tag
  comparison. The existing byte-exact tag KAT now also rejects a changed
  participant key, tag, ROC, empty tag, and oversized tag. **KAT-verified.**
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

### session/authenticated-receive — KAT-verified (reference `2f001b5a3d6374cc5cf7177792c2a81f87a54080`)
- Split receive ROC handling into pure estimate and authenticated commit operations.
  The reference staircase KAT proves unauthenticated packets cannot advance the
  receiver rollover counter. `MediaPipeline.UnprotectAudio` now verifies the
  configured-length WARP MI tag before committing ROC or decrypting; wrong
  participant keys and changed tags are rejected, and the next valid packet remains
  receivable. **KAT-verified.**

### media/group-receive — KAT-verified
- Added the capture-pinned envelope and implementation for connected-device/PID
  activation, deterministic primary-audio SSRC routing, per-participant
  authenticated receive/ROC/decoder state, original-peer PID 0 continuity,
  participant departure pruning, and identity-labeled decoded frames. Meowcaller
  now consumes and retains `CallGroupUpdate` without replacing the public original
  peer. Rejected snapshots leave receiver metadata and transaction ordering
  unchanged, and pending snapshots enter the engine cache only after successful
  application. Focused synthetic composition KATs pass. **KAT-verified.**
- Preserve the authenticated direct receiver across transitional group snapshots
  that contain no connected remote PID-bearing devices. A local/self PID alone no
  longer suppresses that fallback. The transaction still advances, and the first
  actionable remote PID roster promotes the existing peer. The focused KAT targets
  the live add-to-call interruption condition by authenticating direct-peer RTP
  before and after a self-only-PID snapshot; live add-to-call E2E remains pending.
- Linked the self-exclusion and remote-PID readiness branch to the pinned capture
  contract. No runtime behavior changed.

### media/group-audio-mixer — partial
- Implemented bounded participant queues, independent two-frame prefill, 10 ms mix
  ticks, hard clipping, roster-gated departure cleanup, and single-speaker gain
  preservation. The deterministic composition KATs pass. The media loop now clocks
  mixed chunks into the existing sink while participant decoding remains
  independent after a group roster arrives; direct 1:1 calls retain the existing
  timestamp-aligned playout path through invite-only updates, drains buffered direct
  PCM when a second remote connects, and reframes internal 10 ms mix ticks into the
  public 960-sample sink contract. Live multi-speaker playout remains E2E unvalidated.

### voip/group-enc-rekey-ingest — partial
- Added the capture-pinned signaling datasheet for typed keygen-v2 `enc_rekey`
  parsing, existing Signal DM decryption reuse, 32-byte raw-key dispatch, delayed
  transaction handling, and sanitized failure behavior. Live call
  `D66652FC17BF1F8BBA898DE097B428FA` corroborated this as the next authentication
  boundary.
- Whatsmeow now validates and clones the capture-shaped envelope, reuses the exact
  outer-author Signal session for `msg`/`pkmsg`, decodes the decrypted
  `waE2E.Message`, validates its 32-byte `Call.callKey`, dispatches
  `CallEncRekey`, and preserves deferred ACKs on failure. Parser,
  malformed-envelope, cloning, metadata-only logging, and failure-router KATs pass;
  live Signal decryption of the 79-byte application envelope is observed, while a
  post-fix authenticated group-audio retest remains pending.

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
