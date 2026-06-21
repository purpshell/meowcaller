# Datasheet: `stun/stun`

STUN/WARP relay framing: an RFC 5389 TLV encoder with MESSAGE-INTEGRITY
(HMAC-SHA1) and FINGERPRINT (CRC-32), the allocate-request builders, the consent
ping, and the response parsers. Transport layer.

**Validation vector:** (integration — no single vector). Pinned by the inline
`#[test]` cases below against the shared `kats()` fixture (`crc32_abc`,
`relayToken`/`attr_token`, `xorEndpoint`, `nativeSenderSub`, `minimalMi`, `withFp`,
`wasmAllocate`, `ping`, and the `stun_proto.*` protobuf payloads). Copy that fixture
JSON verbatim into `stun/testdata/`.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust `wacore/src/voip/`).

## Reference source (verbatim — authoritative)

```rust
//! STUN/WARP relay framing: RFC 5389 TLV encoder with WhatsApp's MESSAGE-INTEGRITY
//! (HMAC-SHA1) and FINGERPRINT (CRC-32), the non-protobuf allocate builders, the
//! WhatsApp ping, and the response parsers.
//!
//! Transaction IDs are passed in (the I/O layer supplies 12 random bytes) so this stays
//! pure and deterministically testable. Protobuf-based allocate builders (0x4024/0x4025
//! dynamic) come with the waproto voip schemas.

use hmac::{Hmac, KeyInit, Mac};
use sha1::Sha1;

type HmacSha1 = Hmac<Sha1>;

const STUN_MAGIC: u32 = 0x2112_a442;
const STUN_FINGERPRINT_XOR: u32 = 0x5354_554e;
const STUN_XOR_PORT: u16 = 0x2112;
const STUN_XOR_ADDR: [u8; 4] = [0x21, 0x12, 0xa4, 0x42];

const ATTR_MESSAGE_INTEGRITY: u16 = 0x0008;
const ATTR_FINGERPRINT: u16 = 0x8028;
const ATTR_ERROR_CODE: u16 = 0x0009;
const ATTR_RELAY_TOKEN: u16 = 0x4000;
const STUN_ATTR_STREAM_DESCRIPTORS: u16 = 0x4024;
const STUN_ATTR_WASM_RELAY_ENDPOINT: u16 = 0x0016;

pub const MSG_BINDING_REQUEST: u16 = 0x0001;
pub const MSG_ALLOCATE_REQUEST: u16 = 0x0003;
pub const MSG_BINDING_SUCCESS: u16 = 0x0101;
pub const MSG_ALLOCATE_SUCCESS: u16 = 0x0103;
pub const MSG_ALLOCATE_ERROR: u16 = 0x0113;
pub const MSG_WHATSAPP_PING: u16 = 0x0801;
pub const MSG_WHATSAPP_PONG: u16 = 0x0802;

/// WASM/Web StreamDescriptors template (attr 0x4024): auxiliary stream SSRCs.
const WASM_STREAM_DESCRIPTORS_TEMPLATE: &[u8] = &[
    0x0a, 0x06, 0x18, 0xca, 0xbc, 0x85, 0xae, 0x04, 0x0a, 0x08, 0x10, 0x01, 0x18, 0xa5, 0xac, 0xaf,
    0xae, 0x0a, 0x0a, 0x08, 0x10, 0x02, 0x18, 0xd6, 0xa4, 0xe6, 0xf9, 0x0f, 0x0a, 0x08, 0x08, 0x01,
    0x18, 0xf7, 0xdd, 0x9e, 0xb6, 0x0a, 0x0a, 0x0a, 0x08, 0x01, 0x10, 0x01, 0x18, 0xab, 0xcc, 0xb1,
    0xf3, 0x0d, 0x0a, 0x0a, 0x08, 0x01, 0x10, 0x02, 0x18, 0xda, 0xda, 0xef, 0x8a, 0x05, 0x0a, 0x08,
    0x08, 0x02, 0x18, 0xc5, 0xe9, 0xec, 0x8e, 0x0b, 0x0a, 0x0a, 0x08, 0x02, 0x10, 0x01, 0x18, 0xfd,
    0xc2, 0xb1, 0xb6, 0x0f, 0x0a, 0x0a, 0x08, 0x02, 0x10, 0x02, 0x18, 0xb0, 0x97, 0xf7, 0xb2, 0x09,
];

fn pad4(n: usize) -> usize {
    (4 - (n % 4)) % 4
}

/// Encode a single STUN attribute (type, length, value, 4-byte alignment padding).
fn stun_attr(attr_type: u16, value: &[u8]) -> Vec<u8> {
    let pad = pad4(value.len());
    let mut buf = Vec::with_capacity(4 + value.len() + pad);
    buf.extend_from_slice(&attr_type.to_be_bytes());
    buf.extend_from_slice(&(value.len() as u16).to_be_bytes());
    buf.extend_from_slice(value);
    buf.resize(buf.len() + pad, 0);
    buf
}

/// CRC-32 (IEEE, reflected, poly 0xedb88320) for the STUN FINGERPRINT.
fn crc32(buf: &[u8]) -> u32 {
    let mut crc: u32 = 0xffff_ffff;
    for &b in buf {
        crc ^= b as u32;
        for _ in 0..8 {
            crc = (crc >> 1) ^ (0xedb8_8320 & 0u32.wrapping_sub(crc & 1));
        }
    }
    !crc
}

fn stun_pseudo_header(msg_type: u16, msg_len: u16, transaction_id: &[u8; 12]) -> [u8; 20] {
    let mut h = [0u8; 20];
    h[0..2].copy_from_slice(&msg_type.to_be_bytes());
    h[2..4].copy_from_slice(&msg_len.to_be_bytes());
    h[4..8].copy_from_slice(&STUN_MAGIC.to_be_bytes());
    h[8..20].copy_from_slice(transaction_id);
    h
}

/// Encode a STUN request per RFC 5389: header + attrs, then optional MESSAGE-INTEGRITY
/// (over a pseudo-header whose length already counts the MI attr) and FINGERPRINT.
pub fn encode_stun_request(
    msg_type: u16,
    transaction_id: &[u8; 12],
    attrs: &[u8],
    integrity_key: Option<&[u8]>,
    include_fingerprint: bool,
) -> Vec<u8> {
    let mut body = attrs.to_vec();

    if let Some(key) = integrity_key {
        let msg_len = (body.len() + 24) as u16; // attrs + MI attr (4 + 20)
        let header = stun_pseudo_header(msg_type, msg_len, transaction_id);
        let mut mac = HmacSha1::new_from_slice(key).expect("HMAC accepts any key length");
        mac.update(&header);
        mac.update(&body);
        let mi = mac.finalize().into_bytes(); // 20 bytes
        body.extend_from_slice(&stun_attr(ATTR_MESSAGE_INTEGRITY, &mi));
    }

    if include_fingerprint {
        let msg_len = (body.len() + 8) as u16; // attrs + FINGERPRINT attr (4 + 4)
        let header = stun_pseudo_header(msg_type, msg_len, transaction_id);
        let mut crc_input = Vec::with_capacity(20 + body.len());
        crc_input.extend_from_slice(&header);
        crc_input.extend_from_slice(&body);
        let fp = crc32(&crc_input) ^ STUN_FINGERPRINT_XOR;
        body.extend_from_slice(&stun_attr(ATTR_FINGERPRINT, &fp.to_be_bytes()));
    }

    let mut out = Vec::with_capacity(20 + body.len());
    out.extend_from_slice(&msg_type.to_be_bytes());
    out.extend_from_slice(&(body.len() as u16).to_be_bytes());
    out.extend_from_slice(&STUN_MAGIC.to_be_bytes());
    out.extend_from_slice(transaction_id);
    out.extend_from_slice(&body);
    out
}

/// Native WA sender subscription: 1-byte count + big-endian SSRC (attr 0x4023).
pub fn create_native_sender_subscription(ssrc: u32) -> [u8; 5] {
    let mut buf = [0u8; 5];
    buf[0] = 1;
    buf[1..5].copy_from_slice(&ssrc.to_be_bytes());
    buf
}

/// XOR-encoded IPv4:port (6 bytes) for the WASM relay endpoint attr.
pub fn encode_xor_relay_endpoint(ipv4: &str, port: u16) -> Option<[u8; 6]> {
    let octets: Vec<u8> = ipv4
        .split('.')
        .filter_map(|n| n.parse::<u8>().ok())
        .collect();
    if octets.len() != 4 {
        return None;
    }
    let xor_port = port ^ STUN_XOR_PORT;
    let mut buf = [0u8; 6];
    buf[0..2].copy_from_slice(&xor_port.to_be_bytes());
    for i in 0..4 {
        buf[2 + i] = octets[i] ^ STUN_XOR_ADDR[i];
    }
    Some(buf)
}

/// WASM attr 0x0016 value: `00 01` followed by the 6-byte XOR relay endpoint.
fn create_wasm_relay_endpoint_attr(endpoint_xor: &[u8; 6]) -> [u8; 8] {
    let mut buf = [0u8; 8];
    buf[0..2].copy_from_slice(&1u16.to_be_bytes());
    buf[2..8].copy_from_slice(endpoint_xor);
    buf
}

/// WASM/Web DataChannel Allocate: 0x4000 token + 0x4024 stream desc + 0x0016 endpoint + MI, no FP.
pub fn build_wasm_stun_allocate_request(
    transaction_id: &[u8; 12],
    relay_token: &[u8],
    endpoint_xor: &[u8; 6],
    integrity_key: &[u8],
) -> Vec<u8> {
    let mut attrs = stun_attr(ATTR_RELAY_TOKEN, relay_token);
    attrs.extend_from_slice(&stun_attr(
        STUN_ATTR_STREAM_DESCRIPTORS,
        WASM_STREAM_DESCRIPTORS_TEMPLATE,
    ));
    attrs.extend_from_slice(&stun_attr(
        STUN_ATTR_WASM_RELAY_ENDPOINT,
        &create_wasm_relay_endpoint_attr(endpoint_xor),
    ));
    encode_stun_request(
        MSG_ALLOCATE_REQUEST,
        transaction_id,
        &attrs,
        Some(integrity_key),
        false,
    )
}

/// WhatsApp consent ping (type 0x0801, empty body).
pub fn build_whatsapp_ping(transaction_id: &[u8; 12]) -> [u8; 20] {
    let mut out = [0u8; 20];
    out[0..2].copy_from_slice(&MSG_WHATSAPP_PING.to_be_bytes());
    out[4..8].copy_from_slice(&STUN_MAGIC.to_be_bytes());
    out[8..20].copy_from_slice(transaction_id);
    out
}

pub fn is_stun_packet(data: &[u8]) -> bool {
    data.len() >= 2 && (data[0] & 0xc0) == 0x00
}

pub fn stun_message_type(data: &[u8]) -> Option<u16> {
    (data.len() >= 2).then(|| (((data[0] & 0x3f) as u16) << 8) | data[1] as u16)
}

pub fn stun_transaction_id(data: &[u8]) -> Option<&[u8]> {
    (data.len() >= 20).then(|| &data[8..20])
}

pub fn is_allocate_or_binding_success(data: &[u8]) -> bool {
    if !is_stun_packet(data) || data.len() < 20 {
        return false;
    }
    matches!(
        stun_message_type(data),
        Some(MSG_ALLOCATE_SUCCESS | MSG_BINDING_SUCCESS)
    )
}

pub fn is_allocate_error(data: &[u8]) -> bool {
    is_stun_packet(data) && stun_message_type(data) == Some(MSG_ALLOCATE_ERROR)
}

pub fn is_whatsapp_pong(data: &[u8], transaction_id: Option<&[u8]>) -> bool {
    if !is_stun_packet(data) || stun_message_type(data) != Some(MSG_WHATSAPP_PONG) {
        return false;
    }
    match transaction_id {
        None | Some(&[]) => true,
        Some(want) => stun_transaction_id(data) == Some(want),
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StunAttribute {
    pub attr_type: u16,
    pub value: Vec<u8>,
}

/// Parse the STUN attributes after the 20-byte header.
pub fn parse_stun_attributes(data: &[u8]) -> Vec<StunAttribute> {
    if !is_stun_packet(data) || data.len() < 20 {
        return Vec::new();
    }
    let mut attrs = Vec::new();
    let mut off = 20;
    while off + 4 <= data.len() {
        let attr_type = ((data[off] as u16) << 8) | data[off + 1] as u16;
        let len = ((data[off + 2] as usize) << 8) | data[off + 3] as usize;
        off += 4;
        if off + len > data.len() {
            break;
        }
        attrs.push(StunAttribute {
            attr_type,
            value: data[off..off + len].to_vec(),
        });
        off += len + pad4(len);
    }
    attrs
}

/// Parse the numeric error code (class*100 + number) from an Allocate-error response.
pub fn parse_stun_error_code(data: &[u8]) -> Option<u16> {
    if data.len() < 20 {
        return None;
    }
    let t = stun_message_type(data)?;
    if t != MSG_ALLOCATE_ERROR && t != 0x0111 {
        return None;
    }
    let body_len = ((data[2] as usize) << 8) | data[3] as usize;
    let end = (20 + body_len).min(data.len());
    let mut off = 20;
    while off + 4 <= end {
        let attr_type = ((data[off] as u16) << 8) | data[off + 1] as u16;
        let len = ((data[off + 2] as usize) << 8) | data[off + 3] as usize;
        if attr_type == ATTR_ERROR_CODE && len >= 4 && off + 8 <= data.len() {
            let class = data[off + 6] as u16;
            let number = data[off + 7] as u16;
            return Some(class * 100 + number);
        }
        off += 4 + len + pad4(len);
    }
    None
}

const ATTR_SENDER_SUBSCRIPTIONS_V2: u16 = 0x4025;

// --- Minimal protobuf wire encoding for the STUN subscription attrs ---

use crate::voip::encode_varint as pb_varint;

fn pb_tag(out: &mut Vec<u8>, field: u32, wire: u32) {
    pb_varint(out, ((field << 3) | wire) as u64);
}

fn pb_zigzag(n: i64) -> u64 {
    ((n << 1) ^ (n >> 63)) as u64
}

fn pb_len_delim(out: &mut Vec<u8>, field: u32, bytes: &[u8]) {
    pb_tag(out, field, 2);
    pb_varint(out, bytes.len() as u64);
    out.extend_from_slice(bytes);
}

/// `voip.SenderSubscriptions` (WASM, STUN attr 0x4000): one audio sender (ssrc as uint32).
pub fn create_voip_sender_subscriptions(ssrc: u32) -> Vec<u8> {
    let mut sender = Vec::new();
    pb_tag(&mut sender, 3, 0); // ssrc
    pb_varint(&mut sender, ssrc as u64);
    pb_tag(&mut sender, 5, 0); // stream_layer = AUDIO(0)
    pb_varint(&mut sender, 0);
    pb_tag(&mut sender, 6, 0); // payload_type = MEDIA(0)
    pb_varint(&mut sender, 0);
    let mut out = Vec::new();
    pb_len_delim(&mut out, 1, &sender); // senders[0]
    out
}

/// `wa.voip.SenderSubscriptions` (APK, STUN attr 0x4025): one audio ssrc (sint64), optional pid.
pub fn create_apk_sender_subscriptions(ssrc: u32, pid: Option<u32>) -> Vec<u8> {
    let mut ssrc_layers = Vec::new();
    pb_tag(&mut ssrc_layers, 1, 0); // ssrcs[0] (sint64, zigzag)
    pb_varint(&mut ssrc_layers, pb_zigzag(ssrc as i64));
    if let Some(pid) = pid {
        let mut p = Vec::new();
        pb_tag(&mut p, 1, 0); // pid (sint64)
        pb_varint(&mut p, pb_zigzag(pid as i64));
        pb_len_delim(&mut p, 2, b"audio"); // layerId
        pb_len_delim(&mut ssrc_layers, 2, &p); // pids[0]
    }
    let mut ext = Vec::new();
    pb_len_delim(&mut ext, 1, &ssrc_layers); // ssrcLayers
    let mut out = Vec::new();
    pb_len_delim(&mut out, 1, &ext); // subscriptions[0]
    out
}

/// `wa.voip.StreamDescriptors` (APK, STUN attr 0x4024): one audio/OPUS descriptor (ssrc sint64).
pub fn create_apk_stream_descriptors(ssrc: u32) -> Vec<u8> {
    let mut sd = Vec::new();
    pb_len_delim(&mut sd, 1, b"audio"); // stream_layer
    pb_len_delim(&mut sd, 2, b"OPUS"); // payload_type
    pb_tag(&mut sd, 3, 0); // ssrc (sint64)
    pb_varint(&mut sd, pb_zigzag(ssrc as i64));
    pb_tag(&mut sd, 4, 0); // is_uplink_prefetch_enabled = false
    pb_varint(&mut sd, 0);
    let mut out = Vec::new();
    pb_len_delim(&mut out, 1, &sd); // stream_descriptors[0]
    out
}

/// APK Allocate: 0x4000 token + 0x4025 sender subs + 0x4024 stream desc + MI.
pub fn build_android_stun_allocate_request(
    transaction_id: &[u8; 12],
    relay_token: &[u8],
    ssrc: u32,
    pid: Option<u32>,
    integrity_key: &[u8],
    include_fingerprint: bool,
) -> Vec<u8> {
    let mut attrs = stun_attr(ATTR_RELAY_TOKEN, relay_token);
    attrs.extend_from_slice(&stun_attr(
        ATTR_SENDER_SUBSCRIPTIONS_V2,
        &create_apk_sender_subscriptions(ssrc, pid),
    ));
    attrs.extend_from_slice(&stun_attr(
        STUN_ATTR_STREAM_DESCRIPTORS,
        &create_apk_stream_descriptors(ssrc),
    ));
    encode_stun_request(
        MSG_ALLOCATE_REQUEST,
        transaction_id,
        &attrs,
        Some(integrity_key),
        include_fingerprint,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::voip::testkat::{hexd, kats};

    fn tx12(k: &serde_json::Value) -> [u8; 12] {
        let mut tx = [0u8; 12];
        tx.copy_from_slice(&hexd(k, &["stun", "tx"]));
        tx
    }

    #[test]
    fn crc32_is_ieee() {
        let k = kats();
        assert_eq!(
            crc32(b"abc") as u64,
            k["stun"]["crc32_abc"].as_u64().unwrap()
        );
        assert_eq!(crc32(b"abc"), 0x3524_41c2);
    }

    #[test]
    fn attr_and_endpoint_match_kat() {
        let k = kats();
        let token = hexd(&k, &["stun", "relayToken"]);
        assert_eq!(
            hex::encode(stun_attr(ATTR_RELAY_TOKEN, &token)),
            k["stun"]["attr_token"].as_str().unwrap()
        );
        let ep = encode_xor_relay_endpoint("157.240.226.133", 3478).unwrap();
        assert_eq!(hex::encode(ep), k["stun"]["xorEndpoint"].as_str().unwrap());
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        assert_eq!(
            hex::encode(create_native_sender_subscription(ssrc)),
            k["stun"]["nativeSenderSub"].as_str().unwrap()
        );
    }

    #[test]
    fn encode_request_mi_and_fingerprint_match_kat() {
        let k = kats();
        let tx = tx12(&k);
        let token = hexd(&k, &["stun", "relayToken"]);
        let mi_key = hexd(&k, &["stun", "miKey"]);
        let attrs = stun_attr(ATTR_RELAY_TOKEN, &token);
        let minimal = encode_stun_request(MSG_ALLOCATE_REQUEST, &tx, &attrs, Some(&mi_key), false);
        assert_eq!(
            hex::encode(&minimal),
            k["stun"]["minimalMi"].as_str().unwrap()
        );
        let with_fp = encode_stun_request(MSG_ALLOCATE_REQUEST, &tx, &attrs, Some(&mi_key), true);
        assert_eq!(hex::encode(&with_fp), k["stun"]["withFp"].as_str().unwrap());
    }

    #[test]
    fn wasm_allocate_and_ping_match_kat() {
        let k = kats();
        let tx = tx12(&k);
        let token = hexd(&k, &["stun", "relayToken"]);
        let mi_key = hexd(&k, &["stun", "miKey"]);
        let ep = encode_xor_relay_endpoint("157.240.226.133", 3478).unwrap();
        let alloc = build_wasm_stun_allocate_request(&tx, &token, &ep, &mi_key);
        assert_eq!(
            hex::encode(&alloc),
            k["stun"]["wasmAllocate"].as_str().unwrap()
        );
        assert_eq!(
            hex::encode(build_whatsapp_ping(&tx)),
            k["stun"]["ping"].as_str().unwrap()
        );
    }

    #[test]
    fn parse_round_trips_attributes() {
        let k = kats();
        let minimal = hexd(&k, &["stun", "minimalMi"]);
        assert!(is_stun_packet(&minimal));
        assert_eq!(stun_message_type(&minimal), Some(MSG_ALLOCATE_REQUEST));
        let attrs = parse_stun_attributes(&minimal);
        // relay token (0x4000) + message-integrity (0x0008)
        assert_eq!(attrs.len(), 2);
        assert_eq!(attrs[0].attr_type, ATTR_RELAY_TOKEN);
        assert_eq!(attrs[0].value, hexd(&k, &["stun", "relayToken"]));
        assert_eq!(attrs[1].attr_type, ATTR_MESSAGE_INTEGRITY);
        assert_eq!(attrs[1].value.len(), 20);
    }

    #[test]
    fn protobuf_payloads_match_kat() {
        let k = kats();
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        assert_eq!(
            hex::encode(create_voip_sender_subscriptions(ssrc)),
            k["stun_proto"]["voip_sender_subscriptions"]
                .as_str()
                .unwrap()
        );
        assert_eq!(
            hex::encode(create_apk_sender_subscriptions(ssrc, None)),
            k["stun_proto"]["apk_sender_subscriptions_nopid"]
                .as_str()
                .unwrap()
        );
        assert_eq!(
            hex::encode(create_apk_sender_subscriptions(ssrc, Some(7))),
            k["stun_proto"]["apk_sender_subscriptions_pid"]
                .as_str()
                .unwrap()
        );
        assert_eq!(
            hex::encode(create_apk_stream_descriptors(ssrc)),
            k["stun_proto"]["apk_stream_descriptors"].as_str().unwrap()
        );
    }

    #[test]
    fn android_allocate_carries_three_attrs() {
        let k = kats();
        let tx = tx12(&k);
        let token = hexd(&k, &["stun", "relayToken"]);
        let mi_key = hexd(&k, &["stun", "miKey"]);
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        let pkt = build_android_stun_allocate_request(&tx, &token, ssrc, None, &mi_key, false);
        let attrs = parse_stun_attributes(&pkt);
        // 0x4000 token, 0x4025 sender subs, 0x4024 stream desc, 0x0008 MI
        assert_eq!(attrs[0].attr_type, ATTR_RELAY_TOKEN);
        assert_eq!(attrs[1].attr_type, ATTR_SENDER_SUBSCRIPTIONS_V2);
        assert_eq!(attrs[2].attr_type, STUN_ATTR_STREAM_DESCRIPTORS);
        assert_eq!(attrs[3].attr_type, ATTR_MESSAGE_INTEGRITY);
        assert_eq!(attrs[2].value, create_apk_stream_descriptors(ssrc));
    }

    #[test]
    fn pong_matching() {
        let k = kats();
        let tx = tx12(&k);
        let mut pong = build_whatsapp_ping(&tx).to_vec();
        pong[0..2].copy_from_slice(&MSG_WHATSAPP_PONG.to_be_bytes());
        assert!(is_whatsapp_pong(&pong, Some(&tx)));
        assert!(is_whatsapp_pong(&pong, None));
        let wrong_tx = [0u8; 12];
        assert!(!is_whatsapp_pong(&pong, Some(&wrong_tx)));
    }
}
```

## Go envelope (signatures only)

```go
package stun

const (
	MsgBindingRequest  uint16 = 0x0001
	MsgAllocateRequest uint16 = 0x0003
	MsgBindingSuccess  uint16 = 0x0101
	MsgAllocateSuccess uint16 = 0x0103
	MsgAllocateError   uint16 = 0x0113
	MsgWhatsappPing    uint16 = 0x0801
	MsgWhatsappPong    uint16 = 0x0802
)

func EncodeStunRequest(msgType uint16, transactionID [12]byte, attrs []byte, integrityKey []byte, includeFingerprint bool) []byte

func CreateNativeSenderSubscription(ssrc uint32) [5]byte

func EncodeXorRelayEndpoint(ipv4 string, port uint16) ([6]byte, bool)

func BuildWasmStunAllocateRequest(transactionID [12]byte, relayToken []byte, endpointXor [6]byte, integrityKey []byte) []byte

func BuildNativeStunAllocateRequest(transactionID [12]byte, relayToken []byte, ssrc uint32, integrityKey []byte, includeSubscription, includeFingerprint bool) []byte

func BuildMinimalStunAllocateRequest(transactionID [12]byte, relayToken []byte, integrityKey []byte, includeFingerprint bool) []byte

func BuildWhatsappPing(transactionID [12]byte) [20]byte

func IsStunPacket(data []byte) bool

func StunMessageType(data []byte) uint16

func StunTransactionID(data []byte) []byte

func IsAllocateOrBindingSuccess(data []byte) bool

func IsAllocateError(data []byte) bool

func IsWhatsappPong(data []byte, transactionID []byte) bool

type StunAttribute struct {
	AttrType uint16
	Value    []byte
}

func ParseStunAttributes(data []byte) []StunAttribute

func ParseStunErrorCode(data []byte) (uint16, bool)

func CreateVoipSenderSubscriptions(ssrc uint32) []byte

func CreateApkSenderSubscriptions(ssrc uint32, pid *uint32) []byte

func CreateApkStreamDescriptors(ssrc uint32) []byte

func BuildAndroidStunAllocateRequest(transactionID [12]byte, relayToken []byte, ssrc uint32, pid *uint32, integrityKey []byte, includeFingerprint bool) []byte

func BuildRustStunAllocateRequest(transactionID [12]byte, username, senderSubscriptions, integrityKey []byte, includeFingerprint bool) []byte
```

## Implementation suggestions (guidance, not authoritative)

- All multi-byte STUN fields are big-endian (`to_be_bytes`); use `encoding/binary`
  with `binary.BigEndian`. The protobuf varints are the usual little-endian-7-bit
  encoding; keep those hand-rolled to match the source exactly.
- `Option<&[u8]>` integrity key → a `[]byte` where nil means "no MESSAGE-INTEGRITY".
  `Option<u32>` pid → `*uint32`. `Option<[u8; 6]>`/`Option<u16>` returns → a second
  `bool` "ok" return as shown.
- HMAC-SHA1 is `crypto/hmac` + `crypto/sha1`; the MI is the full 20-byte digest. The
  Rust `crc32` is the reflected IEEE poly `0xedb88320`; Go `hash/crc32.ChecksumIEEE`
  produces the same value, so the FINGERPRINT is `crc32.ChecksumIEEE(input) ^ 0x5354554e`.
  `TODO(human)`: decide whether to reuse stdlib CRC-32 or port the bitwise loop verbatim.
- Fixed-size returns (`[5]byte`, `[20]byte`, `[6]byte`, `[8]byte`) map to Go arrays;
  builders that return `Vec<u8>` map to `[]byte`. The pseudo-header `msg_len` already
  accounts for the trailing MI/FINGERPRINT attribute — preserve those `+24`/`+8` offsets.
- `pb_zigzag` is `(n<<1) ^ (n>>63)` on `i64`; in Go use `int64` and arithmetic shift,
  then store as `uint64`. The `ssrc as i64` widening must happen before zigzag.
- The error-code parser also accepts message type `0x0111` in addition to
  `MSG_ALLOCATE_ERROR`; preserve both. `pad4` is `(4 - (n % 4)) % 4` so a zero-length
  value adds no padding.
```
