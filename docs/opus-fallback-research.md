# Pure-Go Opus fallback research

This branch preserves MLow as the default and adds native RFC 6716 Opus when
call signaling selects it. The target profile is mono PCM at 16 kHz, 960
samples per 60 ms packet, RTP payload type 120, and timestamp step 960.

An initial CGO/libopus prototype established the interoperability contract;
the product implementation in this branch is pure Go and CGO remains only in
an external test oracle.

This candidate keeps one stateful Pion SILK/WB encoder and encodes three 20 ms
units before combining them into one standard RFC 6716 Code 3 VBR packet.
Each receive stream owns an independent decoder. Unsupported codecs, invalid
frame sizes, and malformed packets fail closed without falling back to MLow.

The offline acceptance suite covers:

- product encode to native libopus decode;
- native libopus encode to product decode;
- negotiated codec selection, PT120, and timestamp step 960;
- independent encoder and decoder state;
- invalid input and the CGO-disabled product build.

The same oracle also validates a separate direct-60-ms Pion candidate. Live
Opus has not been observed yet because full bidirectional capability/settings
negotiation is still pending.

Reference: [whatsapp-rust codec profiles and negotiation](https://github.com/oxidezap/whatsapp-rust/blob/main/agent_docs/voip_audio_codecs.md).
