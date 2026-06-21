<!-- Datasheet = three things only: the reference source VERBATIM, the Go envelope
     (signatures, no bodies), and implementation suggestions. No behavioral summary,
     no implementation. The verbatim source is the only authoritative content. -->

# Datasheet: `srtp/hbh`

Hop-by-hop SRTP. Keying + transport layer: derives master keying material from a
30-byte hop-by-hop key with a two-stage HKDF, expands the libsrtp session keys,
builds the AES-ICM nonce, and encrypts/decrypts RTP payloads with libsrtp-style
AES-ICM.

**Validation vector:** `kats.json` — known-answer vectors (`inputs.hbhKey`,
`inputs.ssrc`, `inputs.seq`, `inputs.roc`, `inputs.payload`, and the expected
`hbh_srtp.*` fields). Copy it verbatim into `srtp/testdata/`.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust `wacore/src/voip/`).

## Reference source (verbatim — authoritative)


```rust
//! HBH (hop-by-hop) SRTP: legacy/fallback path keyed from the signaling `hbh_key`
//! (30B) via HKDF-SHA256 (label as `info`), then the libsrtp AES-ICM session-key expansion.
//!
//! The counter here is libsrtp's AES-ICM with a 2-byte carry (bytes 15 then 14),
//! NOT a full 128-bit CTR; it only diverges past ~1 MiB/packet (impossible for
//! audio), but is reproduced faithfully so vectors match.

use aes::Aes128;
use aes::cipher::{Block, BlockCipherEncrypt, KeyInit};

use crate::voip::hkdf_sha256;

const NULL_SALT_32: [u8; 32] = [0u8; 32];

const LABEL_RTP_ENCRYPTION: u8 = 0x00;
const LABEL_RTP_AUTH: u8 = 0x01;
const LABEL_RTP_SALT: u8 = 0x02;

/// 16B master key + 14B master salt.
#[derive(Clone, Debug)]
pub struct SrtpKeyingMaterial {
    pub master_key: [u8; 16],
    pub master_salt: [u8; 14],
}

/// Expanded per-session keys (AES_CM_128_HMAC_SHA1_80).
#[derive(Clone, Debug)]
pub struct LibsrtpSessionKeys {
    pub session_key: [u8; 16],
    pub session_salt: [u8; 14],
    pub auth_key: [u8; 20],
}

fn keying_from_crypto_key(crypto_key: &[u8]) -> SrtpKeyingMaterial {
    let mut m = SrtpKeyingMaterial {
        master_key: [0u8; 16],
        master_salt: [0u8; 14],
    };
    m.master_key.copy_from_slice(&crypto_key[0..16]);
    m.master_salt.copy_from_slice(&crypto_key[16..30]);
    m
}

/// mbedtls/Android hbh_key split: 16B master_key + 14B master_salt, two-stage KDF.
/// Returns `None` on malformed relay input (the only valid `hbh_key` is exactly 30 bytes).
fn derive_hbh_srtp_key_with_labels(
    hbh_key: &[u8],
    salt_label: &str,
    key_label: &str,
) -> Option<Vec<u8>> {
    if hbh_key.len() != 30 {
        return None;
    }
    let master_key = &hbh_key[0..16];
    let master_salt = &hbh_key[16..30];
    // WA SFU KDF == HKDF-SHA256 with the literal UTF-8 label as `info`.
    let srtcp_salt = hkdf_sha256(&NULL_SALT_32, master_salt, salt_label.as_bytes(), 32);
    Some(hkdf_sha256(
        &srtcp_salt,
        master_key,
        key_label.as_bytes(),
        30,
    ))
}

pub fn derive_hbh_srtp_key_uplink(hbh_key: &[u8]) -> Option<Vec<u8>> {
    derive_hbh_srtp_key_with_labels(hbh_key, "uplink hbh srtcp salt", "uplink hbh srtcp key")
}

pub fn derive_hbh_srtp_key_downlink(hbh_key: &[u8]) -> Option<Vec<u8>> {
    derive_hbh_srtp_key_with_labels(hbh_key, "downlink hbh srtcp salt", "downlink hbh srtcp key")
}

pub fn keying_from_hbh_key_uplink(hbh_key: &[u8]) -> Option<SrtpKeyingMaterial> {
    Some(keying_from_crypto_key(&derive_hbh_srtp_key_uplink(
        hbh_key,
    )?))
}

pub fn keying_from_hbh_key_downlink(hbh_key: &[u8]) -> Option<SrtpKeyingMaterial> {
    Some(keying_from_crypto_key(&derive_hbh_srtp_key_downlink(
        hbh_key,
    )?))
}

/// 30-byte libsrtp AES-ICM key: 16B AES key followed by the 14B salt.
fn aes_icm_key30(aes_key: &[u8], salt: &[u8]) -> [u8; 30] {
    let mut out = [0u8; 30];
    out[..16].copy_from_slice(&aes_key[..16]);
    out[16..30].copy_from_slice(&salt[..14]);
    out
}

/// libsrtp AES-ICM: counter = (salt zero-padded to 16) XOR iv, keystream = AES(counter),
/// counter increments byte 15 with a single carry into byte 14 (2-level, not 128-bit).
fn aes_icm_crypt(key30: &[u8], iv16: &[u8], data: &[u8]) -> Vec<u8> {
    let aes_key = &key30[..16];
    let salt = &key30[16..30];
    let mut counter = [0u8; 16];
    counter[..14].copy_from_slice(salt);
    for i in 0..16 {
        counter[i] ^= iv16[i];
    }
    let cipher = Aes128::new_from_slice(aes_key).expect("16-byte AES key");
    let mut out = data.to_vec();
    let mut pos = 0;
    while pos < out.len() {
        let mut ks = Block::<Aes128>::from(counter);
        cipher.encrypt_block(&mut ks);
        let n = core::cmp::min(16, out.len() - pos);
        for i in 0..n {
            out[pos + i] ^= ks[i];
        }
        pos += n;
        counter[15] = counter[15].wrapping_add(1);
        if counter[15] == 0 {
            counter[14] = counter[14].wrapping_add(1);
        }
    }
    out
}

/// libsrtp srtp_kdf_generate: IV is all-zero except byte 7 = label.
fn derive_session_bytes(master_key: &[u8], master_salt: &[u8], label: u8, len: usize) -> Vec<u8> {
    let kdf_key = aes_icm_key30(master_key, master_salt);
    let mut iv = [0u8; 16];
    iv[7] = label;
    aes_icm_crypt(&kdf_key, &iv, &vec![0u8; len])
}

/// libsrtp session-key expansion (labels 0x00 enc, 0x01 auth, 0x02 salt).
pub fn expand_libsrtp_session_keys(keying: &SrtpKeyingMaterial) -> LibsrtpSessionKeys {
    let mut out = LibsrtpSessionKeys {
        session_key: [0u8; 16],
        session_salt: [0u8; 14],
        auth_key: [0u8; 20],
    };
    out.session_key.copy_from_slice(&derive_session_bytes(
        &keying.master_key,
        &keying.master_salt,
        LABEL_RTP_ENCRYPTION,
        16,
    ));
    out.session_salt.copy_from_slice(&derive_session_bytes(
        &keying.master_key,
        &keying.master_salt,
        LABEL_RTP_SALT,
        14,
    ));
    out.auth_key.copy_from_slice(&derive_session_bytes(
        &keying.master_key,
        &keying.master_salt,
        LABEL_RTP_AUTH,
        20,
    ));
    out
}

/// RTP AES-ICM nonce: zero, SSRC at bytes 4-7 (BE), (packet_index << 16) at bytes 8-15 (BE).
pub fn build_rtp_icm_nonce(ssrc: u32, packet_index: u64) -> [u8; 16] {
    let mut iv = [0u8; 16];
    iv[4..8].copy_from_slice(&ssrc.to_be_bytes());
    iv[8..16].copy_from_slice(&packet_index.wrapping_shl(16).to_be_bytes());
    iv
}

/// Encrypt/decrypt an RTP payload with the expanded session key (symmetric).
pub fn crypt_rtp_payload(
    session: &LibsrtpSessionKeys,
    ssrc: u32,
    packet_index: u64,
    payload: &[u8],
) -> Vec<u8> {
    let icm_key = aes_icm_key30(&session.session_key, &session.session_salt);
    let nonce = build_rtp_icm_nonce(ssrc, packet_index);
    aes_icm_crypt(&icm_key, &nonce, payload)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::voip::testkat::{hexd, kats};

    #[test]
    fn hbh_uplink_derivation_matches_kat() {
        let k = kats();
        let hbh = hexd(&k, &["inputs", "hbhKey"]);
        let uplink = derive_hbh_srtp_key_uplink(&hbh).unwrap();
        assert_eq!(
            hex::encode(&uplink),
            k["hbh_srtp"]["uplinkKey30"].as_str().unwrap()
        );

        let keying = keying_from_hbh_key_uplink(&hbh).unwrap();
        assert_eq!(
            hex::encode(keying.master_key),
            k["hbh_srtp"]["masterKey"].as_str().unwrap()
        );
        assert_eq!(
            hex::encode(keying.master_salt),
            k["hbh_srtp"]["masterSalt"].as_str().unwrap()
        );

        let session = expand_libsrtp_session_keys(&keying);
        assert_eq!(
            hex::encode(session.session_key),
            k["hbh_srtp"]["sessionKey"].as_str().unwrap()
        );
        assert_eq!(
            hex::encode(session.session_salt),
            k["hbh_srtp"]["sessionSalt"].as_str().unwrap()
        );
        assert_eq!(
            hex::encode(session.auth_key),
            k["hbh_srtp"]["authKey"].as_str().unwrap()
        );
    }

    #[test]
    fn hbh_icm_nonce_and_cipher_match_kat() {
        let k = kats();
        let hbh = hexd(&k, &["inputs", "hbhKey"]);
        let session = expand_libsrtp_session_keys(&keying_from_hbh_key_uplink(&hbh).unwrap());
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        let seq = k["inputs"]["seq"].as_u64().unwrap();
        let roc = k["inputs"]["roc"].as_u64().unwrap();
        let packet_index = (roc << 16) | seq;

        let nonce = build_rtp_icm_nonce(ssrc, packet_index);
        assert_eq!(
            hex::encode(nonce),
            k["hbh_srtp"]["rtpIcmNonce"].as_str().unwrap()
        );

        let payload = hexd(&k, &["inputs", "payload"]);
        let ct = crypt_rtp_payload(&session, ssrc, packet_index, &payload);
        assert_eq!(
            hex::encode(&ct),
            k["hbh_srtp"]["cipher_out"].as_str().unwrap()
        );
        // Symmetric round-trip.
        assert_eq!(
            crypt_rtp_payload(&session, ssrc, packet_index, &ct),
            payload
        );
    }
}
```

## Go envelope (signatures only)

The corresponding Go declarations — exported types and function **signatures with
no bodies**. This is the surface to implement; it is not the implementation.

```go
package srtp

type SrtpKeyingMaterial struct {
	MasterKey  [16]byte
	MasterSalt [14]byte
}

type LibsrtpSessionKeys struct {
	SessionKey  [16]byte
	SessionSalt [14]byte
	AuthKey     [20]byte
}

func WaSfuKDF(label string, salt, ikm []byte, length int) []byte

func DeriveHbhSrtpKeyUplink(hbhKey []byte) []byte

func DeriveHbhSrtpKeyDownlink(hbhKey []byte) []byte

func KeyingFromHbhKeyUplink(hbhKey []byte) SrtpKeyingMaterial

func KeyingFromHbhKeyDownlink(hbhKey []byte) SrtpKeyingMaterial

func ExpandLibsrtpSessionKeys(keying *SrtpKeyingMaterial) LibsrtpSessionKeys

func BuildRtpICMNonce(ssrc uint32, packetIndex uint64) [16]byte

func CryptRtpPayload(session *LibsrtpSessionKeys, ssrc uint32, packetIndex uint64, payload []byte) []byte
```

## Implementation suggestions (guidance, not authoritative)

- The AES-ICM counter increment is deliberately NOT a 128-bit counter: it only
  carries from byte 15 into byte 14. Implement it exactly as written (two `wrapping_add`
  steps); do not substitute `cipher.NewCTR`, which would carry across all 16 bytes
  and diverge from the vectors. Use `crypto/aes` block encryption directly per
  16-byte block.
- `wrapping_shl(16)` on the `u64` packet index → Go `packetIndex << 16` on a `uint64`
  (Go shift on `uint64` already wraps mod 2^64). Then `binary.BigEndian.PutUint64`.
- `assert_eq!(hbh_key.len(), 30, ...)` is a hard invariant; `to_be_bytes` → use
  `encoding/binary` BigEndian. `TODO(human):` decide whether the 30-byte length
  check should panic or return an error in Go.
- The `wa_sfu_kdf` labels are literal ASCII strings (e.g. `"uplink hbh srtcp key"`);
  pass them as `[]byte(label)` info to the HKDF helper. The salt for stage 1 is a
  32-byte zero buffer.
- Fixed-size struct fields are arrays; the 30-byte concatenations (`aes_icm_key30`,
  derived keys) are slices/arrays you index — use the `copy` builtin for the
  16+14 byte splits.
