package meowcaller

import "testing"

func TestAudioCodecString(t *testing.T) {
	if got := AudioCodecMlow.String(); got != "mlow" {
		t.Fatalf("AudioCodecMlow.String() = %q, want mlow", got)
	}
	if got := AudioCodecOpus.String(); got != "opus" {
		t.Fatalf("AudioCodecOpus.String() = %q, want opus", got)
	}
}
