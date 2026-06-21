<!-- Datasheet = three things only: the reference source VERBATIM, the Go envelope
     (signatures, no bodies), and implementation suggestions. No behavioral summary,
     no implementation. The verbatim source is the only authoritative content. -->

# Datasheet: `srtp/warp`

WARP RTP extension constants and the WARP MESSAGE-INTEGRITY tag. Transport layer:
provides the RTP audio-piggyback extension word and the HMAC-SHA1 integrity tag
appended to protected packets.

**Validation vector:** `kats.json` — known-answer vectors (`e2e_srtp.peer_authKey`,
`inputs.samplePacket`, `inputs.roc`, and the expected `e2e_srtp.warp_mi_tag4`).
Copy it verbatim into `srtp/testdata/`.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust `wacore/src/voip/`).

## Reference source (verbatim — authoritative)


```rust
//! WARP RTP extension constants and the WARP MESSAGE-INTEGRITY tag.

use hmac::{Hmac, KeyInit, Mac};
use sha1::Sha1;

type HmacSha1 = Hmac<Sha1>;

pub const WARP_EXT_PROFILE: u16 = 0xdebe;
pub const WARP_AUDIO_PIGGYBACK_EXT: [u8; 4] = [0x30, 0x01, 0x00, 0x00];
pub const WARP_MI_TAG_LEN: usize = 4;
/// Packets #1-2 carry an empty extension; #3+ (0-based index >= 2) piggyback.
pub const WARP_PIGGYBACK_START_PACKET: usize = 2;

/// Audio piggyback extension word for `packet_index`, or `None` for the first packets.
pub fn audio_piggyback_extension_for(
    packet_index: usize,
    enabled: bool,
    start_packet: usize,
) -> Option<u32> {
    if !enabled || packet_index < start_packet {
        return None;
    }
    Some(u32::from_be_bytes(WARP_AUDIO_PIGGYBACK_EXT))
}

/// WARP MI tag = first `tag_len` bytes of HMAC-SHA1(auth_key, packet || roc_be32).
pub fn compute_warp_mi_tag(
    auth_key: &[u8],
    packet_without_tag: &[u8],
    roc: u32,
    tag_len: usize,
) -> Vec<u8> {
    let mut mac = HmacSha1::new_from_slice(auth_key).expect("HMAC accepts any key length");
    mac.update(packet_without_tag);
    mac.update(&roc.to_be_bytes());
    let full = mac.finalize().into_bytes();
    full[..tag_len].to_vec()
}

/// Append the WARP MI tag to a protected packet.
pub fn append_warp_mi_tag(
    auth_key: &[u8],
    packet_without_tag: &[u8],
    roc: u32,
    tag_len: usize,
) -> Vec<u8> {
    let tag = compute_warp_mi_tag(auth_key, packet_without_tag, roc, tag_len);
    let mut out = Vec::with_capacity(packet_without_tag.len() + tag.len());
    out.extend_from_slice(packet_without_tag);
    out.extend_from_slice(&tag);
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::voip::testkat::{hexd, kats};

    #[test]
    fn warp_mi_tag_matches_kat() {
        let k = kats();
        let auth_key = hexd(&k, &["e2e_srtp", "peer_authKey"]);
        let packet = hexd(&k, &["inputs", "samplePacket"]);
        let roc = k["inputs"]["roc"].as_u64().unwrap() as u32;
        let tag = compute_warp_mi_tag(&auth_key, &packet, roc, WARP_MI_TAG_LEN);
        assert_eq!(
            hex::encode(tag),
            k["e2e_srtp"]["warp_mi_tag4"].as_str().unwrap()
        );
    }
}
```

## Go envelope (signatures only)

The corresponding Go declarations — exported types and function **signatures with
no bodies**. This is the surface to implement; it is not the implementation.

```go
package srtp

const (
	WarpExtProfile          uint16 = 0xdebe
	WarpMITagLen                   = 4
	WarpPiggybackStartPacket       = 2
)

var WarpAudioPiggybackExt = [4]byte{0x30, 0x01, 0x00, 0x00}

func AudioPiggybackExtensionFor(packetIndex int, enabled bool, startPacket int) (uint32, bool)

func ComputeWarpMITag(authKey, packetWithoutTag []byte, roc uint32, tagLen int) []byte

func AppendWarpMITag(authKey, packetWithoutTag []byte, roc uint32, tagLen int) []byte
```

## Implementation suggestions (guidance, not authoritative)

- HMAC-SHA1 is `crypto/hmac` + `crypto/sha1`: `hmac.New(sha1.New, authKey)`, then
  write `packetWithoutTag` followed by the 4-byte big-endian ROC, then `Sum(nil)`
  and slice the first `tagLen` bytes.
- The MAC is computed over the concatenation `packet || roc_be32`; feed it as two
  writes (packet, then ROC) — equivalent to one write of the joined bytes.
  `roc.to_be_bytes()` → `binary.BigEndian.PutUint32`.
- `u32::from_be_bytes(WARP_AUDIO_PIGGYBACK_EXT)` → `binary.BigEndian.Uint32` over
  the 4-byte array. The constant array is a fixed `[4]byte`; the extension word is a
  `uint32`.
- `Option<u32>` from `audio_piggyback_extension_for` maps to `(uint32, bool)`; the
  `None` case is `(0, false)`. `usize` indices → Go `int`.
- The reference `expect("HMAC accepts any key length")` never fails (HMAC takes any
  key length), so no error path is needed; return the tag slice directly.
