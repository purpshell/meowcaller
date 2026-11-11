package mlow

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestEncodeConcurrentColdStart(t *testing.T) {
	const childEnv = "MEOWCALLER_MLOW_COLD_START_CHILD"
	if os.Getenv(childEnv) != "1" {
		// Other codec tests may already have initialized the shared tables.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEncodeConcurrentColdStart$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cold concurrent encoding: %v\n%s", err, output)
		}
		return
	}

	runtime.GOMAXPROCS(4)
	pcm := make([]float32, 960)
	for i := range pcm {
		pcm[i] = float32(0.5 * math.Sin(2*math.Pi*550*float64(i)/16000))
	}

	const workers = 16
	type result struct {
		frame []byte
		err   error
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			frame, err := NewMlowEncoder().Encode(pcm)
			results <- result{frame: frame, err: err}
		}()
	}
	ready.Wait()
	close(start)

	var firstFrame []byte
	for i := range workers {
		r := <-results
		if r.err != nil {
			t.Fatalf("encoder %d: %v", i, r.err)
		}
		if len(r.frame) == 0 || r.frame[0] != 0x50 {
			t.Fatalf("encoder %d: expected active MLow frame", i)
		}
		if i == 0 {
			firstFrame = r.frame
		} else if !bytes.Equal(firstFrame, r.frame) {
			t.Fatalf("encoder %d: output differs for identical input", i)
		}
		if out := NewMlowDecoder().Decode(r.frame); len(out) != len(pcm) {
			t.Fatalf("encoder %d: decoded %d samples, want %d", i, len(out), len(pcm))
		}
	}
}
