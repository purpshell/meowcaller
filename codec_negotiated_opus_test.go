package meowcaller

import (
	"errors"
	"math"
	"testing"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
)

type closeTrackingParticipantDecoder struct {
	closeCalls int
}

func (d *closeTrackingParticipantDecoder) Decode([]byte) []float32 {
	return make([]float32, FrameSamples)
}

func (d *closeTrackingParticipantDecoder) Close() error {
	d.closeCalls++
	return nil
}

func negotiatedOpusTestSignal() []float32 {
	pcm := make([]float32, FrameSamples)
	for i := range pcm {
		pcm[i] = float32(0.45 * math.Sin(2*math.Pi*440*float64(i)/SampleRate))
	}
	return pcm
}

func TestNegotiatedOpusMediaPathProducesPT120And960(t *testing.T) {
	codec := selectAudioCodec(&signaling.VoipSettings{
		Present:        true,
		UseMlowCodecV1: false,
	})
	if codec != AudioCodecOpus {
		t.Fatalf("selectAudioCodec(use_mlow=false) = %s, want opus", codec)
	}

	encoder, err := buildEncoder(codec, zerolog.Nop())
	if err != nil {
		t.Fatalf("buildEncoder(%s): %v", codec, err)
	}
	defer encoder.Close()

	callKey := make([]byte, 32)
	for i := range callKey {
		callKey[i] = byte(i + 1)
	}
	pipeline, err := NewMediaPipeline(
		callKey,
		"1:0@lid",
		"2:0@lid",
		0x10203040,
		FrameSamples,
	)
	if err != nil {
		t.Fatalf("NewMediaPipeline: %v", err)
	}

	protect := func() []byte {
		t.Helper()
		payload, encodeErr := encoder.Encode(negotiatedOpusTestSignal())
		if encodeErr != nil {
			t.Fatalf("encode negotiated Opus: %v", encodeErr)
		}
		packet, protectErr := pipeline.ProtectAudio(payload)
		if protectErr != nil {
			t.Fatalf("protect negotiated Opus: %v", protectErr)
		}
		return packet
	}

	firstPacket := protect()
	secondPacket := protect()
	first, ok := rtp.ParseRtpHeader(firstPacket)
	if !ok {
		t.Fatal("first protected packet has no RTP header")
	}
	second, ok := rtp.ParseRtpHeader(secondPacket)
	if !ok {
		t.Fatal("second protected packet has no RTP header")
	}
	if first.PayloadType != rtp.RtpPayloadTypeOpus || second.PayloadType != rtp.RtpPayloadTypeOpus {
		t.Fatalf("payload types = %d, %d; want %d", first.PayloadType, second.PayloadType, rtp.RtpPayloadTypeOpus)
	}
	if second.Timestamp-first.Timestamp != FrameSamples {
		t.Fatalf("timestamp step = %d, want %d", second.Timestamp-first.Timestamp, FrameSamples)
	}
	if first.Ssrc != 0x10203040 || second.Ssrc != 0x10203040 {
		t.Fatalf("SSRC = %#x, %#x; want %#x", first.Ssrc, second.Ssrc, uint32(0x10203040))
	}
}

func TestParticipantDecoderFactoryFailsClosed(t *testing.T) {
	want := errors.New("decoder unavailable")
	_, err := newParticipantReceiveRegistryWithDecoderFactory(
		"CID",
		make([]byte, 32),
		"1:0@lid",
		"2:0@lid",
		func() (participantAudioDecoder, error) { return nil, want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("registry error = %v, want %v", err, want)
	}
}

func TestParticipantDecoderClosedWithRegistry(t *testing.T) {
	decoder := &closeTrackingParticipantDecoder{}
	registry, err := newParticipantReceiveRegistryWithDecoderFactory(
		"CID",
		make([]byte, 32),
		"1:0@lid",
		"2:0@lid",
		func() (participantAudioDecoder, error) { return decoder, nil },
	)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	registry.clear()
	registry.clear()
	if decoder.closeCalls != 1 {
		t.Fatalf("decoder close calls = %d, want 1", decoder.closeCalls)
	}
}
