# voip — meowcaller calling-layer demo

A small cross-platform CLI that drives the meowcaller calling layer with a real mic
and speaker. It's a **separate Go module** so its audio/WhatsApp dependencies
(miniaudio, whatsmeow, sqlite) stay out of the library.

Audio is captured/played through [miniaudio](https://github.com/gen2brain/malgo),
which picks the OS default backend (CoreAudio / WASAPI / ALSA / PulseAudio), so it
runs on macOS, Linux and Windows with no per-OS device wiring. (cgo is required — a
C compiler must be on PATH.)

## Commands

```
voip loopback            Mic → MLow encode → E2E-SRTP protect (RTP WARP header +
                         WARP MI tag) → unprotect → MLow decode → speaker.
                         No WhatsApp connection: it exercises the whole codec +
                         keying + framing stack on live audio. Talk and you hear
                         yourself back through the full pipeline.

voip call <target>       Log in (QR on first run), resolve the peer LID, discover
                         the peer's devices, encrypt a fresh callKey per device, and
                         send a <call><offer>.
                         <target> = a phone number (+15551234567), a phone JID
                         (15551234567@s.whatsapp.net), or a LID JID (...@lid).

voip listen              Log in and print incoming call signaling.

voip autoaccept          Log in and auto-accept incoming calls: decrypt the offer's
                         callKey and reply preaccept + accept.
```

Run from this directory:

```sh
go run . loopback
go run . call +15551234567
go run . autoaccept
```

## What's wired

`loopback` is the honest end-to-end exercise of everything in the library: `mlow`
encode/decode, `MediaPipeline` (RTP WARP framing + E2E-SRTP + WARP MI tag), and the
recv-side ROC tracker — all on real audio, no network.

`call` and `autoaccept` drive the full signaling path against a real account:

1. **Login + app state** — whatsmeow session in a local `wa-voip.db` (QR pairing on
   first run). After the socket is ready we wait for the connection, sync the
   critical app-state block, and announce presence so the server delivers call
   signaling.

   > **Two separate databases.** whatsmeow's auth/session store (`wa-voip.db`) and
   > meowcaller's own call store (`meowcaller.db`) are *different files with
   > independent connections*. meowcaller never writes to whatsmeow's auth DB, so a
   > busy call can't lock or corrupt the device session. Both are gitignored and
   > disposable; deleting `wa-voip.db` just means re-pairing (no migration), and
   > `meowcaller.db` is recreated on next run.
2. **LID resolution** — a phone target is mapped to the peer's `@lid` via a usync
   `GetUserInfo` query (which carries the `lid` field and persists the PN→LID
   mapping). The call's E2E keys and SSRCs derive from the LID, so this happens
   *before* the offer.
3. **Device discovery** — `GetUserDevices` lists the peer's devices.
4. **Privacy token** — the peer's privacy token (from `Store.PrivacyTokens`) is
   attached to the offer when present; the server requires it to call contacts with
   privacy enabled.
5. **callKey encryption** — the 32-byte callKey is wrapped as the Signal message
   `Message{Call{CallKey}}` and encrypted to each device's Signal session
   (`EncryptMessageForDevice`, fetching a pre-key bundle when there's no session
   yet). `autoaccept` does the reverse: it decrypts the inbound offer's `<enc>`.
6. **Offer / answer** — `signaling.BuildOffer` (and `BuildPreaccept`/`BuildAccept`
   for answering) assemble the call-control stanzas with the load-bearing child order.

## Live media (wired, `NOT VALIDATED`)

After the offer is accepted, audio flows over the relay; `media.go` wires that hop
end to end so the call no longer dies on `setup_failed`:

1. **Relay parse** — the `<relay>` node (a port of the reference `relay_parse`) is
   found in the offer / `relaylatency` / `transport` stanza and parsed into its
   endpoints, indexed tokens and `<key>` (the STUN MESSAGE-INTEGRITY key — the raw
   `relay_key_ascii`, *not* a derived WARP key).
2. **Connect + allocate** — `relay.ConnectRelayMedia` opens the pion
   DTLS/SCTP/DataChannel transport to the chosen endpoint, then a STUN Allocate
   (`stun.BuildWasmStunAllocateRequest`) registers the stream. A 1 Hz allocate+ping
   keepalive keeps the relay from dropping us.
3. **Media loop** — the loopback-proven `MediaPipeline`: mic → MLow → E2E-SRTP
   protect → DataChannel, and DataChannel → classify → unprotect → MLow → speaker.

Both directions are wired: `call` pre-seeds its generated callKey and starts media
when the relay arrives; `autoaccept` decrypts the inbound callKey, answers, and does
the same.

**`NOT VALIDATED`:** the pion relay transport (DTLS handshake to WhatsApp's relay)
has no test vector and can only be exercised against a live relay — this is the one
hop in the stack that hasn't been proven, so expect to debug it on a real call.
Everything it feeds (codec, keying, framing, the `MediaPipeline`) is KAT-verified
and proven by `loopback`.

## Notes

- Requires cgo (a C compiler) for miniaudio. The Signal/WhatsApp pieces use
  whatsmeow's low-level `DangerousInternals` (the only entry point for raw call
  nodes and per-device encryption) — expected for call signaling.
- All dependencies are public; `loopback` needs only `malgo` + the library.
