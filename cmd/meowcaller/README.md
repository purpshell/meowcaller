# meowcaller command

`meowcaller notify` is the headless, send-only command surface for automation:

```text
meowcaller notify [flags] <target> <audio.mp3|wav|ogg|opus|mulaw|ulaw|->
```

`target` may be a phone number, phone JID, or LID JID. `-` reads raw 8 kHz mono G.711 PCMU from stdin. Files use their extension; `.mulaw` and `.ulaw` are also raw PCMU. The command opens no local audio/video device, attaches no inbound sink, sends the input once after the recipient answers, then hangs up.

Flags:

- `--store PATH`: linked-device SQLite store; defaults to the platform user-config directory. On Unix, its parent directory must be user-private (`0700`).
- `--answer-timeout DURATION`: maximum ringing time; default `45s`, `0` disables.
- `--max-duration DURATION`: maximum playback after answer; default `5m`, `0` streams until EOF.

The first pairing must run in an interactive terminal so the QR never enters service logs. Scan it in WhatsApp under **Linked devices**. Subsequent invocations reuse the protected store and can run headlessly. The command does not offer diagnostic capture and defaults to warning-only structured logs; `MEOW_LOG_LEVEL` opts into a different level.
