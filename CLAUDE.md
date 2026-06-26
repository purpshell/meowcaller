# CLAUDE.md

You are working on **meowcaller**, a clean-room, pure-Go implementation of the
WhatsApp 1:1 VoIP stack (signaling, keying, transport, media). Read these before
doing anything:

1. **@AGENTS.md** — the build protocol. It is binding. In short: you
   are **not autonomous**. You build **one module at a time**; you **scaffold**
   function envelopes with `// TODO` bodies and then **stop for the human** to
   direct the logic. You explain *why* in the conversation, never in code comments.
   Verify against the test vector or it is not done.

Non-negotiables: the Go never imports or copies a reference library (it stays an
independent implementation), but every function carries a `// Source of truth:`
comment citing the reference symbol it ports — see `AGENTS.md` Comment policy;
commits are `(<module>: <change>)` and update `CHANGELOG.md`; commit but do not
push; no real PII in tests. Scope is 1:1 calls.

If you are about to write a function body that involves a real engineering choice,
**stop and ask** instead. Scaffold, explain in chat, and let the human decide.
