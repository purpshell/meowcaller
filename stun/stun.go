package stun

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"hash/crc32"
	"strconv"
	"strings"
)

// STUN/WARP relay framing: an RFC 5389 TLV encoder with WhatsApp's
// MESSAGE-INTEGRITY (HMAC-SHA1) and FINGERPRINT (CRC-32), the allocate-request
// builders, the consent ping, and the response parsers.

const (
	stunMagic          = 0x2112a442
	stunFingerprintXor = 0x5354554e
	stunXorPort        = 0x2112

	attrMessageIntegrity      = 0x0008
	attrFingerprint           = 0x8028
	attrErrorCode             = 0x0009
	attrRelayToken            = 0x4000
	attrStreamDescriptors     = 0x4024
	attrWasmRelayEndpoint     = 0x0016
	attrSenderSubscriptionsV2 = 0x4025
)

// STUN message types.
const (
	MsgBindingRequest  uint16 = 0x0001
	MsgAllocateRequest uint16 = 0x0003
	MsgBindingSuccess  uint16 = 0x0101
	MsgAllocateSuccess uint16 = 0x0103
	MsgAllocateError   uint16 = 0x0113
	MsgWhatsappPing    uint16 = 0x0801
	MsgWhatsappPong    uint16 = 0x0802
)

// stunXorAddr is the magic-cookie prefix XORed into XOR-MAPPED addresses.
var stunXorAddr = [4]byte{0x21, 0x12, 0xa4, 0x42}

// wasmStreamDescriptorsTemplate is the WASM/Web StreamDescriptors blob (attr 0x4024).
var wasmStreamDescriptorsTemplate = []byte{
	0x0a, 0x06, 0x18, 0xca, 0xbc, 0x85, 0xae, 0x04, 0x0a, 0x08, 0x10, 0x01, 0x18, 0xa5, 0xac, 0xaf,
	0xae, 0x0a, 0x0a, 0x08, 0x10, 0x02, 0x18, 0xd6, 0xa4, 0xe6, 0xf9, 0x0f, 0x0a, 0x08, 0x08, 0x01,
	0x18, 0xf7, 0xdd, 0x9e, 0xb6, 0x0a, 0x0a, 0x0a, 0x08, 0x01, 0x10, 0x01, 0x18, 0xab, 0xcc, 0xb1,
	0xf3, 0x0d, 0x0a, 0x0a, 0x08, 0x01, 0x10, 0x02, 0x18, 0xda, 0xda, 0xef, 0x8a, 0x05, 0x0a, 0x08,
	0x08, 0x02, 0x18, 0xc5, 0xe9, 0xec, 0x8e, 0x0b, 0x0a, 0x0a, 0x08, 0x02, 0x10, 0x01, 0x18, 0xfd,
	0xc2, 0xb1, 0xb6, 0x0f, 0x0a, 0x0a, 0x08, 0x02, 0x10, 0x02, 0x18, 0xb0, 0x97, 0xf7, 0xb2, 0x09,
}

func pad4(n int) int {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L44-L46
	return (4 - (n % 4)) % 4
}

// stunAttr encodes one STUN attribute (type, length, value, 4-byte alignment pad).
func stunAttr(attrType uint16, value []byte) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L49-L57
	pad := pad4(len(value))
	buf := make([]byte, 0, 4+len(value)+pad)
	buf = binary.BigEndian.AppendUint16(buf, attrType)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(value)))
	buf = append(buf, value...)
	return append(buf, make([]byte, pad)...)
}

// stunFingerprint is the STUN FINGERPRINT CRC-32 (reflected IEEE poly 0xedb88320),
// which is exactly stdlib hash/crc32.ChecksumIEEE.
func stunFingerprint(buf []byte) uint32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L60-L69
	return crc32.ChecksumIEEE(buf)
}

func stunPseudoHeader(msgType, msgLen uint16, transactionID [12]byte) [20]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L71-L78
	var h [20]byte
	binary.BigEndian.PutUint16(h[0:2], msgType)
	binary.BigEndian.PutUint16(h[2:4], msgLen)
	binary.BigEndian.PutUint32(h[4:8], stunMagic)
	copy(h[8:20], transactionID[:])
	return h
}

// EncodeStunRequest encodes a STUN request: header + attrs, then optional
// MESSAGE-INTEGRITY (nil integrityKey skips it) and optional FINGERPRINT.
func EncodeStunRequest(msgType uint16, transactionID [12]byte, attrs []byte, integrityKey []byte, includeFingerprint bool) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L82-L118
	body := append([]byte(nil), attrs...)

	if integrityKey != nil {
		msgLen := uint16(len(body) + 24) // attrs + MI attr (4 + 20)
		header := stunPseudoHeader(msgType, msgLen, transactionID)
		mac := hmac.New(sha1.New, integrityKey)
		mac.Write(header[:])
		mac.Write(body)
		body = append(body, stunAttr(attrMessageIntegrity, mac.Sum(nil))...)
	}

	if includeFingerprint {
		msgLen := uint16(len(body) + 8) // attrs + FINGERPRINT attr (4 + 4)
		header := stunPseudoHeader(msgType, msgLen, transactionID)
		crcInput := make([]byte, 0, 20+len(body))
		crcInput = append(crcInput, header[:]...)
		crcInput = append(crcInput, body...)
		fp := stunFingerprint(crcInput) ^ stunFingerprintXor
		var fpb [4]byte
		binary.BigEndian.PutUint32(fpb[:], fp)
		body = append(body, stunAttr(attrFingerprint, fpb[:])...)
	}

	out := make([]byte, 0, 20+len(body))
	out = binary.BigEndian.AppendUint16(out, msgType)
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	out = binary.BigEndian.AppendUint32(out, stunMagic)
	out = append(out, transactionID[:]...)
	return append(out, body...)
}

// CreateNativeSenderSubscription is a native WA sender sub: 1-byte count + BE SSRC.
func CreateNativeSenderSubscription(ssrc uint32) [5]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L121-L126
	var buf [5]byte
	buf[0] = 1
	binary.BigEndian.PutUint32(buf[1:5], ssrc)
	return buf
}

// EncodeXorRelayEndpoint XOR-encodes an IPv4:port into 6 bytes; ok=false if ipv4
// is not exactly four dotted octets.
func EncodeXorRelayEndpoint(ipv4 string, port uint16) ([6]byte, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L129-L144
	var octets []byte
	for part := range strings.SplitSeq(ipv4, ".") {
		n, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			continue
		}
		octets = append(octets, byte(n))
	}
	if len(octets) != 4 {
		return [6]byte{}, false
	}
	var buf [6]byte
	binary.BigEndian.PutUint16(buf[0:2], port^stunXorPort)
	for i := range 4 {
		buf[2+i] = octets[i] ^ stunXorAddr[i]
	}
	return buf, true
}

// createWasmRelayEndpointAttr is the WASM attr 0x0016 value: 00 01 + 6-byte endpoint.
func createWasmRelayEndpointAttr(endpointXor [6]byte) [8]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L147-L152
	var buf [8]byte
	binary.BigEndian.PutUint16(buf[0:2], 1)
	copy(buf[2:8], endpointXor[:])
	return buf
}

// BuildWasmStunAllocateRequest builds the WASM/Web DataChannel Allocate: 0x4000
// token + 0x4024 stream desc + 0x0016 endpoint + MI, no FP.
func BuildWasmStunAllocateRequest(transactionID [12]byte, relayToken []byte, endpointXor [6]byte, integrityKey []byte) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L155-L177
	attrs := stunAttr(attrRelayToken, relayToken)
	attrs = append(attrs, stunAttr(attrStreamDescriptors, wasmStreamDescriptorsTemplate)...)
	wep := createWasmRelayEndpointAttr(endpointXor)
	attrs = append(attrs, stunAttr(attrWasmRelayEndpoint, wep[:])...)
	return EncodeStunRequest(MsgAllocateRequest, transactionID, attrs, integrityKey, false)
}

// BuildWhatsappPing builds the WhatsApp consent ping (type 0x0801, empty body).
func BuildWhatsappPing(transactionID [12]byte) [20]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L180-L186
	var out [20]byte
	binary.BigEndian.PutUint16(out[0:2], MsgWhatsappPing)
	binary.BigEndian.PutUint32(out[4:8], stunMagic)
	copy(out[8:20], transactionID[:])
	return out
}

// IsStunPacket reports whether data looks like a STUN packet (top two bits zero).
func IsStunPacket(data []byte) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L188-L190
	return len(data) >= 2 && (data[0]&0xc0) == 0x00
}

// StunMessageType returns the STUN message type; ok=false if data is too short.
func StunMessageType(data []byte) (uint16, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L192-L194
	if len(data) < 2 {
		return 0, false
	}
	return (uint16(data[0]&0x3f) << 8) | uint16(data[1]), true
}

// StunTransactionID returns the 12-byte transaction id; ok=false if data < 20 bytes.
func StunTransactionID(data []byte) ([]byte, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L196-L198
	if len(data) < 20 {
		return nil, false
	}
	return data[8:20], true
}

// IsAllocateOrBindingSuccess reports an Allocate/Binding success response.
func IsAllocateOrBindingSuccess(data []byte) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L200-L208
	if !IsStunPacket(data) || len(data) < 20 {
		return false
	}
	mt, ok := StunMessageType(data)
	return ok && (mt == MsgAllocateSuccess || mt == MsgBindingSuccess)
}

// IsAllocateError reports an Allocate-error response.
func IsAllocateError(data []byte) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L210-L212
	if !IsStunPacket(data) {
		return false
	}
	mt, ok := StunMessageType(data)
	return ok && mt == MsgAllocateError
}

// IsWhatsappPong reports a WhatsApp pong; a nil/empty transactionID matches any.
func IsWhatsappPong(data []byte, transactionID []byte) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L214-L222
	if !IsStunPacket(data) {
		return false
	}
	mt, ok := StunMessageType(data)
	if !ok || mt != MsgWhatsappPong {
		return false
	}
	if len(transactionID) == 0 {
		return true
	}
	tx, ok := StunTransactionID(data)
	return ok && bytes.Equal(tx, transactionID)
}

// StunAttribute is one parsed STUN TLV.
type StunAttribute struct {
	AttrType uint16
	Value    []byte
}

// ParseStunAttributes parses the STUN attributes after the 20-byte header.
func ParseStunAttributes(data []byte) []StunAttribute {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L231-L251
	if !IsStunPacket(data) || len(data) < 20 {
		return nil
	}
	var attrs []StunAttribute
	off := 20
	for off+4 <= len(data) {
		attrType := (uint16(data[off]) << 8) | uint16(data[off+1])
		length := (int(data[off+2]) << 8) | int(data[off+3])
		off += 4
		if off+length > len(data) {
			break
		}
		attrs = append(attrs, StunAttribute{
			AttrType: attrType,
			Value:    append([]byte(nil), data[off:off+length]...),
		})
		off += length + pad4(length)
	}
	return attrs
}

// ParseStunErrorCode parses the numeric error code (class*100+number); ok=false if absent.
func ParseStunErrorCode(data []byte) (uint16, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L254-L276
	if len(data) < 20 {
		return 0, false
	}
	t, ok := StunMessageType(data)
	if !ok || (t != MsgAllocateError && t != 0x0111) {
		return 0, false
	}
	bodyLen := (int(data[2]) << 8) | int(data[3])
	end := min(20+bodyLen, len(data))
	off := 20
	for off+4 <= end {
		attrType := (uint16(data[off]) << 8) | uint16(data[off+1])
		length := (int(data[off+2]) << 8) | int(data[off+3])
		if attrType == attrErrorCode && length >= 4 && off+8 <= len(data) {
			class := uint16(data[off+6])
			number := uint16(data[off+7])
			return class*100 + number, true
		}
		off += 4 + length + pad4(length)
	}
	return 0, false
}

// pbTag writes a protobuf field tag.
func pbTag(out []byte, field, wire uint32) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L284-L286
	return binary.AppendUvarint(out, uint64((field<<3)|wire))
}

// pbZigzag zigzag-encodes a signed integer.
func pbZigzag(n int64) uint64 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L288-L290
	return uint64((n << 1) ^ (n >> 63))
}

// pbLenDelim writes a length-delimited protobuf field.
func pbLenDelim(out []byte, field uint32, b []byte) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L292-L296
	out = pbTag(out, field, 2)
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

// CreateVoipSenderSubscriptions builds voip.SenderSubscriptions (WASM, attr 0x4000).
func CreateVoipSenderSubscriptions(ssrc uint32) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L299-L310
	var sender []byte
	sender = pbTag(sender, 3, 0) // ssrc
	sender = binary.AppendUvarint(sender, uint64(ssrc))
	sender = pbTag(sender, 5, 0) // stream_layer = AUDIO(0)
	sender = binary.AppendUvarint(sender, 0)
	sender = pbTag(sender, 6, 0) // payload_type = MEDIA(0)
	sender = binary.AppendUvarint(sender, 0)
	return pbLenDelim(nil, 1, sender)
}

// CreateApkSenderSubscriptions builds wa.voip.SenderSubscriptions (APK, attr 0x4025).
func CreateApkSenderSubscriptions(ssrc uint32, pid *uint32) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L313-L329
	var ssrcLayers []byte
	ssrcLayers = pbTag(ssrcLayers, 1, 0) // ssrcs[0] (sint64, zigzag)
	ssrcLayers = binary.AppendUvarint(ssrcLayers, pbZigzag(int64(ssrc)))
	if pid != nil {
		var p []byte
		p = pbTag(p, 1, 0) // pid (sint64)
		p = binary.AppendUvarint(p, pbZigzag(int64(*pid)))
		p = pbLenDelim(p, 2, []byte("audio")) // layerId
		ssrcLayers = pbLenDelim(ssrcLayers, 2, p)
	}
	ext := pbLenDelim(nil, 1, ssrcLayers) // ssrcLayers
	return pbLenDelim(nil, 1, ext)        // subscriptions[0]
}

// CreateApkStreamDescriptors builds wa.voip.StreamDescriptors (APK, attr 0x4024).
func CreateApkStreamDescriptors(ssrc uint32) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L332-L343
	var sd []byte
	sd = pbLenDelim(sd, 1, []byte("audio")) // stream_layer
	sd = pbLenDelim(sd, 2, []byte("OPUS"))  // payload_type
	sd = pbTag(sd, 3, 0)                    // ssrc (sint64)
	sd = binary.AppendUvarint(sd, pbZigzag(int64(ssrc)))
	sd = pbTag(sd, 4, 0) // is_uplink_prefetch_enabled = false
	sd = binary.AppendUvarint(sd, 0)
	return pbLenDelim(nil, 1, sd)
}

// BuildAndroidStunAllocateRequest builds the APK Allocate: 0x4000 token + 0x4025
// sender subs + 0x4024 stream desc + MI.
func BuildAndroidStunAllocateRequest(transactionID [12]byte, relayToken []byte, ssrc uint32, pid *uint32, integrityKey []byte, includeFingerprint bool) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stun.rs#L346-L370
	attrs := stunAttr(attrRelayToken, relayToken)
	attrs = append(attrs, stunAttr(attrSenderSubscriptionsV2, CreateApkSenderSubscriptions(ssrc, pid))...)
	attrs = append(attrs, stunAttr(attrStreamDescriptors, CreateApkStreamDescriptors(ssrc))...)
	return EncodeStunRequest(MsgAllocateRequest, transactionID, attrs, integrityKey, includeFingerprint)
}
