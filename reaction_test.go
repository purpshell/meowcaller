package meowcaller

import (
	"bytes"
	"errors"
	"testing"

	"github.com/purpshell/meowcaller/rtp"
)

func TestAppDataReactionMatchesCapturedWirePayload(t *testing.T) {
	want := []byte{0x0a, 0x0a, 0x0a, 0x08, 0x08, 0x01, 0x12, 0x04, 0xf0, 0x9f, 0x91, 0x8d}
	if got := encodeAppDataReaction(1, "👍"); !bytes.Equal(got, want) {
		t.Fatalf("encoded reaction = %x, want %x", got, want)
	}
}

func TestAppDataReactionDecodesCapturedWirePayloads(t *testing.T) {
	tests := []struct {
		payload []byte
		id      uint64
		emoji   string
	}{
		{[]byte{0x0a, 0x0a, 0x0a, 0x08, 0x08, 0x01, 0x12, 0x04, 0xf0, 0x9f, 0x91, 0x8d}, 1, "👍"},
		{[]byte{0x0a, 0x0c, 0x0a, 0x0a, 0x08, 0x02, 0x12, 0x06, 0xe2, 0x9d, 0xa4, 0xef, 0xb8, 0x8f}, 2, "❤️"},
		{[]byte{0x0a, 0x0a, 0x0a, 0x08, 0x08, 0x03, 0x12, 0x04, 0xf0, 0x9f, 0x98, 0x82}, 3, "😂"},
		{[]byte{0x0a, 0x0a, 0x0a, 0x08, 0x08, 0x04, 0x12, 0x04, 0xf0, 0x9f, 0x98, 0xae}, 4, "😮"},
		{[]byte{0x0a, 0x0a, 0x0a, 0x08, 0x08, 0x05, 0x12, 0x04, 0xf0, 0x9f, 0x98, 0xa2}, 5, "😢"},
		{[]byte{0x0a, 0x0a, 0x0a, 0x08, 0x08, 0x06, 0x12, 0x04, 0xf0, 0x9f, 0x99, 0x8f}, 6, "🙏"},
	}
	for _, tt := range tests {
		reactions, err := decodeAppDataReactions(tt.payload)
		if err != nil {
			t.Fatalf("decode %x: %v", tt.payload, err)
		}
		if len(reactions) != 1 || reactions[0].transactionID != tt.id || reactions[0].emoji != tt.emoji {
			t.Fatalf("decode %x = %+v, want id=%d emoji=%q", tt.payload, reactions, tt.id, tt.emoji)
		}
	}
}

func TestAppDataReceiverDeduplicatesRetransmissions(t *testing.T) {
	var receiver appDataReceiver
	payload := encodeAppDataReaction(9, "👍")
	first, ok, err := receiver.receive(payload)
	if err != nil || !ok || first.emoji != "👍" {
		t.Fatalf("first receive = (%+v, %v, %v)", first, ok, err)
	}
	if _, ok, err = receiver.receive(payload); err != nil || ok {
		t.Fatalf("duplicate receive = (ok=%v, err=%v), want ignored", ok, err)
	}
	if _, ok, err = receiver.receive(encodeAppDataReaction(8, "👏")); err != nil || ok {
		t.Fatalf("older receive = (ok=%v, err=%v), want ignored", ok, err)
	}
}

func TestAppDataSenderUsesCapturedRTPShape(t *testing.T) {
	callKey := iota32()
	const ssrc = 0x7f4d310b
	pipe, err := NewMediaPipeline(callKey, "111111111111111:0@lid", "222222222222222:0@lid", ssrc, FrameSamples)
	if err != nil {
		t.Fatalf("sender pipe: %v", err)
	}
	var packets [][]byte
	sender := newAppDataSender(pipe, ssrc, func(packet []byte) (int, error) {
		packets = append(packets, bytes.Clone(packet))
		return len(packet), nil
	})
	sender.retransmitInterval = 0
	if err = sender.sendReaction("👍"); err != nil {
		t.Fatalf("send reaction: %v", err)
	}
	if len(packets) != appDataRetransmitCount {
		t.Fatalf("packet count = %d, want %d", len(packets), appDataRetransmitCount)
	}
	for i, packet := range packets {
		header, ok := rtp.ParseRtpHeader(packet)
		if !ok {
			t.Fatalf("packet %d has no RTP header", i)
		}
		if header.PayloadType != rtp.RtpPayloadTypeAppData || header.Ssrc != ssrc || header.SequenceNumber != uint16(i+1) || header.Timestamp != uint32((i+1)*appDataTimestampStep) {
			t.Fatalf("packet %d header = %+v", i, header)
		}
	}

	receiverPipe, err := NewMediaPipeline(callKey, "222222222222222:0@lid", "111111111111111:0@lid", ssrc, FrameSamples)
	if err != nil {
		t.Fatalf("receiver pipe: %v", err)
	}
	_, payload, ok := receiverPipe.UnprotectAudio(packets[0])
	if !ok {
		t.Fatal("receiver could not unprotect app-data packet")
	}
	if !bytes.Equal(payload, encodeAppDataReaction(1, "👍")) {
		t.Fatalf("decrypted payload = %x", payload)
	}
}

func TestMediaPayloadClassificationDoesNotTreatAppDataAsAudio(t *testing.T) {
	if got := classifyMediaPayload(rtp.RtpHeader{PayloadType: rtp.RtpPayloadTypeAppData}); got != mediaPayloadAppData {
		t.Fatalf("PT119 classified as %v, want app-data", got)
	}
	if got := classifyMediaPayload(rtp.RtpHeader{PayloadType: 118}); got != mediaPayloadUnknown {
		t.Fatalf("unknown PT classified as %v, want unknown", got)
	}
}

func TestCallSendsReactionThroughActiveAppDataStream(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	pipe, err := NewMediaPipeline(iota32(), "111111111111111:0@lid", "222222222222222:0@lid", 1234, FrameSamples)
	if err != nil {
		t.Fatalf("sender pipe: %v", err)
	}
	var sent int
	sender := newAppDataSender(pipe, 1234, func(packet []byte) (int, error) {
		sent++
		return len(packet), nil
	})
	sender.retransmitInterval = 0
	eng.calls[call.ID()].appDataTx = sender

	if err = call.SendReaction("👍"); err != nil {
		t.Fatalf("SendReaction: %v", err)
	}
	if sent != appDataRetransmitCount {
		t.Fatalf("sent %d packets, want %d", sent, appDataRetransmitCount)
	}
}

func TestCallSendReactionRequiresActiveAppDataStream(t *testing.T) {
	_, call := testEngineWithOutgoingCall()
	if err := call.SendReaction("👍"); !errors.Is(err, errAppDataUnavailable) {
		t.Fatalf("SendReaction error = %v, want %v", err, errAppDataUnavailable)
	}
}
