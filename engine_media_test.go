package meowcaller

import (
	"testing"
	"time"

	"github.com/purpshell/meowcaller/rtp"
)

func TestVideoRtpDurationSamples(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     uint32
	}{
		{name: "zero uses fallback", duration: 0, want: defaultVideoRtpStepSamples},
		{name: "negative uses fallback", duration: -time.Millisecond, want: defaultVideoRtpStepSamples},
		{name: "30 fps", duration: time.Second / 30, want: 3000},
		{name: "60 fps", duration: time.Second / 60, want: 1500},
		{name: "sub sample uses fallback", duration: time.Nanosecond, want: defaultVideoRtpStepSamples},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := videoRtpDurationSamples(tt.duration); got != tt.want {
				t.Fatalf("videoRtpDurationSamples(%s) = %d, want %d", tt.duration, got, tt.want)
			}
		})
	}
}

func TestVideoSenderStartsAtIDRAndUsesWhatsappHeaders(t *testing.T) {
	callKey := iota32()
	pipe, err := NewMediaPipeline(callKey, "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{
		pipe:             pipe,
		stream:           rtp.NewVideoRtpStream(0x55667788, 4500),
		keyframeRequired: true,
	}

	delta := []byte{0, 0, 0, 1, 0x41, 1, 2, 3}
	if packets := sender.protectAccessUnit(delta, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("dependent frame produced %d packets before an IDR", len(packets))
	}

	idr := []byte{
		0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
		0, 0, 0, 1, 0x68, 0xce, 6, 0xe2,
		0, 0, 0, 1, 0x65, 1, 2, 3,
	}
	packets := sender.protectAccessUnit(idr, 50*time.Millisecond)
	if len(packets) != 3 {
		t.Fatalf("IDR produced %d packets, want 3", len(packets))
	}
	for i, packet := range packets {
		header, ok := rtp.ParseRtpHeader(packet)
		if !ok {
			t.Fatalf("packet %d has no RTP header", i)
		}
		if n, ok := rtp.RtpHeaderByteLength(packet); !ok || n != rtp.WhatsappVideoRtpHeaderSize {
			t.Fatalf("packet %d header length = (%d, %v), want (%d, true)", i, n, ok, rtp.WhatsappVideoRtpHeaderSize)
		}
		if header.VideoExtension == nil || header.VideoExtension.MediaFrameInfo != rtp.VideoMediaFrameInfoIDR {
			t.Fatalf("packet %d video extension = %+v", i, header.VideoExtension)
		}
	}
}

func TestMediaSrtcpSenderProtectsVideoReport(t *testing.T) {
	sender, err := newMediaSrtcpSender(iota32(), "111111111111111:0@lid", 0x55667788, true)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	packet, err := sender.senderReport(rtp.RtcpSenderStats{
		PacketsSent:  3,
		OctetsSent:   400,
		RtpTimestamp: 90000,
	}, 1700000000000)
	if err != nil {
		t.Fatalf("sender report: %v", err)
	}
	if kind := rtp.IsRtcpPacket(packet); !kind {
		t.Fatal("protected sender report is not classified as RTCP")
	}
	if len(packet) != 60+14 {
		t.Fatalf("protected report length = %d, want 74", len(packet))
	}
}
