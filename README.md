# meowcaller
[![Go Reference](https://pkg.go.dev/badge/github.com/purpshell/meowcaller.svg)](https://pkg.go.dev/github.com/purpshell/meowcaller)

meowcaller is a Go library for the WhatsApp Web VoIP stack. It is 100% pure GO without CGO and it has minimal dependencies. It includes the novel proprietary audio codec MLOW written and validated completely in GO. In turn, meowcaller does not rely on any native bindings and can run everywhere that GO can.

## Discussion
Matrix room: [#meowcaller:matrix.org](https://matrix.to/#/#meowcaller:matrix.org).

Discord channel: #meowcaller in the [WhiskeySockets Discord server](https://whiskey.so/discord).

You can find the underlying spec in the [WhatsApp Calls Research Group](https://wacrg.org). We are under process of standardizing the spec and moving away from whatsapp-rust source of truth comments.

## Usage
The [godoc](https://pkg.go.dev/github.com/purpshell/meowcaller) includes docs for all methods.

There's a range of examples in the [examples](/examples/) directory.

### Headless notifications

The installable `meowcaller` command can place a send-only notification call without opening a microphone, speaker, video device, or inbound audio sink:

```sh
go install github.com/purpshell/meowcaller/cmd/meowcaller@latest
meowcaller pair
meowcaller notify +15551234567 notice.wav
cat notice.mulaw | meowcaller notify +15551234567 -
```

Inputs may be WAV, MP3, Ogg/Opus, or raw 8 kHz mono G.711 PCMU (`.mulaw`, `.ulaw`, or stdin with `-`). PCMU is decoded in 480-byte / 60 ms blocks and expanded to the 16 kHz, 960-sample frames required by the call codec. The command sends silence until the recipient answers, streams the input once, hangs up at EOF, and never decrypts or decodes inbound audio because it does not attach a receive sink.

`meowcaller pair` displays a linked-device QR in the terminal and exits after the session is ready. `notify` never pairs; it reuses authentication from `~/Library/Application Support/meowcaller/whatsapp.db` on macOS or the platform user-config equivalent. Pass the same `--store` path to `pair` and `notify` when overriding it. The store file is created as `0600`; on Unix its parent directory must be user-private (`0700`) so SQLite sidecars cannot expose linked-device credentials. QR data and media are never written to logs, and command logging defaults to `warn` (set `MEOW_LOG_LEVEL` explicitly for troubleshooting).

Notifications wait up to 45 seconds for an answer and play for at most 5 minutes by default. Both limits are configurable with `--answer-timeout` and `--max-duration`; `--max-duration=0` explicitly permits an unbounded stream through EOF. Hitting a limit stops playback, hangs up, and exits nonzero.

The API is easy to approach and implement: attach a **`Source`** to send media, a **`Sink`** to receive it, and register callbacks for call events.

A 12-line example to show the power and simplicity of the library:
```go
// wa is a whatsmeow.Client
client := meowcaller.NewClient(wa)

client.OnIncomingCall(func(call *meowcaller.Call) {
    call.Answer()

    if mp3, err := meowcaller.MP3File("hello.mp3"); err == nil {
        call.Play(mp3)               // stream audio to the caller
    }
    if wav, err := meowcaller.WAVRecorder("caller.wav"); err == nil {
        call.Receive(wav)            // record their voice
    }
    if h264, err := meowcaller.AnnexBRecorder("caller.h264"); err == nil {
        call.ReceiveVideo(h264)      // record their video
    }
})

// Placing a call is just as short:
call, _ := client.Call(ctx, "+15551234567")
call.Receive(meowcaller.SinkFunc(func(pcm []float32) { /* the peer's audio */ }))
```

## Features

Core VoIP features are present:

- Outbound calls
- Inbound calls
- Audio calls (the pure-Go MLow codec)
- Video calls (ported from WaCalls; see Credits)

Things that are not yet implemented:

- Opus codec fallback for clients not using MLOW (in progress; testing edge cases)
- Mid-call audio→video upgrade (from-start video calls work; the upgrade handshake is WIP)
- Group calls (WIP)
- Call signalling features (raise hand, lobby, reactions)

## Credits

meowcaller relies heavily on primitives that are implemented in the [WhatsApp Calls Research Group](https://wacrg.org). I thank all the developers who have contributed to it.

meowcaller's video call implementation is built on the design of [WaCalls](https://github.com/JotaDev66/WaCalls) by [jotadev66](https://github.com/JotaDev66). WaCalls in turn vendors meowcaller's MLow impl.

## Sponsoring and contribution
You may contribute to the maintenance of this library by sponsoring its maintainers on [GitHub](https://purpshell.dev/sponsor).

You may also submit pull requests and issues where relevant, given you follow the contributor [Code of Conduct](CODE_OF_CONDUCT.md).

## License

This repository follows the MIT license, as stated in the [LICENSE](/LICENSE) file
