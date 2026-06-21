# Datasheet: `rtp/rtp`

RTP/RTCP WARP framing: the 16-byte speech / 20-byte DTX headers (extension
profile 0xdebe), Opus payload classifiers, the send-side sequencer, plus the
compact RTCP reports and Sender Report. Transport/media layer.

**Validation vector:** (integration — no single vector). Pinned by the inline
`#[test]` cases below against the shared `kats()` fixture (`rtp.speechHeader16`,
`rtp.dtxHeader20`, `rtp.estimate*`, and `rtcp.compact208`/`compact209`/`senderReport`).
Copy that fixture JSON verbatim into `rtp/testdata/`.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust `wacore/src/voip/`).

## Reference source (verbatim — authoritative)

### `rtp.rs`

```rust
//! RTP WARP framing: WhatsApp's 16-byte speech / 20-byte DTX headers (extension
//! profile 0xdebe), Opus payload classifiers, and the send-side sequencer.

use crate::voip::warp::audio_piggyback_extension_for;

pub const RTP_PAYLOAD_TYPE_OPUS: u8 = 120;
pub const WHATSAPP_RTP_EXTENSION_PROFILE: u16 = 0xdebe;
pub const WHATSAPP_RTP_HEADER_SIZE: usize = 16;
pub const WHATSAPP_RTP_HEADER_DTX_SIZE: usize = 20;
pub const WHATSAPP_RTP_EXTENSION_DTX_WORD: u32 = 0x3001_0000;
const RTP_VERSION: u8 = 2;
const SRTP_AUTH_TAG_LEN: usize = 10;
const SRTP_AUTH_TAG_LEN_SHORT: usize = 4;

/// Android first priming frame (18 bytes).
pub const OPUS_PRIMING_FRAME_1: [u8; 18] = [
    0x12, 0x36, 0x26, 0x2b, 0x4a, 0xc8, 0x2b, 0x09, 0xc9, 0x1f, 0x34, 0xc2, 0xd6, 0x7a, 0x01, 0x73,
    0x1b, 0x2e,
];
/// WASM/Web caller priming (24 bytes).
pub const OPUS_PRIMING_FRAME_1_WASM: [u8; 24] = [
    0x32, 0x36, 0x26, 0x2b, 0x4a, 0xcb, 0x1b, 0x5f, 0xba, 0x91, 0x68, 0x7e, 0xb8, 0x50, 0x93, 0x58,
    0xe6, 0xd0, 0xa3, 0xa9, 0xd7, 0x1d, 0x81, 0x8c,
];
/// Second priming frame (5 bytes).
pub const OPUS_PRIMING_FRAME_2: [u8; 5] = [0x90, 0xb8, 0x14, 0x14, 0xc4];

pub fn is_whatsapp_opus_rtp_payload(payload_type: u8) -> bool {
    payload_type == RTP_PAYLOAD_TYPE_OPUS || payload_type == 121
}

/// DTX / comfort-noise: RFC `0x10`, mlow `0x90`, and short warmup silence frames.
pub fn is_opus_dtx_payload(payload: &[u8]) -> bool {
    match payload.len() {
        0 => false,
        1 => matches!(payload[0], 0x10 | 0x88 | 0x90),
        n if n <= 15 => {
            let b0 = payload[0];
            if (b0 & 0xf8) == 0x08 || b0 == 0x0a {
                return true;
            }
            (b0 & 0xf0) == 0x30 && n <= 6
        }
        _ => false,
    }
}

/// mlow speech frame (20 ms `0x48..0x4f` or 60 ms `0x50..0x57`).
pub fn is_opus_mlow_speech_payload(payload: &[u8]) -> bool {
    if payload.len() < 18 {
        return false;
    }
    let b0 = payload[0];
    (b0 & 0xf8) == 0x48 || (b0 & 0xf8) == 0x50
}

pub fn is_opus_priming_payload(payload: &[u8]) -> bool {
    payload == OPUS_PRIMING_FRAME_1 || payload == OPUS_PRIMING_FRAME_2
}

/// Estimate on-wire SRTP size (header + opus + auth tag) for ladder frame picking.
pub fn estimate_srtp_rtp_wire_bytes(opus_payload: &[u8]) -> usize {
    let dtx = is_opus_dtx_payload(opus_payload);
    let header_size = if dtx {
        WHATSAPP_RTP_HEADER_DTX_SIZE
    } else {
        WHATSAPP_RTP_HEADER_SIZE
    };
    let short_tag = (dtx && header_size == WHATSAPP_RTP_HEADER_DTX_SIZE)
        || (!dtx
            && header_size == WHATSAPP_RTP_HEADER_SIZE
            && (is_opus_priming_payload(opus_payload) || opus_payload.len() <= 18));
    let tag_len = if short_tag {
        SRTP_AUTH_TAG_LEN_SHORT
    } else {
        SRTP_AUTH_TAG_LEN
    };
    header_size + opus_payload.len() + tag_len
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RtpHeader {
    pub marker: bool,
    pub payload_type: u8,
    pub sequence_number: u16,
    pub timestamp: u32,
    pub ssrc: u32,
    /// When set, the header carries one 0xdebe extension word (DTX/piggyback).
    pub extension_word: Option<u32>,
}

impl RtpHeader {
    pub fn byte_size(&self) -> usize {
        if self.extension_word.is_some() {
            WHATSAPP_RTP_HEADER_DTX_SIZE
        } else {
            WHATSAPP_RTP_HEADER_SIZE
        }
    }
}

/// Full on-wire RTP header size (fixed 12 + CSRC + optional extension block), or `None`.
pub fn rtp_header_byte_length(data: &[u8]) -> Option<usize> {
    if data.len() < 12 {
        return None;
    }
    if (data[0] >> 6) & 0x03 != RTP_VERSION {
        return None;
    }
    let cc = (data[0] & 0x0f) as usize;
    let mut header_len = 12 + cc * 4;
    if data.len() < header_len {
        return None;
    }
    let has_extension = (data[0] >> 4) & 1 == 1;
    if has_extension {
        if data.len() < header_len + 4 {
            return None;
        }
        let ext_words = ((data[header_len + 2] as usize) << 8) | data[header_len + 3] as usize;
        header_len += 4 + ext_words * 4;
        if data.len() < header_len {
            return None;
        }
    }
    Some(header_len)
}

pub fn is_rtp_version2(data: &[u8]) -> bool {
    data.len() >= 12 && (data[0] >> 6) & 0x03 == RTP_VERSION
}

/// Parse the fixed RTP header fields (the extension word is not decoded).
pub fn parse_rtp_header(data: &[u8]) -> Option<RtpHeader> {
    rtp_header_byte_length(data)?;
    Some(RtpHeader {
        marker: (data[1] >> 7) & 1 == 1,
        payload_type: data[1] & 0x7f,
        sequence_number: ((data[2] as u16) << 8) | data[3] as u16,
        timestamp: u32::from_be_bytes([data[4], data[5], data[6], data[7]]),
        ssrc: u32::from_be_bytes([data[8], data[9], data[10], data[11]]),
        extension_word: None,
    })
}

pub fn encode_rtp_header(header: &RtpHeader) -> Vec<u8> {
    let size = header.byte_size();
    let mut buf = vec![0u8; size];
    buf[0] = RTP_VERSION << 6;
    if size > 12 {
        buf[0] |= 0x10; // X=1 (WhatsApp 0xdebe extension)
    }
    buf[1] = ((header.marker as u8) << 7) | (header.payload_type & 0x7f);
    buf[2] = (header.sequence_number >> 8) as u8;
    buf[3] = header.sequence_number as u8;
    buf[4..8].copy_from_slice(&header.timestamp.to_be_bytes());
    buf[8..12].copy_from_slice(&header.ssrc.to_be_bytes());
    if size >= 16 {
        buf[12] = (WHATSAPP_RTP_EXTENSION_PROFILE >> 8) as u8;
        buf[13] = WHATSAPP_RTP_EXTENSION_PROFILE as u8;
        let ext_words: u16 = if header.extension_word.is_some() {
            1
        } else {
            0
        };
        buf[14] = (ext_words >> 8) as u8;
        buf[15] = ext_words as u8;
    }
    if size >= 20
        && let Some(w) = header.extension_word
    {
        buf[16..20].copy_from_slice(&w.to_be_bytes());
    }
    buf
}

/// Send-side RTP sequencer: seq starts at 1, timestamp advances by `samples_per_packet`.
pub struct RtpStream {
    pub ssrc: u32,
    seq: u16,
    timestamp: u32,
    samples_per_packet: u32,
    speech_started: bool,
    audio_packet_index: usize,
    warp_piggyback: bool,
}

impl RtpStream {
    pub fn new(ssrc: u32, samples_per_packet: u32, warp_piggyback: bool) -> Self {
        Self {
            ssrc,
            seq: 1,
            timestamp: 0,
            samples_per_packet,
            speech_started: false,
            audio_packet_index: 0,
            warp_piggyback,
        }
    }

    fn resolve_warp_extension(&mut self, dtx: bool) -> Option<u32> {
        if dtx {
            return Some(WHATSAPP_RTP_EXTENSION_DTX_WORD);
        }
        if !self.warp_piggyback {
            return None;
        }
        let idx = self.audio_packet_index;
        self.audio_packet_index += 1;
        audio_piggyback_extension_for(idx, true, crate::voip::warp::WARP_PIGGYBACK_START_PACKET)
    }

    pub fn next_packet(&mut self, payload: &[u8], marker: bool) -> RtpHeader {
        let dtx = is_opus_dtx_payload(payload);
        let priming = is_opus_priming_payload(payload);
        let speech = !dtx && !priming;
        let use_marker = marker || (speech && !self.speech_started);
        if speech {
            self.speech_started = true;
        }
        let header = RtpHeader {
            marker: use_marker,
            payload_type: RTP_PAYLOAD_TYPE_OPUS,
            sequence_number: self.seq,
            timestamp: self.timestamp,
            ssrc: self.ssrc,
            extension_word: self.resolve_warp_extension(dtx),
        };
        self.seq = self.seq.wrapping_add(1);
        self.timestamp = self.timestamp.wrapping_add(self.samples_per_packet);
        header
    }

    /// Pre-speech ladder packet: advances seq/timestamp without a marker or speech latch.
    pub fn next_pre_speech_packet(&mut self) -> RtpHeader {
        let header = RtpHeader {
            marker: false,
            payload_type: RTP_PAYLOAD_TYPE_OPUS,
            sequence_number: self.seq,
            timestamp: self.timestamp,
            ssrc: self.ssrc,
            extension_word: self.resolve_warp_extension(false),
        };
        self.seq = self.seq.wrapping_add(1);
        self.timestamp = self.timestamp.wrapping_add(self.samples_per_packet);
        header
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::voip::testkat::kats;

    #[test]
    fn encode_headers_match_kat() {
        let k = kats();
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        let speech = RtpHeader {
            marker: true,
            payload_type: 120,
            sequence_number: 1,
            timestamp: 0,
            ssrc,
            extension_word: None,
        };
        assert_eq!(
            hex::encode(encode_rtp_header(&speech)),
            k["rtp"]["speechHeader16"].as_str().unwrap()
        );
        let dtx = RtpHeader {
            marker: false,
            payload_type: 120,
            sequence_number: 2,
            timestamp: 320,
            ssrc,
            extension_word: Some(0x3001_0000),
        };
        assert_eq!(
            hex::encode(encode_rtp_header(&dtx)),
            k["rtp"]["dtxHeader20"].as_str().unwrap()
        );
    }

    #[test]
    fn parse_round_trips_fixed_fields() {
        let h = RtpHeader {
            marker: true,
            payload_type: 120,
            sequence_number: 0x1234,
            timestamp: 0xdead_beef,
            ssrc: 0x0102_0304,
            extension_word: None,
        };
        let bytes = encode_rtp_header(&h);
        assert_eq!(rtp_header_byte_length(&bytes), Some(16));
        assert_eq!(parse_rtp_header(&bytes), Some(h));
    }

    #[test]
    fn estimate_wire_bytes_match_kat() {
        let k = kats();
        let payload = hex::decode(k["inputs"]["payload"].as_str().unwrap()).unwrap();
        assert_eq!(
            estimate_srtp_rtp_wire_bytes(&payload) as u64,
            k["rtp"]["estimateSpeech12"].as_u64().unwrap()
        );
        assert_eq!(
            estimate_srtp_rtp_wire_bytes(&[0x10]) as u64,
            k["rtp"]["estimateDtx1"].as_u64().unwrap()
        );
        assert_eq!(
            estimate_srtp_rtp_wire_bytes(&OPUS_PRIMING_FRAME_2) as u64,
            k["rtp"]["estimatePriming2"].as_u64().unwrap()
        );
    }

    #[test]
    fn classifiers() {
        assert!(is_opus_dtx_payload(&[0x10]));
        assert!(is_opus_dtx_payload(&[0x90]));
        assert!(!is_opus_dtx_payload(&[]));
        assert!(is_opus_priming_payload(&OPUS_PRIMING_FRAME_1));
        assert!(is_opus_priming_payload(&OPUS_PRIMING_FRAME_2));
        assert!(!is_opus_priming_payload(&[0x12, 0x36]));
        assert!(is_opus_mlow_speech_payload(&[0x48; 20]));
        assert!(!is_opus_mlow_speech_payload(&[0x48; 4]));
        assert!(is_whatsapp_opus_rtp_payload(120));
        assert!(is_whatsapp_opus_rtp_payload(121));
        assert!(!is_whatsapp_opus_rtp_payload(96));
    }

    #[test]
    fn stream_sequence_and_marker() {
        let mut s = RtpStream::new(0xabcd, 320, false);
        // Priming/DTX before speech: no marker, no speech latch.
        let p0 = s.next_packet(&OPUS_PRIMING_FRAME_2, false);
        assert_eq!((p0.sequence_number, p0.timestamp, p0.marker), (1, 0, false));
        let d1 = s.next_packet(&[0x10], false);
        assert_eq!(
            (d1.sequence_number, d1.timestamp, d1.marker),
            (2, 320, false)
        );
        assert_eq!(d1.extension_word, Some(WHATSAPP_RTP_EXTENSION_DTX_WORD));
        // First speech frame latches the marker.
        let sp = s.next_packet(&[0x48; 40], false);
        assert_eq!(
            (sp.sequence_number, sp.timestamp, sp.marker),
            (3, 640, true)
        );
        // Subsequent speech has no marker.
        let sp2 = s.next_packet(&[0x48; 40], false);
        assert!(!sp2.marker);
    }
}
```

### `rtcp.rs`

```rust
//! RTCP: WhatsApp compact reports (PT 208/209) and a Sender Report (PT 200).
//! The SR's NTP timestamp is taken as a `now_ms` argument so this stays
//! pure/no-clock (caller passes `wacore::time`).

use crate::voip::rtp::RTP_PAYLOAD_TYPE_OPUS;

pub const RTCP_PT_SR: u8 = 200;
pub const RTCP_PT_WA_COMPACT: u8 = 208;
pub const RTCP_PT_WA_COMPACT2: u8 = 209;
pub const RTCP_HEADER_LEN: usize = 8;
pub const SRTCP_TRAILER_LEN: usize = 14;

/// NTP epoch offset (seconds between 1900-01-01 and 1970-01-01).
const NTP_UNIX_OFFSET_SECS: u64 = 2_208_988_800;

pub fn is_rtcp_packet(data: &[u8]) -> bool {
    if data.len() < RTCP_HEADER_LEN + SRTCP_TRAILER_LEN {
        return false;
    }
    if (data[0] >> 6) & 0x03 != 2 {
        return false;
    }
    // WhatsApp RTP uses X=1 (byte0 0x90) and a 7-bit PT in byte1; RTCP uses the full byte1 as PT.
    if data[0] & 0x10 != 0 && data[1] & 0x7f == RTP_PAYLOAD_TYPE_OPUS {
        return false;
    }
    data[1] >= 64
}

pub fn rtcp_payload_type(data: &[u8]) -> Option<u8> {
    is_rtcp_packet(data).then(|| data[1])
}

pub fn parse_rtcp_sender_ssrc(data: &[u8]) -> Option<u32> {
    if data.len() < 8 || (data[0] >> 6) & 0x03 != 2 {
        return None;
    }
    Some(u32::from_be_bytes([data[4], data[5], data[6], data[7]]))
}

#[derive(Clone, Copy, Debug)]
pub struct RtcpSenderStats {
    pub packets_sent: u32,
    pub octets_sent: u32,
    pub rtp_timestamp: u32,
}

/// 12-byte compact RTCP (PT 208, RC=1); 26 bytes on the wire with the SRTCP trailer.
pub fn build_compact_rtcp_208(local_ssrc: u32, remote_ssrc: u32) -> [u8; 12] {
    let mut buf = [0u8; 12];
    buf[0] = 0x81; // V=2, P=0, RC=1
    buf[1] = RTCP_PT_WA_COMPACT;
    buf[2] = 0;
    buf[3] = 2; // (2+1)*4 = 12 bytes
    buf[4..8].copy_from_slice(&local_ssrc.to_be_bytes());
    buf[8..12].copy_from_slice(&remote_ssrc.to_be_bytes());
    buf
}

/// 8-byte compact RTCP (PT 209, RC=1): pre-speech ladder.
pub fn build_compact_rtcp_209(local_ssrc: u32) -> [u8; 8] {
    let mut buf = [0u8; 8];
    buf[0] = 0x81;
    buf[1] = RTCP_PT_WA_COMPACT2;
    buf[2] = 0;
    buf[3] = 1; // (1+1)*4 = 8 bytes
    buf[4..8].copy_from_slice(&local_ssrc.to_be_bytes());
    buf
}

/// 28-byte Sender Report (PT 200, RC=0). `now_ms` is the wall clock in milliseconds.
pub fn build_sender_report(local_ssrc: u32, stats: &RtcpSenderStats, now_ms: u64) -> [u8; 28] {
    let mut buf = [0u8; 28];
    buf[0] = 0x80; // V=2, RC=0
    buf[1] = RTCP_PT_SR;
    buf[2] = 0;
    buf[3] = 6; // (6+1)*4 = 28 bytes
    buf[4..8].copy_from_slice(&local_ssrc.to_be_bytes());
    // NTP timestamp: seconds (upper 32) since 1900, fraction (lower 32). Both fields
    // truncate to u32 (wrapping), matching the reference encoder.
    let ntp_sec = (now_ms / 1000).wrapping_add(NTP_UNIX_OFFSET_SECS) as u32;
    let ntp_frac = ((now_ms % 1000) as f64 / 1000.0 * 4_294_967_296.0) as u32;
    buf[8..12].copy_from_slice(&ntp_sec.to_be_bytes());
    buf[12..16].copy_from_slice(&ntp_frac.to_be_bytes());
    buf[16..20].copy_from_slice(&stats.rtp_timestamp.to_be_bytes());
    buf[20..24].copy_from_slice(&stats.packets_sent.to_be_bytes());
    buf[24..28].copy_from_slice(&stats.octets_sent.to_be_bytes());
    buf
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::voip::testkat::kats;

    #[test]
    fn compact_reports_match_kat() {
        let k = kats();
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        let remote = k["rtcp"]["remoteSsrc"].as_u64().unwrap() as u32;
        assert_eq!(
            hex::encode(build_compact_rtcp_208(ssrc, remote)),
            k["rtcp"]["compact208"].as_str().unwrap()
        );
        assert_eq!(
            hex::encode(build_compact_rtcp_209(ssrc)),
            k["rtcp"]["compact209"].as_str().unwrap()
        );
    }

    #[test]
    fn sender_report_matches_kat() {
        let k = kats();
        let ssrc = k["inputs"]["ssrc"].as_u64().unwrap() as u32;
        let now_ms = k["rtcp"]["nowMs"].as_u64().unwrap();
        let stats = RtcpSenderStats {
            packets_sent: k["rtcp"]["stats"]["packetsSent"].as_u64().unwrap() as u32,
            octets_sent: k["rtcp"]["stats"]["octetsSent"].as_u64().unwrap() as u32,
            rtp_timestamp: k["rtcp"]["stats"]["rtpTimestamp"].as_u64().unwrap() as u32,
        };
        assert_eq!(
            hex::encode(build_sender_report(ssrc, &stats, now_ms)),
            k["rtcp"]["senderReport"].as_str().unwrap()
        );
    }

    #[test]
    fn rtcp_classification() {
        let k = kats();
        let sr = hex::decode(k["rtcp"]["senderReport"].as_str().unwrap()).unwrap();
        // SR alone is 28 bytes (>= 8+14), V=2, PT=200 >= 64.
        assert!(is_rtcp_packet(&sr));
        assert_eq!(rtcp_payload_type(&sr), Some(200));
        // An RTP speech header (X=1, PT=120) must NOT be classified as RTCP.
        let rtp = hex::decode(k["rtp"]["speechHeader16"].as_str().unwrap()).unwrap();
        let mut padded = rtp.clone();
        padded.resize(40, 0);
        assert!(!is_rtcp_packet(&padded));
    }
}
```

## Go envelope (signatures only)

```go
package rtp

const (
	RtpPayloadTypeOpus            uint8  = 120
	WhatsappRtpExtensionProfile   uint16 = 0xdebe
	WhatsappRtpHeaderSize         int    = 16
	WhatsappRtpHeaderDtxSize      int    = 20
	WhatsappRtpExtensionDtxWord   uint32 = 0x30010000
)

var (
	OpusPrimingFrame1     = [18]byte{ /* ... */ }
	OpusPrimingFrame1Wasm = [24]byte{ /* ... */ }
	OpusPrimingFrame2     = [5]byte{0x90, 0xb8, 0x14, 0x14, 0xc4}
)

func IsWhatsappOpusRtpPayload(payloadType uint8) bool

func IsOpusDtxPayload(payload []byte) bool

func IsOpusMlowSpeechPayload(payload []byte) bool

func IsOpusPrimingPayload(payload []byte) bool

func EstimateSrtpRtpWireBytes(opusPayload []byte) int

type RtpHeader struct {
	Marker         bool
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	Ssrc           uint32
	ExtensionWord  *uint32 // nil = no 0xdebe extension word
}

func (h *RtpHeader) ByteSize() int

func RtpHeaderByteLength(data []byte) (int, bool)

func IsRtpVersion2(data []byte) bool

func ParseRtpHeader(data []byte) (RtpHeader, bool)

func EncodeRtpHeader(header *RtpHeader) []byte

type RtpStream struct {
	Ssrc uint32
	// unexported: seq, timestamp, samplesPerPacket, speechStarted, audioPacketIndex, warpPiggyback
}

func NewRtpStream(ssrc, samplesPerPacket uint32, warpPiggyback bool) *RtpStream

func (s *RtpStream) NextPacket(payload []byte, marker bool) RtpHeader

func (s *RtpStream) NextPreSpeechPacket() RtpHeader

// --- RTCP ---

const (
	RtcpPtSr        uint8 = 200
	RtcpPtWaCompact uint8 = 208
	RtcpPtWaCompact2 uint8 = 209
	RtcpHeaderLen   int   = 8
	SrtcpTrailerLen int   = 14
)

func IsRtcpPacket(data []byte) bool

func RtcpPayloadType(data []byte) (uint8, bool)

func ParseRtcpSenderSsrc(data []byte) (uint32, bool)

type RtcpSenderStats struct {
	PacketsSent  uint32
	OctetsSent   uint32
	RtpTimestamp uint32
}

func BuildCompactRtcp208(localSsrc, remoteSsrc uint32) [12]byte

func BuildCompactRtcp209(localSsrc uint32) [8]byte

func BuildSenderReport(localSsrc uint32, stats *RtcpSenderStats, nowMs uint64) [28]byte
```

## Implementation suggestions (guidance, not authoritative)

- All on-wire integers are big-endian (`to_be_bytes` / `from_be_bytes`); use
  `binary.BigEndian`. The `usize` lengths map to Go `int`; the wire-byte estimator
  returns an `int`.
- `Option<u32> extension_word` → `*uint32`: nil selects the 16-byte header, set
  selects the 20-byte header with one extension word. The `&[u8]`/`Vec<u8>` pairs
  map to `[]byte`; fixed-byte returns (`[12]byte`, `[8]byte`, `[28]byte`) stay arrays.
- `seq` and `timestamp` advance with `wrapping_add` on `u16`/`u32`; Go's unsigned
  arithmetic already wraps, so plain `+=` matches. Seq starts at 1, timestamp at 0.
- `is_opus_dtx_payload` and `is_opus_mlow_speech_payload` are length-and-bit-pattern
  matches on the first byte; preserve the exact masks (`0xf8`, `0xf0`) and the
  `n <= 15` / `n <= 6` / `< 18` length gates. The `RtpStream.warp_piggyback` branch
  calls into a separate warp module — `TODO(human)`: wire `audioPiggybackExtensionFor`
  and `WARP_PIGGYBACK_START_PACKET` from that package, not implemented here.
- `is_opus_priming_payload` compares full byte slices against the constant frames;
  `bytes.Equal` is the idiomatic Go equivalent of the Rust slice `==`.
- The Sender Report NTP fraction uses `f64` math (`(now_ms % 1000)/1000.0 * 2^32`)
  truncated to `u32`; use Go `float64` then convert to `uint32` to match the `>>> 0`
  behavior. `TODO(human)`: confirm float rounding direction matches on your platform
  (the source truncates toward zero on the cast).
- These functions return optional results via `Option`/`None` in Rust; the Go
  envelope uses a trailing `bool` "ok". `ParseRtpHeader` validates via
  `RtpHeaderByteLength` first, then fills only the fixed fields (extension word left nil).
```
