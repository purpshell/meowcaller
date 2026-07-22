# cli — meowcaller managed-calling demo

A small cross-platform CLI that drives the **managed** meowcaller calling API with a
real mic and speaker. It is a **separate Go module** so its audio/WhatsApp
dependencies (miniaudio, whatsmeow, sqlite) stay out of the library.

The command owns only the whatsmeow login boilerplate (QR pairing + connect) and the
logger — then hands the connected client to `meowcaller.NewClient` and does everything
through the managed `Call` API. Mic/speaker come from the opt-in cgo subpackage
`meowcaller/audio/malgo`, so a C compiler must be on PATH.

## Commands

```
cli call <target>              Place a 1:1 call; your mic -> peer, peer -> your speaker.
cli play <target> <file>       Place a call and stream a .mp3/.wav/.opus file to the
                               peer (peer audio still goes to the speaker).
cli listen                     Log in and print incoming call signaling.
cli autoaccept [record.wav]    Auto-answer incoming calls, wiring mic <-> speaker, or
                               recording the peer's audio to a .wav file.
```

`<target>` is a phone number (`+15551234567`), a phone JID, or a LID JID (`...@lid`).
Run from this directory:

```sh
go run . call +15551234567
go run . play +15551234567 hold-music.mp3
go run . autoaccept greeting.wav
```

For the browser audio/video console, use the separate
[`examples/web`](../web/) module.

## What it shows

The managed API hides all the signaling/keying/relay/media that this example used to
hand-roll:

```go
client := meowcaller.NewClient(wa, meowcaller.WithLogger(*zerolog.Ctx(ctx)))

call, _ := client.Call(ctx, target)          // place a 1:1 call
call.Play(mic)                               // a Player streams a source (mic/file) out
call.Receive(speaker)                        // a sink takes the peer's decoded audio
call.OnReady(func() { ... })
call.OnEnd(func(reason string) { ... })
call.OnReaction(func(reaction meowcaller.CallReaction) { ... })
call.SendReaction("👍")

client.OnIncomingCall(func(c *meowcaller.Call) { c.Answer() /* or c.Reject() */ })
```

Audio sources decode to the codec's 16 kHz mono frames behind the scenes:
`meowcaller.MP3File` / `WAVFile` / `OpusFile` (and `malgo.Mic()`); sinks are
`malgo.Speaker()` / `meowcaller.WAVRecorder` / `meowcaller.SinkFunc`.

> **NOT VALIDATED:** the live relay hop (DTLS handshake to WhatsApp's relay) has no test
> vector and is only exercisable against a real relay — expect to debug it on a real
> call. Everything it feeds (codec, keying, framing) is KAT-verified.

## Notes

- Requires cgo (a C compiler) for miniaudio via `meowcaller/audio/malgo`.
- whatsmeow's session lives in a local `wa-voip.db` (QR pairing on first run); it is
  gitignored and disposable (delete it to re-pair).
- `MEOW_LOG_LEVEL=trace go run . call ...` surfaces the full per-frame trace across the
  whole stack.
