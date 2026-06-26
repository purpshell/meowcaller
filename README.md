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

