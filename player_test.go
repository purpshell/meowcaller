package meowcaller

import (
	"errors"
	"io"
	"testing"
)

// scriptedSource yields the given frames in order (nil means "nothing yet"), then EOF.
type scriptedSource struct {
	frames [][]float32
	closed bool
}

func (s *scriptedSource) ReadFrame() ([]float32, error) {
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

func (s *scriptedSource) Close() error { s.closed = true; return nil }

func ramp(start, n int) []float32 {
	f := make([]float32, n)
	for i := range f {
		f[i] = float32(start + i)
	}
	return f
}

func TestPlayerSplitsOversizeFrames(t *testing.T) {
	src := &scriptedSource{frames: [][]float32{ramp(0, 3*FrameSamples)}}
	p := NewPlayer()
	finished := 0
	p.OnFinish(func() { finished++ })
	p.Play(src)
	for i := 0; i < 3; i++ {
		f := p.nextFrame()
		if len(f) != FrameSamples {
			t.Fatalf("tick %d: got %d samples, want %d", i, len(f), FrameSamples)
		}
		if f[0] != float32(i*FrameSamples) {
			t.Fatalf("tick %d: first sample %v, want %v", i, f[0], i*FrameSamples)
		}
	}
	if f := p.nextFrame(); f != nil {
		t.Fatalf("after EOF got %d samples, want silence", len(f))
	}
	if finished != 1 || !src.closed || p.State() != PlayerIdle {
		t.Fatalf("finish not handled: finished=%d closed=%v state=%v", finished, src.closed, p.State())
	}
}

func TestPlayerJoinsShortFramesAndWaitsOnLiveSource(t *testing.T) {
	half := FrameSamples / 2
	src := &scriptedSource{frames: [][]float32{ramp(0, half), nil, ramp(half, half), ramp(FrameSamples, 100)}}
	p := NewPlayer()
	p.Play(src)
	if f := p.nextFrame(); f != nil {
		t.Fatalf("half a frame buffered: want silence, got %d samples", len(f))
	}
	f := p.nextFrame()
	if len(f) != FrameSamples || f[0] != 0 || f[FrameSamples-1] != float32(FrameSamples-1) {
		t.Fatalf("joined frame wrong: len=%d", len(f))
	}
	f = p.nextFrame() // 100 samples then EOF: padded
	if len(f) != FrameSamples || f[0] != float32(FrameSamples) || f[100] != 0 {
		t.Fatalf("final padded frame wrong: len=%d", len(f))
	}
}

func TestPlayerPassesExactFramesThrough(t *testing.T) {
	frame := ramp(7, FrameSamples)
	p := NewPlayer()
	p.Play(&scriptedSource{frames: [][]float32{frame}})
	got := p.nextFrame()
	if &got[0] != &frame[0] {
		t.Fatal("exact frame should be passed through without copying")
	}
}

type failingSource struct{}

func (failingSource) ReadFrame() ([]float32, error) { return nil, errors.New("decode") }
func (failingSource) Close() error                  { return nil }

func TestPlayerDropsCarryOnReplaceAndError(t *testing.T) {
	p := NewPlayer()
	p.Play(&scriptedSource{frames: [][]float32{ramp(0, FrameSamples+10), nil}})
	_ = p.nextFrame()
	p.Play(&scriptedSource{frames: [][]float32{ramp(100, FrameSamples)}})
	if f := p.nextFrame(); len(f) != FrameSamples || f[0] != 100 {
		t.Fatal("leftover samples of a replaced source leaked into the new one")
	}
	p.Play(failingSource{})
	if f := p.nextFrame(); f != nil || p.State() != PlayerIdle {
		t.Fatal("a failing source must end playback with silence")
	}
}
