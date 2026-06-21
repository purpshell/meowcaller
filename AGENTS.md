# AGENTS.md — how meowcaller gets built

This library is built **module by module, under human audit, in real time**. The
human reviewer directs the engineering; agents prepare and explain. This file is
the working protocol. It is binding.

Unfamiliar with a term or acronym (TOC, KAT, SID, VAD, LTP, NLSF, PVQ, SRTP,
SFrame, WARP, …)? See [`GLOSSARY.md`](GLOSSARY.md).

> If you are an agent reading this: you are not autonomous here. You scaffold and
> explain; the human decides how logic flows and what trade-offs are made. When in
> doubt, stop and ask. A wrong guess committed quietly is the worst outcome.

**Handoffs start here.** Every handoff or session-kickoff instruction — human-to-agent
or agent-to-agent — **must name this file (`AGENTS.md`) as its starting point**: read
it first and follow the build loop below before doing anything else. A handoff that
does not point at `AGENTS.md` is incomplete; treat reading it as the implicit first
step regardless.

## The prime directives

1. **Do not decide logic. Scaffold it.** When you reach a function whose behavior
   involves a real engineering choice (an algorithm, a data layout, an
   error-handling strategy, a buffering decision), write the **signature, the doc
   comment, and the datasheet reference**, leave the body as the three-line stub
   block (`// TODO` / `// agent suggestion:` / `// human input:`), and **stop for
   the human**. Do not fill it in to "keep moving."
2. **One module at a time. Take the break.** Finish a single, reviewable unit —
   often just a scaffold, or one function the human approved — then pause for
   review and approval before continuing. No multi-module sprints. The human is
   watching this get built and will say when to proceed.
3. **Explain the why in the conversation, never in the code.** As you scaffold
   each thing, say in the chat — to the human, in plain language — *why* it exists
   and what it does, so the human understands what is being built **without
   reading any code comment**. The backing detail (Rust source, constants,
   validation) lives in the datasheet you read; you speak the relevant part aloud.
   The human should never have to read a comment to follow the reasoning. Code
   carries logic; the conversation carries the why.
4. **Verify against vectors, never vibes.** A behavior is correct only when its
   KAT passes. Reverse-engineered names and analysis notes are frequently wrong;
   the vector is the proof. If a module has no vector, that is a decision point
   for the human, not a license to guess.
5. **Scaffold every prerequisite; implement only the asked module.** When the
   module you are building references another module that is not built yet (a type
   it embeds, a function it calls), **scaffold that prerequisite first** — its
   envelope and stubs, per the Scaffolding standard — so the current module
   compiles and its cross-module calls resolve against the real surface. Scaffolding
   a prerequisite is **not** implementing it: leave its bodies as three-line stubs,
   do not fill them, do not KAT it. Only the module the human asked for gets
   implemented. This is not a multi-module sprint (directive #2 still holds — you
   implement exactly one); it just lets a dependent module stand on the real
   signatures of its dependencies instead of being blocked or faked. Each scaffolded
   prerequisite is its own `scaffolded` registry entry and commit.

## The build loop (per module)

```
1. READ      datasheets/<module>.md — three parts: the reference source VERBATIM
             (the authoritative ground truth), the Go envelope (signatures), and
             implementation suggestions. Implement from the verbatim source; the
             suggestions are guidance, not proof.
2. SCAFFOLD  create the package file: types, exported signatures, doc comments,
             the KAT test wired to the (copied) vector — but function bodies are
             three-line stub blocks (`// TODO` / `// agent suggestion:` /
             `// human input:`).  → COMMIT, PAUSE.
3. DIRECT    the human reviews the scaffold and decides how each body should work
             (or approves translating the embedded reference 1:1 into Go). One
             function at a time.
4. IMPLEMENT the approved function(s) only, as clean Go that ports the reference
             (with the `// Source of truth:` line; never imports/copies it). Keep
             the KAT test running. Before shipping the commit, run a quick review
             with `/code-review` (the CodeRabbit CLI/skill) and address what it
             surfaces.  → COMMIT per fn, PAUSE.
5. VERIFY    when the module's body is complete, its KAT must pass. Then run
             `git diff <scaffold-commit>..<impl-commit>` and confirm the actual
             change matches what you told the human you changed — same functions,
             same approach, no silent extras. If the diff and the narration
             disagree, the narration was wrong: say so and reconcile before
             moving on. Update the datasheet status and CHANGELOG.  → COMMIT, PAUSE.
```

### Prerequisite KATs (gating + the pre-commit check)

A KAT cannot pass while a function it transitively exercises is still a stub (a
scaffolded prerequisite, per directive #5) or a `// NOT VALIDATED:` body. Such a KAT
must be **skipped, not left red**: `t.Skip("blocked: <prerequisite/module> is a stub;
enable when implemented")`. Flag it; never fake a pass and never commit a knowingly
red suite.

**Run a prerequisite-validation check before every commit.** Walk every skipped KAT
and ask: *are all the prerequisites it depends on now implemented (real, non-stub
bodies)?* If yes, **enable that KAT** (remove the `t.Skip`) — it must now pass — and
**delete the `// NOT VALIDATED:` markers** on the functions it now covers. Implementing
a module thus re-activates the downstream KATs it unblocks; leaving a now-satisfiable
KAT skipped, or a now-covered marker in place, is a VERIFY failure. State in the chat
which KATs you enabled (or why each still-skipped one remains blocked) at each commit.

**Keep the MODULES.md Status column true to the code.** Status is not a wish — it is
the module's actual build state, derived from its KAT: `verified` only when the KAT
runs and passes (not `t.Skip`'d); `scaffolded` when the KAT is skipped; `partial`
when a module has both passing and skipped KATs; `implemented` for code with no KAT
yet; `planned` for no source. A curated parenthetical note may follow the word
(e.g. `verified (decode; estimator scaffolded)`). Whenever a commit changes a
module's build state, update its Status (and note) in the same commit — this is part
of VERIFY. A Status that disagrees with the code's KAT reality is a defect: the
reviewer (CodeRabbit) flags it and the agent fixes it. No external script or git
hook owns this — it is maintained in-tree, by hand, like every other fact in the
registry.

If the datasheet for the module does not exist yet, write it first (embed the
reference source verbatim + the Go target, per `datasheets/_TEMPLATE.md`), get it
reviewed, and only then scaffold. Datasheets are written one module at a time.

**Datasheets are always kept current with the pinned reference.** The embedded
verbatim is the ground truth, so it must match the reference commit the SOT
permalinks point at — never a stale snapshot. Whenever the reference is re-synced
(new commit pulled / new pin), re-verify every datasheet's verbatim against the
current source and refresh any that diverged, **before** building from it. A module
must never be implemented from a stale datasheet. When in doubt, re-diff the
embedded source against the current file and reconcile.

**Every datasheet records the reference commit its verbatim is synced to.** Carry a
`**Reference pinned at:** <full-40-char-SHA>` line in the datasheet header (just
before the `## Reference source` section). It is the honest sync state, not a wish:
stamp it only when the embedded verbatim actually matches the reference at that
commit (full-file or the cited excerpt). When you refresh a datasheet, update its
pin in the same commit; when a datasheet's source can't be located in the pinned
reference (renamed/removed/never-upstreamed), mark the pin `UNMAPPED` with a note
and stop — do not fabricate or guess the verbatim. This applies to every datasheet
from now on.

Each arrow `→ PAUSE` is a real stop: hand control back to the human. Do not chain
steps without approval.

## Scaffolding standard

A scaffold is a complete, compiling skeleton with no logic. The doc comment is a
normal, brief Go doc comment. The **first line of every function body** is a
`// Source of truth:` comment pointing at the reference symbol the function ports;
this is required, not optional (see Comment policy). Below it, every unfinished
body uses the **three-line stub block**:

```go
// SealFrame encrypts one media frame with the participant SFrame key.
func (s *Session) SealFrame(plaintext []byte) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/<sha>/wacore/src/voip/sframe.rs#L<start>-L<end>
	// TODO
	// agent suggestion: AES-GCM with a 16-byte LE-counter nonce; authenticate the
	// varint header as AAD.
	// human input:
	return nil, errNotImplemented
}
```

The three lines are a fixed protocol:

- **`// TODO`** — the marker that this body is unimplemented.
- **`// agent suggestion: <course of action>`** — your concrete proposed
  implementation for this body, in one or a few lines. State what you would do, not
  just what the open question is. This is your recommendation on the record.
- **`// human input:`** — left blank for the human. Whatever they write after the
  colon is their direction, and it **enters the implementation round**: when you
  implement the body you follow the human input if present, and your own agent
  suggestion only where they left it blank.

When you write this, you also say in the chat — not in the file — something like:
"I'm scaffolding `SealFrame` for the send path's per-frame encryption; it comes
from `sframe.rs`, it's AES-GCM, and my suggestion is the 16-byte LE-counter nonce
with the varint header as AAD." The chat carries the why; the stub carries the
suggestion and the slot for the human's answer.

The three stub lines are stripped when the body lands; the `// Source of truth:`
line **stays** — it is permanent provenance, not scaffolding.

It must `go build` and `go vet` cleanly. The KAT test exists and **fails** (or
skips with a clear reason) until the body lands — never a fake pass.

## Comment policy

Comments earn their place or they do not exist:

- **`// Source of truth: ...`** — required as the first line of every function
  body. It is a **GitHub permalink** to the exact reference lines the function
  ports, so a reader can jump straight to the ground truth. For now that is the
  Rust reference, pinned to the **full 40-character commit SHA** (never an
  abbreviated/short SHA) with the function's line range (a real, clickable example —
  this is the actual link in `rangecoder.go`):
  `https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/rangecoder.rs#L203-L226`.
  The path follows the **reference** tree, not the Go package tree: only the codec
  lives under `wacore/src/voip/mlow/`; srtp/signaling/transport sources
  (`sframe.rs`, `stanza.rs`, `stun.rs`, `rtp.rs`, …) sit directly under
  `wacore/src/voip/`. Locate the real file before linking — do not assume `mlow/`.
  A function's header line pins the **commit that introduced the body** (its impl
  commit) at the function's line range — not the current reference tip. When you
  later **modify** that function to port a change from a newer reference commit,
  **leave the header on the original impl commit** and add a per-change
  `// Source of truth:` line directly above each changed block, pinned to that
  change's impl commit and line range. A modified function thus carries its original
  header plus one extra line per ported change. Later, when a specific logic branch
  has a wacrg decision artifact, its link goes in this same slot (a second
  `// Source of truth:` line at that branch). This is the one place the reference
  may be named in code.
- **The three-line stub block** (`// TODO` / `// agent suggestion: ...` /
  `// human input:`) — an unfinished body, per the scaffolding standard above. The
  three lines exist only while the body is a stub and are removed when it lands.
- **`// ASSUMPTION: ...`** — a choice made without full confirmation, stating what
  would invalidate it.
- **`// NOT VALIDATED: ...`** — a body that **is** implemented (a faithful 1:1 port
  of the reference) but that **no passing KAT exercises yet** — typically a function
  gated behind an end-to-end vector whose pipeline is not yet wired. State *which*
  KAT will cover it (e.g. "validated once the #15 decoder runs `e2e_vectors.json`").
  This marker is a **debt and a promise**: the function is provisional until proven.
  It may only be added when the human has **explicitly authorized landing unvalidated
  bodies** — never on your own initiative (the default remains "green KAT or it is not
  done"). **The moment a KAT that depends on the function passes, delete the marker**
  (and confirm the KAT actually covers that function). Treat removing stale
  `// NOT VALIDATED:` markers as part of every module's VERIFY step: when wiring a KAT,
  grep the functions it transitively exercises and clear their markers.
- **A short context note** — only when a future reader would otherwise lose
  non-obvious context (a magic constant's origin, a byte-order quirk, a deviation
  from the reference and why).
- **Doc comments** on exported identifiers, per Go convention — a brief statement
  of what it is. Keep KAT/validation detail in the conversation and datasheet, not
  in doc comments.

Do **not** narrate what the code plainly does, and do **not** use comments to
explain *why* — say the why out loud. Clean code is the default; comments are the
exception.

## Porting floating-point code (bit-exactness)

When the reference's float op order is load-bearing — matmul accumulation,
sqrt-then-reciprocal, any expanded table the codec later reads — the Go port must be
bit-for-bit identical, not merely "close". Two traps that have already cost real time:

- **FMA contraction.** Go's compiler fuses `a*b + c` into a single fused
  multiply-add (one rounding) on amd64/arm64, which diverges ~1 ULP from a reference
  that rounds the multiply and the add separately (Rust/LLVM does not fuse by
  default). A plain temporary does **not** stop it — the Go spec permits fusion
  *across statements*. The only reliable fix is an **explicit `float32(...)`
  conversion around the multiply itself**: `prod := float32(a * b); y += prod`. Wrap
  every load-bearing product that feeds an add.
- **float64-cast transcendentals.** `sqrt`/`log2`/`cos` written as
  `float32(math.Sqrt(float64(x)))` match the reference's f32 op for `sqrt`
  (double-rounding is benign there) but can differ a few ULP for `log2` and friends.
  Expect ≤ a few ULP on transcendental-derived table fields.

Validate a regenerated or expanded table two ways: (1) cross-check against the
reference's **own golden constants** (e.g. its `to_bits` checksums) for a true
cross-implementation bit match, and (2) field-by-field against the prior artifact —
integer and non-transcendental floats must be **bit-identical**; sqrt/log2-derived
fields within a stated ULP bound. The decisive gate is always the downstream KAT: if
decode/encode stays bit-exact, a bounded float divergence on derived fields is
benign (state the bound, don't hide it).

## Commits and changelog

- One module change per commit. Subject: `<module>: <imperative change>` (no
  wrapping parentheses). Examples: `mlow/toc: scaffold smpl TOC parser`,
  `srtp/e2e: implement RFC3711 AES-CM PRF`,
  `mlow/pitch: KAT-verify against pitch_vectors.json`.
- Every commit updates [`CHANGELOG.md`](CHANGELOG.md) under the module with the
  new state (`scaffolded` / `implemented` / `KAT-verified`).
- Before shipping an **implementation** commit (a function body landing), run
  `/code-review` (the CodeRabbit CLI/skill) for a quick review and address its
  findings first. Scaffold/datasheet/docs commits don't require it.
- Commit messages state **what was validated** when relevant (which vector,
  pass/fail). No attribution lines. No pushing unless the human asks.
- When a commit's change is driven by a reference (Rust) or wacrg spec that
  required a Go update, put the **source-of-truth permalink(s)** in the commit
  **body**, never in the subject. The subject stays the plain
  `<module>: <change>`; the body carries a `Source of truth:` line per driving
  permalink, e.g.:

  ```
  mlow/lpc: implement forward A2NLSF

  Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/<sha>/wacore/src/voip/mlow/smpl_lpc.rs#L592-L604
  ```

## Decision artifacts (ADRs in wacrg)

When a conversation over a specific implementation detail **ends with a decision**,
that decision is recorded — but only on the human's direction, never on your own.

- **When:** the conversation has concluded and the human says to record it. Not
  mid-discussion, not pre-emptively, not for every change — only for decisions
  worth a timestamp.
- **Where:** wacrg (`docs/decisions/`), as a timestamped Markdown record stating
  the decision, the context, the options weighed, and why this one. wacrg is the
  spec home; the decision becomes part of the agreed record.
- **How it connects to code:** the Go file may carry a single plain-URL comment to
  the decision artifact where it genuinely helps a future reader find the rationale
  — a pointer, not an explanation. Nothing else about the reference or the why goes
  in the code.

You must know how to write one (a clear, dated ADR) and must **never** write one
unprompted. Generating decision artifacts autonomously is a violation.

## Explaining as you go (in conversation)

Narrate the why **proactively**, in the chat, as you work — so the human follows
what is being built and why in real time, without reading the code to find out.
Before scaffolding a module, say what it is and why it is next; as you place each
envelope, say what it does and where it comes from; when you make or defer a
decision, say so.

If the human asks "what is this / is this right / what did we validate?", answer
concretely from the datasheet and the vector: the Rust source location, the
constants, the KAT file and what it covers, and any open assumptions. If you
cannot answer from the datasheet, the datasheet is incomplete — say so and fix it
before proceeding. None of this lives in code comments.

## What never happens here

- No autonomous multi-module runs.
- No filling a function body with a guess to avoid stopping.
- No "it compiles, ship it" — green KAT or it is not done. The **only** exception is
  a body the human has explicitly authorized landing ahead of its vector, which must
  carry a `// NOT VALIDATED:` marker (see Comment policy) and be proven the moment its
  KAT exists.
- No silently copying logic whose meaning you cannot explain.
- No importing or copying the reference library — the Go stays an independent
  implementation. Naming it is now expected: every function carries a
  `// Source of truth:` provenance comment, and the reference may be cited there
  (and only there, plus `// ASSUMPTION`/context notes that need it).
- No writing a decision artifact (ADR) without the human's direction.
- No reuse of the old dublin/meowmeow calling code as a source.
