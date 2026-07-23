# Diagnostics

`diag` contains meowcaller's opt-in diagnostic recorder and the companion tools
used to capture reference behavior from WhatsApp Web. None of these diagnostics
are enabled by default.

Library users can attach a `*diag.Recorder` with `meowcaller.WithDiagnostics`.
The recorder writes one JSONL file per stream so keying, relay, RTP, media, and
call-state events remain independently searchable.

## WhatsApp Web reference capture

This is a headless build of the v4 WhatsApp logger without its viewer, composer,
experiments UI, or `.wslc` workflow. Its WAM and normal WhatsApp logger rows are
streamed to JSONL alongside call signaling and wire bytes, parsed relay
topology, VoIP stack calls and logs, call-model and page-input changes, media
acquisition, WebRTC session descriptions and ICE state, data-channel traffic,
media tracks, audio contexts, WebRTC statistics, worker-realm data channels,
and focused Chrome DevTools Protocol events. Page and headless logger events use
the `wa-voip-diag/v2` capture schema; worker payloads use `wa-voip-worker/v2`.
The headless sender bounds its retry queue, and the normal logger hooks isolate
diagnostic failures so they cannot block WhatsApp's original encode/decode calls.

The full worker and CDP capture requires Chrome's `tabs` and `debugger`
permissions. Chrome shows a debugger attachment banner while it is active. Do
not open DevTools on the captured WhatsApp tab because it competes for the same
debugger session.

Start the local collector from the repository root:

```sh
go run ./diag/extension-server
```

Then open `chrome://extensions`, enable developer mode, choose **Load unpacked**,
and select `diag/extension`. Reload WhatsApp Web before placing a call. The
collector writes compact JSONL files under `diag/captures/`.

The extension sends data only to `http://127.0.0.1:3219/events`. Remove or
disable it after collecting a diagnostic call.
