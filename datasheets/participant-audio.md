# Datasheet: `api/participant-audio`

Expose the existing authenticated participant audio before group mixing. This
adds an observer; the decoder, mixer and `Receive` output remain unchanged.

Status: implemented. The focused ownership/metadata regression and full race
suite passed on the original `e121d029` candidate. Its isolated native broker
fixture verifies all six decoded tone/identity directions for direct group offers
and call links at the original dependency pins recorded below. New compositions
need a separate native replay.

**Reference pinned at:** 612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4

## Reference source

The source is meowcaller's existing decoded frame, pinned to commit
`612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4`. This is an additive Go API,
not a new codec or wire-format implementation.

[Decoded participant audio](https://github.com/purpshell/meowcaller/blob/612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4/group_media_receive.go#L21-L31) is:

```go
type decodedParticipantAudio struct {
	ParticipantID string
	UserJID       types.JID
	DeviceJID     types.JID
	PID           uint32
	HasPID        bool
	SSRC          uint32
	Timestamp     uint32
	PCM           []float32
}
```

[The receive loop](https://github.com/purpshell/meowcaller/blob/612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4/engine_media.go#L1081-L1118)
already rejects unsuccessful authentication before using the frame. The observer
runs at that point, before either group mixing or direct playout.

[The video observer](https://github.com/purpshell/meowcaller/blob/612fd411dbc2011c0c5672d7a355b5ba3b8c7ea4/livecall.go#L632-L651)
establishes the existing registration pattern: capture the callback under the
call mutex, release the mutex, then invoke it with an owned copy. The audio
observer follows that pattern.

## Go envelope

```go
type ParticipantAudioFrame struct {
	ParticipantID string
	Sender        types.JID
	Device        types.JID
	PID           uint32
	HasPID        bool
	SSRC          uint32
	Timestamp     uint32
	PCM           []float32
}

func (c *Call) OnParticipantAudioFrame(fn func(ParticipantAudioFrame))
```

`PCM` is 16 kHz mono float32 audio. The callback owns the samples and may retain
or modify them. It runs synchronously on the receive goroutine and must return
promptly. Registering nil removes it. Registration from inside the callback is
safe. `HasPID` distinguishes a roster-assigned PID from an unavailable PID.
`ParticipantID` retains the existing receive registry's canonical ID; `Sender`
and `Device` retain its user and device JIDs, including phone-free LID devices.

## Verification

`TestParticipantAudioCallbackOwnsSamplesAndKeepsIdentity` checks an exact sample
vector, metadata preservation, independent decoder/callback buffers and removal
from inside the callback. Existing receive authentication, mixer and codec
vectors run unchanged. The native broker fixture identifies both remote speakers
at each of three receivers through real encrypted media.
No raw samples, call keys or Signal material belong in its receipt.

The broker check used Barback `a0562ed37bbaffdd6ad7f5789447929c65edd007`,
backend `32ab939e94ea52c0bb51fe594a9062a956986bfc` plus the attributed-audio
fixture, and private HyperMeow `7f135a812f07bbbe41fe7559d946d6667bf6a998`.
RabbitMQ 3.13.7, Redis 7, real Noise/Signal traffic and a race-built Go child run
with seeded paired identities and control-plane authority. This does not prove
QR enrollment, API/RLS admission or a managed native Inspector connection.

## Composition boundary

The callback is composed into the existing guest delivery branch after
`e7026913e681b27618c9ceb3e415cafcea0fa6d2`, which includes the ordered-join and
media ownership repairs. It retains published HyperMeow `b61214a005d7`.
The receive-loop observer has the same nil-Call guard as the existing video
observer. No cryptographic, codec, custody, signaling or managed-route policy
changes are included.

The composed source passes ordinary `GOWORK=off` race tests, build and vet with
published HyperMeow `b61214a`. The backend's `nativebridge`-tagged runner test
package fails to compile with the old MC `612fd411` pin because the method and
frame type are missing, and compiles with this composition. That check runs no
fixture tests or hosted traffic. Its temporary modfile leaves consumer pins and
source unchanged. CodeRabbit CLI returned a rate-limit error without a verdict;
final-head correctness/security reviews remain separate gates.
