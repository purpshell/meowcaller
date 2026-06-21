# Datasheet: `rtp/ssrc`

SSRC derivation (HKDF-SHA256 over call id / participant LID / slot word) and the
participant-LID formatting helpers used as HKDF `info`. Keying layer.

**Validation vector:** (integration — no single vector). Pinned by the inline
`#[test]` cases below against the shared `kats()` fixture
(`voip_crypto.ssrc_slot0`, `voip_crypto.ssrc_slot1`) plus the inline
participant-id format assertions. Copy that fixture JSON verbatim into
`rtp/testdata/`.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust `wacore/src/voip/`).

## Reference source (verbatim — authoritative)

```rust
//! SSRC derivation and participant-LID helpers for E2E HKDF `info`.

use hkdf::Hkdf;
use sha2::Sha256;

/// Slot words for the 9-stream relay allocate plan (node-load / WASM).
pub const WASM_RELAY_STREAM_SLOT_WORDS: [u32; 9] = [0, 1, 4, 2, 3, 5, 7, 8, 6];

/// Participant / stream SSRC: HKDF-SHA256(salt=slot_word LE32, ikm=call_id, info=lid, 4),
/// read back as a little-endian u32.
pub fn derive_wasm_participant_ssrc(call_id: &str, lid: &str, slot_word: u32) -> u32 {
    let hk = Hkdf::<Sha256>::new(Some(&slot_word.to_le_bytes()), call_id.as_bytes());
    let mut okm = [0u8; 4];
    hk.expand(lid.as_bytes(), &mut okm)
        .expect("4 bytes within HKDF limit");
    u32::from_le_bytes(okm)
}

/// All 9 relay-stream SSRCs in slot order.
pub fn derive_wasm_relay_stream_ssrcs(call_id: &str, lid: &str) -> [u32; 9] {
    WASM_RELAY_STREAM_SLOT_WORDS.map(|slot| derive_wasm_participant_ssrc(call_id, lid, slot))
}

/// Device-qualified LID for E2E SRTP HKDF `info`: keep an existing `:N@lid`,
/// bare `@lid` becomes `:0@lid`, everything else passes through. Intentionally a separate protocol
/// surface from SFrame's variant; they coincide today, so both delegate to one helper. Un-shim here
/// if E2E-SRTP ever needs to diverge.
pub fn format_e2e_srtp_participant_id(jid: &str) -> String {
    crate::voip::format_participant_id(jid)
}

/// Device-qualified LID variants the recv path tries as HKDF `info` (peer sender LIDs).
pub fn e2e_participant_id_variants(jid: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut push = |s: String| {
        let t = s.trim().to_string();
        if !t.is_empty() && !out.contains(&t) {
            out.push(t);
        }
    };
    let bare = jid.split('/').next().unwrap_or(jid).trim().to_string();
    push(bare.clone());
    push(format_e2e_srtp_participant_id(jid));
    if let Some(at) = bare.rfind('@')
        && at > 0
    {
        let user = &bare[..at];
        let domain = &bare[at + 1..];
        if domain == "lid" && user.contains(':') {
            let base = user.split(':').next().unwrap_or(user);
            push(format!("{base}:0@{domain}"));
            push(format!("{base}@{domain}"));
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::voip::testkat::kats;

    #[test]
    fn ssrc_matches_kat() {
        let k = kats();
        let call_id = k["inputs"]["callId"].as_str().unwrap();
        let lid = k["inputs"]["peerLid"].as_str().unwrap();
        assert_eq!(
            derive_wasm_participant_ssrc(call_id, lid, 0) as u64,
            k["voip_crypto"]["ssrc_slot0"].as_u64().unwrap()
        );
        assert_eq!(
            derive_wasm_participant_ssrc(call_id, lid, 1) as u64,
            k["voip_crypto"]["ssrc_slot1"].as_u64().unwrap()
        );
    }

    #[test]
    fn format_participant_id_rules() {
        assert_eq!(format_e2e_srtp_participant_id("12345@lid"), "12345:0@lid");
        assert_eq!(format_e2e_srtp_participant_id("12345:6@lid"), "12345:6@lid");
        assert_eq!(
            format_e2e_srtp_participant_id("12345@s.whatsapp.net"),
            "12345@s.whatsapp.net"
        );
    }
}
```

## Go envelope (signatures only)

```go
package rtp

// WasmRelayStreamSlotWords are the slot words for the 9-stream relay allocate plan.
var WasmRelayStreamSlotWords = [9]uint32{0, 1, 4, 2, 3, 5, 7, 8, 6}

func DeriveWasmParticipantSsrc(callID, lid string, slotWord uint32) uint32

func DeriveWasmRelayStreamSsrcs(callID, lid string) [9]uint32

func FormatE2ESrtpParticipantID(jid string) string

func E2EParticipantIDVariants(jid string) []string
```

## Implementation suggestions (guidance, not authoritative)

- HKDF-SHA256 is `golang.org/x/crypto/hkdf` with `sha256.New`. The salt is the
  slot word as 4 little-endian bytes, the IKM is the call id, the `info` is the LID,
  and exactly 4 bytes of output key material are read.
- The output 4 bytes are decoded little-endian into the SSRC (`u32::from_le_bytes`);
  use `binary.LittleEndian.Uint32`. Note salt is LE32 and the readback is also LE.
- `u32` → `uint32`; `[u32; 9]` → `[9]uint32`; `&str` → `string`; `Vec<String>` →
  `[]string`. These are pure functions with no error return — the HKDF expand cannot
  fail for 4 bytes, so panic-on-error in Rust maps to a `TODO(human)`: decide whether
  to `panic` or swallow the (impossible) error in Go.
- The string helpers operate on `rfind('@')` and `:` splits; mirror with
  `strings.LastIndex(bare, "@")` and `strings.Contains`. Strip any resource
  (`split('/').next()`) and `strings.TrimSpace` first, matching the source order.
- `e2e_participant_id_variants` deduplicates while preserving insertion order; in Go
  back the dedup with a `map[string]bool` guard plus an ordered `[]string`, and skip
  empty (post-trim) entries exactly as the closure does.
- The `:0` default-device suffix is only appended when the domain is literally `lid`;
  other domains (e.g. `s.whatsapp.net`) pass through unchanged.
```
