# meowcaller command

Pair once in an interactive terminal, using the same store path the automation will use:

```text
meowcaller pair --store <path>
```

Scan the QR in WhatsApp under **Linked devices**. A successful command prints
`MeowCaller linked device ready` and exits; later `pair` runs verify that the saved linked
device can still connect.

`meowcaller notify` is the headless, send-only command surface for automation:

```text
meowcaller notify [flags] <target> <audio.mp3|wav|ogg|opus|mulaw|ulaw|->
```

`target` may be a phone number, phone JID, or LID JID. `-` reads raw 8 kHz mono G.711 PCMU from stdin. Files use their extension; `.mulaw` and `.ulaw` are also raw PCMU. The command opens no local audio/video device, attaches no inbound sink, sends the input once after the recipient answers, then hangs up.

Flags:

- `--store PATH`: linked-device SQLite store; defaults to the platform user-config directory. On Unix, its parent directory must be user-private (`0700`).
- `--answer-timeout DURATION`: maximum ringing time; default `45s`, `0` disables.
- `--max-duration DURATION`: maximum playback after answer; default `5m`, `0` streams until EOF.

Pairing is an explicit terminal step so the QR never enters service logs. `notify` refuses an
unpaired store when run headlessly. The command does not offer diagnostic capture and defaults
to warning-only structured logs; `MEOW_LOG_LEVEL` opts into a different level.
