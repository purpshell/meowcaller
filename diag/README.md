# Diagnostics

`diag` contains meowcaller's opt-in diagnostic recorder and the companion tools
used to capture reference behavior from WhatsApp Web. None of these diagnostics
are enabled by default.

Library users can attach a `*diag.Recorder` with `meowcaller.WithDiagnostics`.
The recorder writes one JSONL file per stream so keying, relay, RTP, media, and
call-state events remain independently searchable.

## WhatsApp Web reference capture

This is a minimal call logger for comparing WhatsApp Web behavior with
meowcaller. It has no user interface and only records call-related signaling,
VoIP stack calls, internal VoIP logs, call-model changes, and media acquisition.

Start the local collector from the repository root:

```sh
go run ./diag/extension-server
```

Then open `chrome://extensions`, enable developer mode, choose **Load unpacked**,
and select `diag/extension`. Reload WhatsApp Web before placing a call. The
collector writes compact JSONL files under `diag/captures/`.

The extension sends data only to `http://127.0.0.1:3219/events`. Remove or
disable it after collecting a diagnostic call.
