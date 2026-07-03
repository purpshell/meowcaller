package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
)

func TestOpenAudioSourceRoutesOggToOpusDecoder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notice.ogg")
	if err := os.WriteFile(path, []byte("not ogg"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := openAudioSource(path)
	if err == nil {
		t.Fatal("invalid Ogg fixture unexpectedly decoded")
	}
	if strings.Contains(err.Error(), "unsupported audio extension") {
		t.Fatalf(".ogg was rejected before Opus decode: %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("open audio")) {
		t.Fatalf("error = %v, want decoder error wrapped as open audio", err)
	}
}

func TestParseNotifyArgs(t *testing.T) {
	var stderr strings.Builder
	cfg, err := parseNotifyArgs([]string{
		"--store", "/tmp/meowcaller-test.db",
		"--answer-timeout", "12s",
		"--max-duration", "0",
		"+15551234567", "notice.mulaw",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseNotifyArgs: %v", err)
	}
	if cfg.target != "+15551234567" || cfg.audioPath != "notice.mulaw" {
		t.Fatalf("positionals = %q, %q", cfg.target, cfg.audioPath)
	}
	if cfg.answerTimeout != 12*time.Second || cfg.maxDuration != 0 {
		t.Fatalf("durations = %s, %s", cfg.answerTimeout, cfg.maxDuration)
	}
}

func TestParseNotifyArgsRejectsNegativeDuration(t *testing.T) {
	_, err := parseNotifyArgs([]string{"--max-duration", "-1s", "target", "notice.wav"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("error = %v, want non-negative duration error", err)
	}
}

func TestSQLiteDSNEscapesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session name?#.db")
	dsn := sqliteDSN(path)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if parsed.Path != path {
		t.Fatalf("parsed path = %q, want %q", parsed.Path, path)
	}
	if got := parsed.Query()["_pragma"]; len(got) != 2 {
		t.Fatalf("pragma count = %d, want 2", len(got))
	}
}

func TestPrepareStorePathCreatesPrivateRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod private dir: %v", err)
	}
	path := filepath.Join(dir, "session.db")
	if err := prepareStorePath(path); err != nil {
		t.Fatalf("prepareStorePath: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, want regular 0600", info.Mode())
	}
}

func TestPrepareStorePathRejectsSharedDirectoryAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatalf("chmod shared dir: %v", err)
	}
	if err := prepareStorePath(filepath.Join(shared, "session.db")); err == nil || !strings.Contains(err.Error(), "mode 0700") {
		t.Fatalf("shared-directory error = %v, want mode 0700 rejection", err)
	}

	private := t.TempDir()
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatalf("chmod private dir: %v", err)
	}
	target := filepath.Join(private, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(private, "session.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := prepareStorePath(link); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error = %v, want regular-file rejection", err)
	}
}

func TestWaitNotificationCompletesAndHangsUp(t *testing.T) {
	ready := make(chan struct{}, 1)
	finished := make(chan error, 1)
	ended := make(chan string, 1)
	ready <- struct{}{}
	finished <- nil
	var hangups atomic.Int32
	err := waitNotification(context.Background(), ready, finished, ended, time.Second, time.Second, func() {}, func() error {
		hangups.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("waitNotification: %v", err)
	}
	if hangups.Load() != 1 {
		t.Fatalf("hangups = %d, want 1", hangups.Load())
	}
}

func TestRegisterNotificationCallbacksReplaysCurrentState(t *testing.T) {
	tests := []struct {
		name      string
		phase     meowcaller.CallPhase
		wantReady int
		wantEnd   int
	}{
		{name: "active", phase: meowcaller.CallPhaseActive, wantReady: 1},
		{name: "ended", phase: meowcaller.CallPhaseEnded, wantEnd: 1},
		{name: "calling", phase: meowcaller.CallPhaseCalling},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			call := &fakeNotificationCall{phase: tc.phase}
			var ready, ended atomic.Int32
			registerNotificationCallbacks(call, func() { ready.Add(1) }, func(string) { ended.Add(1) })
			if got := int(ready.Load()); got != tc.wantReady {
				t.Fatalf("ready callbacks = %d, want %d", got, tc.wantReady)
			}
			if got := int(ended.Load()); got != tc.wantEnd {
				t.Fatalf("end callbacks = %d, want %d", got, tc.wantEnd)
			}
		})
	}
}

type fakeNotificationCall struct {
	phase   meowcaller.CallPhase
	onReady func()
	onEnd   func(string)
}

func (c *fakeNotificationCall) OnReady(fn func())           { c.onReady = fn }
func (c *fakeNotificationCall) OnEnd(fn func(string))       { c.onEnd = fn }
func (c *fakeNotificationCall) State() meowcaller.CallPhase { return c.phase }

func TestWaitNotificationAnswerTimeout(t *testing.T) {
	var stopped, hungUp atomic.Bool
	err := waitNotification(
		context.Background(), make(chan struct{}), make(chan error), make(chan string),
		time.Millisecond, 0,
		func() { stopped.Store(true) },
		func() error { hungUp.Store(true); return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("error = %v, want answer timeout", err)
	}
	if !stopped.Load() || !hungUp.Load() {
		t.Fatalf("stopped=%v hungUp=%v, want both true", stopped.Load(), hungUp.Load())
	}
}

func TestWaitNotificationMaxDuration(t *testing.T) {
	ready := make(chan struct{}, 1)
	ready <- struct{}{}
	var stopped, hungUp atomic.Bool
	err := waitNotification(
		context.Background(), ready, make(chan error), make(chan string),
		time.Second, time.Millisecond,
		func() { stopped.Store(true) },
		func() error { hungUp.Store(true); return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "--max-duration") {
		t.Fatalf("error = %v, want max-duration error", err)
	}
	if !stopped.Load() || !hungUp.Load() {
		t.Fatalf("stopped=%v hungUp=%v, want both true", stopped.Load(), hungUp.Load())
	}
}

func TestWaitNotificationPeerEndsBeforePlayback(t *testing.T) {
	ended := make(chan string, 1)
	ended <- "rejected"
	err := waitNotification(
		context.Background(), make(chan struct{}), make(chan error), ended,
		time.Second, time.Second, func() {}, func() error { return errors.New("unexpected hangup") },
	)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("error = %v, want peer-end reason", err)
	}
}

func TestWaitNotificationReturnsPlaybackErrorAfterHangup(t *testing.T) {
	ready := make(chan struct{}, 1)
	finished := make(chan error, 1)
	ready <- struct{}{}
	finished <- errors.New("decode failed")
	var hungUp atomic.Bool
	err := waitNotification(
		context.Background(), ready, finished, make(chan string),
		time.Second, time.Second, func() {}, func() error { hungUp.Store(true); return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "decode failed") {
		t.Fatalf("error = %v, want playback error", err)
	}
	if !hungUp.Load() {
		t.Fatal("playback failure did not hang up")
	}
}

type failingAudioSource struct{ err error }

func (s failingAudioSource) ReadFrame() ([]float32, error) { return nil, s.err }
func (failingAudioSource) Close() error                    { return nil }

func TestMonitoredSourceReportsDecoderFailureAndEmptyInput(t *testing.T) {
	decodeErr := errors.New("bad packet")
	failed := &monitoredSource{AudioSource: failingAudioSource{err: decodeErr}}
	_, _ = failed.ReadFrame()
	if err := failed.result(); !errors.Is(err, decodeErr) {
		t.Fatalf("decoder result = %v, want %v", err, decodeErr)
	}

	empty := &monitoredSource{AudioSource: failingAudioSource{err: io.EOF}}
	_, _ = empty.ReadFrame()
	if err := empty.result(); err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Fatalf("empty result = %v, want no-frames error", err)
	}
}
