package rtp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type rtpKat struct {
	Inputs struct {
		SSRC    uint32 `json:"ssrc"`
		Payload string `json:"payload"`
	} `json:"inputs"`
	Rtp struct {
		SpeechHeader16   string `json:"speechHeader16"`
		DtxHeader20      string `json:"dtxHeader20"`
		EstimateSpeech12 uint64 `json:"estimateSpeech12"`
		EstimateDtx1     uint64 `json:"estimateDtx1"`
		EstimatePriming2 uint64 `json:"estimatePriming2"`
	} `json:"rtp"`
	Rtcp struct {
		NowMs      uint64 `json:"nowMs"`
		RemoteSsrc uint32 `json:"remoteSsrc"`
		Stats      struct {
			PacketsSent  uint32 `json:"packetsSent"`
			OctetsSent   uint32 `json:"octetsSent"`
			RtpTimestamp uint32 `json:"rtpTimestamp"`
		} `json:"stats"`
		Compact208   string `json:"compact208"`
		Compact209   string `json:"compact209"`
		SenderReport string `json:"senderReport"`
	} `json:"rtcp"`
}

func loadKat(t *testing.T) rtpKat {
	t.Helper()
	raw, err := os.ReadFile("testdata/kats.json")
	if err != nil {
		t.Fatalf("read kats.json: %v", err)
	}
	var k rtpKat
	if err := json.Unmarshal(raw, &k); err != nil {
		t.Fatalf("parse kats.json: %v", err)
	}
	return k
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func u32ptr(v uint32) *uint32 { return &v }

// TestEncodeHeadersMatchKAT checks the 16-byte speech and 20-byte DTX header encodings.
func TestEncodeHeadersMatchKAT(t *testing.T) {
	k := loadKat(t)
	speech := RtpHeader{Marker: true, PayloadType: 120, SequenceNumber: 1, Timestamp: 0, Ssrc: k.Inputs.SSRC}
	if got := hex.EncodeToString(EncodeRtpHeader(&speech)); got != k.Rtp.SpeechHeader16 {
		t.Errorf("speechHeader16 = %s, want %s", got, k.Rtp.SpeechHeader16)
	}
	dtx := RtpHeader{Marker: false, PayloadType: 120, SequenceNumber: 2, Timestamp: 320, Ssrc: k.Inputs.SSRC, ExtensionWord: u32ptr(0x30010000)}
	if got := hex.EncodeToString(EncodeRtpHeader(&dtx)); got != k.Rtp.DtxHeader20 {
		t.Errorf("dtxHeader20 = %s, want %s", got, k.Rtp.DtxHeader20)
	}
}

// TestParseRoundTripsFixedFields encodes then parses a header and compares the fixed fields.
func TestParseRoundTripsFixedFields(t *testing.T) {
	h := RtpHeader{Marker: true, PayloadType: 120, SequenceNumber: 0x1234, Timestamp: 0xdeadbeef, Ssrc: 0x01020304}
	b := EncodeRtpHeader(&h)
	if n, ok := RtpHeaderByteLength(b); !ok || n != 16 {
		t.Errorf("RtpHeaderByteLength = (%d, %v), want (16, true)", n, ok)
	}
	got, ok := ParseRtpHeader(b)
	if !ok || !reflect.DeepEqual(got, h) {
		t.Errorf("ParseRtpHeader = (%+v, %v), want (%+v, true)", got, ok, h)
	}
}

// TestEstimateWireBytesMatchKAT checks the on-wire size estimator for speech/DTX/priming.
func TestEstimateWireBytesMatchKAT(t *testing.T) {
	k := loadKat(t)
	payload := mustHex(t, k.Inputs.Payload)
	if got := EstimateSrtpRtpWireBytes(payload); uint64(got) != k.Rtp.EstimateSpeech12 {
		t.Errorf("estimateSpeech12 = %d, want %d", got, k.Rtp.EstimateSpeech12)
	}
	if got := EstimateSrtpRtpWireBytes([]byte{0x10}); uint64(got) != k.Rtp.EstimateDtx1 {
		t.Errorf("estimateDtx1 = %d, want %d", got, k.Rtp.EstimateDtx1)
	}
	if got := EstimateSrtpRtpWireBytes(OpusPrimingFrame2[:]); uint64(got) != k.Rtp.EstimatePriming2 {
		t.Errorf("estimatePriming2 = %d, want %d", got, k.Rtp.EstimatePriming2)
	}
}

// TestClassifiers exercises the DTX/priming/mlow/payload-type classifiers.
func TestClassifiers(t *testing.T) {
	if !IsOpusDtxPayload([]byte{0x10}) || !IsOpusDtxPayload([]byte{0x90}) || IsOpusDtxPayload(nil) {
		t.Error("dtx classification wrong")
	}
	if !IsOpusPrimingPayload(OpusPrimingFrame1[:]) || !IsOpusPrimingPayload(OpusPrimingFrame2[:]) || IsOpusPrimingPayload([]byte{0x12, 0x36}) {
		t.Error("priming classification wrong")
	}
	if !IsOpusMlowSpeechPayload(bytes.Repeat([]byte{0x48}, 20)) || IsOpusMlowSpeechPayload(bytes.Repeat([]byte{0x48}, 4)) {
		t.Error("mlow speech classification wrong")
	}
	if !IsWhatsappOpusRtpPayload(120) || !IsWhatsappOpusRtpPayload(121) || IsWhatsappOpusRtpPayload(96) {
		t.Error("payload-type classification wrong")
	}
}

// TestStreamSequenceAndMarker exercises the sequencer's seq/timestamp/marker latch.
func TestStreamSequenceAndMarker(t *testing.T) {
	s := NewRtpStream(0xabcd, 320, false)
	p0 := s.NextPacket(OpusPrimingFrame2[:], false)
	if p0.SequenceNumber != 1 || p0.Timestamp != 0 || p0.Marker {
		t.Errorf("p0 = (%d, %d, %v), want (1, 0, false)", p0.SequenceNumber, p0.Timestamp, p0.Marker)
	}
	d1 := s.NextPacket([]byte{0x10}, false)
	if d1.SequenceNumber != 2 || d1.Timestamp != 320 || d1.Marker {
		t.Errorf("d1 = (%d, %d, %v), want (2, 320, false)", d1.SequenceNumber, d1.Timestamp, d1.Marker)
	}
	if d1.ExtensionWord == nil || *d1.ExtensionWord != WhatsappRtpExtensionDtxWord {
		t.Errorf("d1 extension word = %v, want %#x", d1.ExtensionWord, WhatsappRtpExtensionDtxWord)
	}
	sp := s.NextPacket(bytes.Repeat([]byte{0x48}, 40), false)
	if sp.SequenceNumber != 3 || sp.Timestamp != 640 || !sp.Marker {
		t.Errorf("sp = (%d, %d, %v), want (3, 640, true)", sp.SequenceNumber, sp.Timestamp, sp.Marker)
	}
	sp2 := s.NextPacket(bytes.Repeat([]byte{0x48}, 40), false)
	if sp2.Marker {
		t.Error("second speech frame must not latch the marker")
	}
}

// TestCompactReportsMatchKAT checks the 208/209 compact RTCP reports.
func TestCompactReportsMatchKAT(t *testing.T) {
	k := loadKat(t)
	r208 := BuildCompactRtcp208(k.Inputs.SSRC, k.Rtcp.RemoteSsrc)
	if got := hex.EncodeToString(r208[:]); got != k.Rtcp.Compact208 {
		t.Errorf("compact208 = %s, want %s", got, k.Rtcp.Compact208)
	}
	r209 := BuildCompactRtcp209(k.Inputs.SSRC)
	if got := hex.EncodeToString(r209[:]); got != k.Rtcp.Compact209 {
		t.Errorf("compact209 = %s, want %s", got, k.Rtcp.Compact209)
	}
}

// TestSenderReportMatchesKAT checks the 28-byte Sender Report.
func TestSenderReportMatchesKAT(t *testing.T) {
	k := loadKat(t)
	stats := RtcpSenderStats{PacketsSent: k.Rtcp.Stats.PacketsSent, OctetsSent: k.Rtcp.Stats.OctetsSent, RtpTimestamp: k.Rtcp.Stats.RtpTimestamp}
	sr := BuildSenderReport(k.Inputs.SSRC, &stats, k.Rtcp.NowMs)
	if got := hex.EncodeToString(sr[:]); got != k.Rtcp.SenderReport {
		t.Errorf("senderReport = %s, want %s", got, k.Rtcp.SenderReport)
	}
}

// TestRtcpClassification checks the RTCP/RTP discriminator.
func TestRtcpClassification(t *testing.T) {
	k := loadKat(t)
	sr := mustHex(t, k.Rtcp.SenderReport)
	if !IsRtcpPacket(sr) {
		t.Error("sender report not classified as RTCP")
	}
	if pt, ok := RtcpPayloadType(sr); !ok || pt != 200 {
		t.Errorf("rtcp PT = (%d, %v), want (200, true)", pt, ok)
	}
	rtpHdr := mustHex(t, k.Rtp.SpeechHeader16)
	padded := make([]byte, 40)
	copy(padded, rtpHdr)
	if IsRtcpPacket(padded) {
		t.Error("RTP speech header must not classify as RTCP")
	}
}
