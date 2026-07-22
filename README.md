# meowcaller
[![Go Reference](https://pkg.go.dev/badge/github.com/purpshell/meowcaller.svg)](https://pkg.go.dev/github.com/purpshell/meowcaller)

meowcaller is a Go library for the WhatsApp Web VoIP stack. It is 100% pure GO without CGO and it has minimal dependencies. It includes the novel proprietary audio codec MLOW written and validated completely in GO. In turn, meowcaller does not rely on any native bindings and can run everywhere that GO can.

## Discussion
Matrix room: [#meowcaller:matrix.org](https://matrix.to/#/#meowcaller:matrix.org).

Discord channel: #meowcaller in the [WhiskeySockets Discord server](https://whiskey.so/discord).

You can find the underlying spec in the [WhatsApp Calls Research Group](https://wacrg.org). Video transition behavior is cross-checked against the independently implemented [whatsapp-rust call stack](https://github.com/oxidezap/whatsapp-rust/pull/1024).

## Usage
The [godoc](https://pkg.go.dev/github.com/purpshell/meowcaller) includes docs for all methods.

There's a range of examples in the [examples](/examples/) directory.
The standalone [browser call console](/examples/web/) provides QR pairing, audio/video
calling, independent camera controls, orientation, and reactions without a frontend build step.

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
- Video calls, including calls that start with video
- Mid-call audio-to-video upgrade, acceptance, rejection, cancellation, and downgrade
- Camera orientation and authenticated video keyframe feedback
- Send and receive call emoji reactions over the dedicated RTC app-data stream

WhatsApp call signaling, call-key exchange, and relay election are provided by
[whatsmeow](https://github.com/tulir/whatsmeow). Meowcaller owns the RTP/SRTP media
plane, codecs, pacing, video framing, reactions, and diagnostics.

Things that are not yet implemented:

- Opus codec fallback for clients not using MLOW (in progress; testing edge cases)
- Group calls (WIP)
- Call signalling features (raise hand, lobby)

## Credits

meowcaller relies heavily on primitives that are implemented in the [WhatsApp Calls Research Group](https://wacrg.org). I thank all the developers who have contributed to it.

The video transition lifecycle was validated against [whatsapp-rust PR #1024](https://github.com/oxidezap/whatsapp-rust/pull/1024), an independent implementation of the same protocol.

## Sponsoring and contribution
You may contribute to the maintenance of this library by sponsoring its maintainers on [GitHub](https://purpshell.dev/sponsor).

You may also submit pull requests and issues where relevant, given you follow the contributor [Code of Conduct](CODE_OF_CONDUCT.md).

## License

This repository follows the MIT license, as stated in the [LICENSE](/LICENSE) file
