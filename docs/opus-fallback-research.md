# Pure-Go Opus fallback research

This branch preserves MLow as the default and adds native RFC 6716 Opus when
call signaling selects it. The target profile is mono PCM at 16 kHz, 960
samples per 60 ms packet, RTP payload type 120, and timestamp step 960.

An initial CGO/libopus prototype established the interoperability contract;
the product implementation in this branch is pure Go and CGO remains only in
an external test oracle.

This candidate uses a direct 60 ms SILK/WB encoder path in Pion Opus, with one
stateful encoder per send stream and one independent decoder per receive
stream. Unsupported codecs, invalid frame sizes, and malformed packets fail
closed without falling back to MLow.

The required Pion change is isolated in
[`pion/opus#219`](https://github.com/pion/opus/pull/219).

The offline acceptance suite covers codec selection, PT120, timestamp step
960, state isolation, invalid input, product-to-libopus and libopus-to-product
interoperability, and the CGO-disabled product build. Live Opus has not been
observed yet because full bidirectional capability/settings negotiation is
still pending.

Reference: [whatsapp-rust codec profiles and negotiation](https://github.com/oxidezap/whatsapp-rust/blob/main/agent_docs/voip_audio_codecs.md).
