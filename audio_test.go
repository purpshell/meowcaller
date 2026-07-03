package meowcaller

import (
	"bytes"
	"io"
	"math"
	"testing"
)

func TestMuLawStreamFraming(t *testing.T) {
	src := MuLawStream(io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{0x80}, FrameSamples/2))))
	defer src.Close()

	frame, err := src.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(frame) != FrameSamples {
		t.Fatalf("frame samples = %d, want %d", len(frame), FrameSamples)
	}
	want := float32(32124.0 / 32768.0)
	for i, sample := range frame {
		if sample != want {
			t.Fatalf("sample[%d] = %v, want %v", i, sample, want)
		}
	}
	if _, err := src.ReadFrame(); err != io.EOF {
		t.Fatalf("second ReadFrame error = %v, want io.EOF", err)
	}
}

func TestMuLawStreamShortFramePadsAndDuplicates(t *testing.T) {
	src := MuLawStream(io.NopCloser(bytes.NewReader([]byte{0xff, 0x00})))
	defer src.Close()

	frame, err := src.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	wantNegative := float32(-32124.0 / 32768.0)
	if frame[0] != 0 || frame[1] != 0 || frame[2] != wantNegative || frame[3] != wantNegative {
		t.Fatalf("decoded prefix = %v, want [0 0 %v %v]", frame[:4], wantNegative, wantNegative)
	}
	for i, sample := range frame[4:] {
		if math.Float32bits(sample) != 0 {
			t.Fatalf("padding sample[%d] = %v, want 0", i+4, sample)
		}
	}
	if _, err := src.ReadFrame(); err != io.EOF {
		t.Fatalf("second ReadFrame error = %v, want io.EOF", err)
	}
}

func TestMuLawKnownCodeWords(t *testing.T) {
	tests := []struct {
		encoded byte
		want    int
	}{
		{encoded: 0xff, want: 0},
		{encoded: 0x7f, want: 0},
		{encoded: 0x80, want: 32124},
		{encoded: 0x00, want: -32124},
	}
	for _, tc := range tests {
		got := int(math.Round(float64(decodeMuLaw(tc.encoded) * 32768)))
		if got != tc.want {
			t.Errorf("decodeMuLaw(0x%02x) = %d, want %d", tc.encoded, got, tc.want)
		}
	}
}
