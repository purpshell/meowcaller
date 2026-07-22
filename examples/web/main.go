// Command web runs a localhost browser console for meowcaller audio and video calls.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/audio/malgo"
	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

func main() {
	diagDir := flag.String("diagdump", "", "write sensitive call diagnostics to this directory")
	flag.Parse()

	console := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.000"}
	logger := zerolog.New(console).Level(zerolog.DebugLevel).With().Timestamp().Logger()
	ctx, stop := signal.NotifyContext(logger.WithContext(context.Background()), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var recorder *diag.Recorder
	if *diagDir != "" {
		var err error
		recorder, err = diag.NewRecorder(*diagDir)
		if err != nil {
			logger.Fatal().Err(err).Msg("open diagnostics")
		}
		defer recorder.Close()
	}
	if err := runWebConsole(ctx, recorder); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal().Err(err).Msg("web console failed")
	}
}

func connectManagedClient(ctx context.Context, recorder *diag.Recorder, onQR ...func(string, time.Duration)) (*whatsmeow.Client, *meowcaller.Client, error) {
	store.DeviceProps.Os = proto.String("Mac OS")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
	logger := zerolog.Ctx(ctx)
	container, err := sqlstore.New(ctx, "sqlite", "file:wa-voip-web.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", waLog.Zerolog(*logger).Sub("db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load device: %w", err)
	}
	wa := whatsmeow.NewClient(device, waLog.Zerolog(*logger).Sub("wa"))
	client := meowcaller.NewClient(wa, meowcaller.WithLogger(*logger), meowcaller.WithDiagnostics(recorder))
	if err = connectClient(ctx, wa, onQR...); err != nil {
		return nil, nil, err
	}
	return wa, client, nil
}

func connectClient(ctx context.Context, client *whatsmeow.Client, onQR ...func(string, time.Duration)) error {
	logger := zerolog.Ctx(ctx)
	if client.Store.ID == nil {
		qr, err := client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("open QR channel: %w", err)
		}
		if err = client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		for event := range qr {
			if event.Event == "code" {
				for _, callback := range onQR {
					if callback != nil {
						callback(event.Code, event.Timeout)
					}
				}
			} else {
				logger.Info().Str("event", event.Event).Msg("pairing event")
			}
		}
	} else if err := client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if err := waitUntilReady(ctx, client, time.Minute); err != nil {
		return err
	}
	if client.Store.PushName == "" {
		client.Store.PushName = "meowcaller web"
	}
	if err := client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		logger.Warn().Err(err).Msg("send presence failed")
	}
	logger.Info().Str("self_lid", client.Store.GetLID().String()).Msg("connected")
	return nil
}

func waitUntilReady(ctx context.Context, client *whatsmeow.Client, timeout time.Duration) error {
	ready := make(chan struct{}, 1)
	handlerID := client.AddEventHandler(func(event any) {
		if _, ok := event.(*events.Connected); ok {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})
	defer client.RemoveEventHandler(handlerID)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for !(client.IsConnected() && client.IsLoggedIn()) {
		select {
		case <-ready:
		case <-timer.C:
			return errors.New("timed out waiting for WhatsApp connection")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func wireMic(call *meowcaller.Call) error {
	mic, err := malgo.Mic()
	if err != nil {
		return fmt.Errorf("open mic: %w", err)
	}
	call.Play(mic)
	return nil
}

func wireSpeaker(call *meowcaller.Call) error {
	speaker, err := malgo.Speaker()
	if err != nil {
		return fmt.Errorf("open speaker: %w", err)
	}
	call.Receive(speaker)
	return nil
}
