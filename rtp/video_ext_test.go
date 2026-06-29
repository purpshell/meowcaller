package rtp

import (
	"encoding/binary"
	"testing"
)

// TestBuildVideoRtpExtensionLayout checks the one-byte (RFC 5285) element layout:
// abs-send-time (id3,3B) + orientation (id5,1B) + transport-cc (id6,2B), padded to 12B.
func TestBuildVideoRtpExtensionLayout(t *testing.T) {
	ext := BuildVideoRtpExtension(0x1234, 2)
	if len(ext) != 12 {
		t.Fatalf("ext len = %d, want 12 (3 words)", len(ext))
	}
	if ext[0] != 0x32 {
		t.Errorf("abs-send-time tag = %#x, want 0x32 (id=3,len=3)", ext[0])
	}
	if ext[4] != 0x50 {
		t.Errorf("orientation tag = %#x, want 0x50 (id=5,len=1)", ext[4])
	}
	if ext[5] != 2 {
		t.Errorf("cvo = %d, want 2", ext[5])
	}
	if ext[6] != 0x61 {
		t.Errorf("transport-cc tag = %#x, want 0x61 (id=6,len=2)", ext[6])
	}
	if seq := binary.BigEndian.Uint16(ext[7:9]); seq != 0x1234 {
		t.Errorf("tccSeq = %#x, want 0x1234", seq)
	}
}

// TestEncodeVideoExtensionHeader checks that a header carrying ExtensionData encodes the
// X bit, the 0xBEDE profile, the 32-bit word count, and round-trips its on-wire length.
func TestEncodeVideoExtensionHeader(t *testing.T) {
	h := RtpHeader{
		PayloadType:      RtpPayloadTypeH264,
		SequenceNumber:   7,
		Timestamp:        90000,
		Ssrc:             0x0a0b0c0d,
		Marker:           true,
		ExtensionProfile: VideoRtpExtensionProfile,
		ExtensionData:    BuildVideoRtpExtension(1, 0),
	}
	if want := 12 + 4 + 12; h.ByteSize() != want {
		t.Fatalf("ByteSize = %d, want %d", h.ByteSize(), want)
	}
	b := EncodeRtpHeader(&h)
	if b[0]&0x10 == 0 {
		t.Error("X (extension) bit not set")
	}
	if b[1]&0x7f != RtpPayloadTypeH264 || b[1]&0x80 == 0 {
		t.Errorf("byte1 = %#x, want PT=97 with marker", b[1])
	}
	if prof := binary.BigEndian.Uint16(b[12:14]); prof != VideoRtpExtensionProfile {
		t.Errorf("ext profile = %#x, want %#x", prof, VideoRtpExtensionProfile)
	}
	if words := binary.BigEndian.Uint16(b[14:16]); words != 3 {
		t.Errorf("ext word count = %d, want 3", words)
	}
	if n, ok := RtpHeaderByteLength(b); !ok || n != len(b) {
		t.Errorf("RtpHeaderByteLength = (%d, %v), want (%d, true)", n, ok, len(b))
	}
}
