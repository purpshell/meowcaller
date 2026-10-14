package meowcaller

import (
	"errors"
	"io"
	"math"
	"testing"
)

func TestOpusFileWithCommentHeader(t *testing.T) {
	source, err := OpusFile("testdata/opusfile/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	frames := 0
	var energy float64
	for {
		frame, err := source.ReadFrame()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(frame) != FrameSamples {
			t.Fatalf("frame length = %d, want %d", len(frame), FrameSamples)
		}
		for _, sample := range frame {
			if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
				t.Fatal("non-finite decoded sample")
			}
			energy += float64(sample) * float64(sample)
		}
		frames++
	}
	if frames != 17 {
		t.Fatalf("frames = %d, want 17", frames)
	}
	if energy < 1 {
		t.Fatalf("decoded tone is silent: energy = %g", energy)
	}
	if _, err := source.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("repeat EOF = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}
