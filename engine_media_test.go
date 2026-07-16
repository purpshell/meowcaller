package meowcaller

import (
	"testing"
	"time"
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
