# Security policy

meowcaller is a library implementation of WhatsApp VoIP and follows the wacrg spec for design decisions. This policy explains how to handle two different kinds of security matters.

## 1. You found a security issue in WhatsApp itself

If, during your own research, you discover an actual security weakness in WhatsApp,
**please report it to the vendor first** through their official channel, and **do not
open it here**. This project does not collect, host, or coordinate weaknesses in
third-party products, and it is not a place to publish them.

- WhatsApp / Meta accept reports through their official program:
  <https://www.facebook.com/whitehat>

Keep details out of public issues and discussions until the vendor has had a
reasonable chance to respond.

## 2. You found a problem with this repository

Use this path for issues that are about meowcaller itself, for example:

- a tooling/CI weakness (e.g. a workflow that could leak a token),
- a dependency advisory affecting our scripts,
- **sensitive data that was committed by mistake**: real phone numbers, JIDs, keys,
  tokens, or media (see the [disclaimer](./DISCLAIMER.md)).
- Bugs that allow attackers to gain remote access to systems using meowcaller.

Please report these **privately**:

- Preferred: GitHub's **private vulnerability reporting** (the "Report a vulnerability"
  button under the repository's Security tab), or
- Email **rajeh@reforward.dev**.

For accidental data exposure, also note it so a maintainer can purge it from history
promptly.

## What to expect

- We aim to acknowledge a report within a few days.
- We will work with you on a fix or cleanup and credit you if you wish.

## Scope and conduct

- **In scope:** the repository's tooling, workflows, schemas, dependencies, and any
  data accidentally committed here.
- **Out of scope:** weaknesses in WhatsApp or other third-party software (see section 1),
  and anything requiring access to systems or accounts you do not own.
- All research associated with this project must stay within the bounds described in
  the [disclaimer](./DISCLAIMER.md): your own accounts and devices, no targeting of
  other people, and no real user data in the repository.
