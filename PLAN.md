# meowcaller — engineering plan

A **production-grade, pure-Go** library for WhatsApp 1:1 calls: signaling, keying,
media transport, and the MLow audio codec. Held to the same quality bar as
[whatsmeow](https://github.com/tulir/whatsmeow), whose structure and idioms it
mostly follows. This is not a proof of concept; it is a keystone library that
other software will depend on.

This file is the **map**. The companion documents are:

- [`AGENTS.md`](AGENTS.md) — how implementation proceeds (human-audited,
  module-by-module, agents scaffold then pause). **Read before writing any code.**
- [`MODULES.md`](MODULES.md) — the module registry and the index of per-module
  **datasheets** under [`datasheets/`](datasheets/). Each datasheet is the single
  reference for what a module is, where it comes from, and what it is validated
  against.
- [`CHANGELOG.md`](CHANGELOG.md) — every merged change, tracked.

---

## Mission and standard

Build a calling library that an enterprise can depend on:

- **Correct by construction.** Every byte-level behavior is verified against a
  known-answer vector (KAT) shared with the reference implementation. A module is
  not "done" until its vector passes. A passing round-trip is not enough; it must
  match real, captured data.
- **Auditable.** Every module has a datasheet stating its purpose, its source of
  truth, its inputs/outputs, its constants, and exactly what it was validated
  against. Any contributor — human or agent — can answer "what is this and how do
  we know it's right?" from the datasheet alone.
- **Human-directed.** The flow of logic and the engineering decisions are made by
  a human reviewer, in real time, one module at a time. Agents scaffold and
  explain; they do not decide. See [`AGENTS.md`](AGENTS.md).
- **Clean.** Idiomatic Go, whatsmeow-style. Comments only where they earn their
  place (a `TODO`, a stated assumption, or context that would otherwise be lost).

---

## The model: spec, reference, derivative

You need no prior knowledge of this project to work from this plan. Three things,
kept distinct:

- **The spec.** An abstract, implementation-independent description of the
  protocol — think of it as the RFC. It lives in a separate documentation repo
  (called *wacrg*). A datasheet may link to it for background, but a datasheet is
  self-contained for implementation; you do not need the spec repo to build a
  module.
- **The reference.** An existing, validated implementation of that protocol (in
  Rust), with checked-in **test vectors** that pin its behavior byte-for-byte. Its
  source and its vectors are the ground truth. A datasheet contains the relevant
  reference source **verbatim** so you read the real code, never a paraphrase.
- **meowcaller (this repo).** A clean-room, independent **Go** implementation of
  the same protocol. It is its own program: the reference library is **never
  named, imported, or alluded to in meowcaller's Go code**.

A **datasheet** (`datasheets/<module>.md`) is the only thing you read to build a
module. It contains exactly three parts: the **reference source verbatim**, the
**Go envelope** (signatures, no bodies), and **implementation suggestions**. The
verbatim source is authoritative; the suggestions are guidance, not proof. There
is no behavioral summary to drift from — you implement from the real source.

Do **not** copy from any earlier, unvalidated Go attempt at this protocol; the
validated reference and its vectors are the only source.

---

## Repository structure (mirrors whatsmeow)

```
meowcaller/
  client.go  call.go  offer.go  accept.go  ...   package meowcaller — call control
  mlow/         the MLow/SMPL CELP audio codec (own package)
  srtp/         E2E + HBH SRTP, SFrame, WARP (media keying + protection)
  rtp/          RTP / RTCP / WARP framing
  stun/         relay STUN dialect
  relay/        DTLS / SCTP media loop (pion, pure Go)
  signaling/    the <call> stanza builders/parsers
  types/        shared types and types/events/
  util/         small primitives (hkdfutil, ...) — whatsmeow-style
  internal/kat/ shared test-vector loader
  datasheets/   per-module datasheets
```

Pure-Go dependencies only, and minimal. The **codec** (`mlow/`) and the keying/
framing layers stay on `google.golang.org/protobuf` alone (the protobuf runtime, as
whatsmeow itself uses) for decoding the embedded table blobs, which share a `.proto`
schema with the reference. The **signaling and transport layers** additionally use:

- `go.mau.fi/whatsmeow` — its `binary` (WABinary `Node`) and `types` (`JID`)
  packages for the call-control stanzas, rather than re-implementing the node tree.
- `github.com/pion/*` (`pion/dtls`, `pion/sctp`, `pion/datachannel`, …) for the
  relay media transport (UDP→DTLS→SCTP→DataChannel).
- possibly `libsignal-protocol-go` later, if the offer/accept key path needs it.

These were adopted by explicit human decision; no other third-party dependencies are
added without one. Every `.go` file carries the project license header.

---

## Module registry (the spine)

Work proceeds **module by module**, in dependency order — not in phases. Each
module is a discrete, human-approved unit with its own datasheet, scaffold, and
verification. The registry and live status live in [`MODULES.md`](MODULES.md); the
order of attack (1:1 only):

```
codec foundation:   rangecoder → toc → mem(heap ROM)
codec receive DSP:   lpc → lsf → lsf_quant → pitch → pulse → gains → synth
                     → postfilter → vad → noise → red → decoder(e2e)
codec send DSP:      encoder(+analysis, signal-mode)
keying:              util/hkdf → srtp/e2e → srtp/hbh → srtp/sframe → srtp/warp
signaling:           signaling/stanza
transport:           stun → rtp → ssrc → relay
orchestration:       session/pipeline → client/call (+ types/events)
```

The first audible milestone is `decoder` decoding the real
`inbound_capture_frames.json` to PCM that matches `e2e_vectors.json`. Everything
before it is in service of that.

---

## Conventions (enforced)

- **Commits:** one module change per commit, subject `(<module>: <change>)` —
  e.g. `(mlow/toc: scaffold smpl TOC parser)`, `(srtp/sframe: implement GCM
  seal)`. Each commit updates [`CHANGELOG.md`](CHANGELOG.md).
- **Changelog:** every merged change recorded under the module, with its
  validation state (`scaffolded` / `implemented` / `KAT-verified`).
- **Clean Go, no reference leakage:** the Go code (and its comments) **never**
  names or alludes to the reference library. It reads as an original Go
  implementation. The only outward link permitted in code is a plain URL to a
  wacrg page or a wacrg decision artifact, where a pointer genuinely helps.
- **Comments:** only `// TODO(...)`, `// ASSUMPTION: ...`, or a short note that
  preserves context a future reader would otherwise lose. No narration of what the
  code plainly does, and no explaining *why* in comments — the why is spoken in
  the conversation. Doc comments on exported identifiers per Go convention.
- **Datasheets:** carry the reference source **verbatim** and the Go target.
  Written one module at a time, when we reach it — not bulk-generated.
- **Decision artifacts (ADRs):** when a conversation over an implementation detail
  concludes, a timestamped record of the decision and its rationale is written to
  **wacrg** — **only on direction**, never autonomously. It may be linked from the
  Go file by URL.
- **Tests:** every module ships a KAT test loading the reference vector. `go test
  ./...` is green at every committed step.

The detailed working protocol — how an agent scaffolds, where it stops, and how
the human directs — is [`AGENTS.md`](AGENTS.md).
