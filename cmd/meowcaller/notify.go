package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
)

const (
	defaultAnswerTimeout = 45 * time.Second
	defaultMaxDuration   = 5 * time.Minute
)

type notifyConfig struct {
	target        string
	audioPath     string
	storePath     string
	answerTimeout time.Duration
	maxDuration   time.Duration
}

func parseNotifyArgs(args []string, stderr io.Writer) (notifyConfig, error) {
	var cfg notifyConfig
	defaultStore, err := defaultStorePath()
	if err != nil {
		return cfg, err
	}
	flags := flag.NewFlagSet("notify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.storePath, "store", defaultStore, "linked-device SQLite store")
	flags.DurationVar(&cfg.answerTimeout, "answer-timeout", defaultAnswerTimeout, "maximum wait for the recipient to answer; 0 disables")
	flags.DurationVar(&cfg.maxDuration, "max-duration", defaultMaxDuration, "maximum playback after answer; 0 streams to EOF")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: meowcaller notify [flags] <target> <audio.mp3|wav|ogg|opus|mulaw|ulaw|->")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return cfg, errors.Join(errUsage, err)
	}
	if flags.NArg() != 2 {
		flags.Usage()
		return cfg, errUsage
	}
	if cfg.answerTimeout < 0 || cfg.maxDuration < 0 {
		return cfg, errors.New("durations must be non-negative")
	}
	cfg.target = flags.Arg(0)
	cfg.audioPath = flags.Arg(1)
	return cfg, nil
}

func defaultStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "meowcaller", "whatsapp.db"), nil
}

func runNotify(ctx context.Context, cfg notifyConfig, stderr io.Writer) error {
	source, err := openAudioSource(cfg.audioPath)
	if err != nil {
		return err
	}
	defer source.Close()

	log := zerolog.Ctx(ctx)
	session, err := openSession(ctx, cfg.storePath, *log)
	if err != nil {
		return err
	}
	defer session.Close()

	client := meowcaller.NewClient(session.client, meowcaller.WithLogger(*log))
	if err := session.ConnectPaired(ctx); err != nil {
		return err
	}

	call, err := client.Call(ctx, cfg.target)
	if err != nil {
		return fmt.Errorf("place call: %w", err)
	}
	return playNotification(ctx, call, source, cfg.answerTimeout, cfg.maxDuration)
}

func openAudioSource(path string) (meowcaller.AudioSource, error) {
	if path == "-" {
		return meowcaller.MuLawStream(io.NopCloser(os.Stdin)), nil
	}
	var (
		source meowcaller.AudioSource
		err    error
	)
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".mp3":
		source, err = meowcaller.MP3File(path)
	case ".wav":
		source, err = meowcaller.WAVFile(path)
	case ".ogg", ".opus":
		source, err = meowcaller.OpusFile(path)
	case ".mulaw", ".ulaw":
		source, err = meowcaller.MuLawFile(path)
	default:
		return nil, fmt.Errorf("unsupported audio extension %q (want .mp3, .wav, .ogg, .opus, .mulaw, or .ulaw; '-' is PCMU stdin)", ext)
	}
	if err != nil {
		return nil, fmt.Errorf("open audio %q: %w", path, err)
	}
	return source, nil
}

func playNotification(ctx context.Context, call *meowcaller.Call, source meowcaller.AudioSource, answerTimeout, maxDuration time.Duration) error {
	ready := make(chan struct{}, 1)
	finished := make(chan error, 1)
	ended := make(chan string, 1)
	player := meowcaller.NewPlayer()
	monitored := &monitoredSource{AudioSource: source}
	var start sync.Once

	player.OnFinish(func() { finished <- monitored.result() })
	call.Subscribe(player)
	startPlayback := func() {
		start.Do(func() {
			player.Play(monitored)
			ready <- struct{}{}
		})
	}
	reportEnd := func(reason string) {
		select {
		case ended <- reason:
		default:
		}
	}
	registerNotificationCallbacks(call, startPlayback, reportEnd)
	return waitNotification(ctx, ready, finished, ended, answerTimeout, maxDuration, player.Stop, call.Hangup)
}

type notificationCallState interface {
	OnReady(func())
	OnEnd(func(string))
	State() meowcaller.CallPhase
}

func registerNotificationCallbacks(call notificationCallState, onReady func(), onEnd func(string)) {
	call.OnReady(onReady)
	call.OnEnd(onEnd)
	switch call.State() {
	case meowcaller.CallPhaseActive:
		onReady()
	case meowcaller.CallPhaseEnded:
		onEnd("ended before callback registration")
	}
}

func waitNotification(
	ctx context.Context,
	ready <-chan struct{},
	finished <-chan error,
	ended <-chan string,
	answerTimeout, maxDuration time.Duration,
	stop func(),
	hangup func() error,
) error {
	answerTimer, answerC := optionalTimer(answerTimeout)
	if answerTimer != nil {
		defer answerTimer.Stop()
	}
	select {
	case <-ready:
	case reason := <-ended:
		return fmt.Errorf("call ended before playback: %s", reason)
	case <-answerC:
		stop()
		_ = hangup()
		return fmt.Errorf("recipient did not answer within %s", answerTimeout)
	case <-ctx.Done():
		stop()
		_ = hangup()
		return ctx.Err()
	}

	playTimer, playC := optionalTimer(maxDuration)
	if playTimer != nil {
		defer playTimer.Stop()
	}
	select {
	case playbackErr := <-finished:
		hangupErr := hangup()
		if playbackErr != nil {
			if hangupErr != nil {
				return errors.Join(playbackErr, fmt.Errorf("hang up after playback: %w", hangupErr))
			}
			return playbackErr
		}
		if hangupErr != nil {
			return fmt.Errorf("hang up after playback: %w", hangupErr)
		}
		return nil
	case reason := <-ended:
		stop()
		return fmt.Errorf("call ended during playback: %s", reason)
	case <-playC:
		stop()
		_ = hangup()
		return fmt.Errorf("notification exceeded --max-duration %s", maxDuration)
	case <-ctx.Done():
		stop()
		_ = hangup()
		return ctx.Err()
	}
}

type monitoredSource struct {
	meowcaller.AudioSource

	mu     sync.Mutex
	frames int
	err    error
}

func (s *monitoredSource) ReadFrame() ([]float32, error) {
	frame, err := s.AudioSource.ReadFrame()
	s.mu.Lock()
	if len(frame) > 0 {
		s.frames++
	}
	if err != nil && !errors.Is(err, io.EOF) && s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	return frame, err
}

func (s *monitoredSource) result() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return fmt.Errorf("audio playback failed: %w", s.err)
	}
	if s.frames == 0 {
		return errors.New("audio input contained no frames")
	}
	return nil
}

func optionalTimer(duration time.Duration) (*time.Timer, <-chan time.Time) {
	if duration == 0 {
		return nil, nil
	}
	timer := time.NewTimer(duration)
	return timer, timer.C
}
