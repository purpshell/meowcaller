package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mdp/qrterminal/v3"
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

type linkedSession struct {
	client    *whatsmeow.Client
	container *sqlstore.Container
}

func openSession(ctx context.Context, path string, log zerolog.Logger) (*linkedSession, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve store path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := prepareStorePath(absPath); err != nil {
		return nil, err
	}
	store.DeviceProps.Os = proto.String("Mac OS")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
	waLogger := waLog.Zerolog(log.Level(zerolog.WarnLevel))

	container, err := sqlstore.New(ctx, "sqlite", sqliteDSN(absPath), waLogger.Sub("db"))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		container.Close()
		return nil, fmt.Errorf("load device: %w", err)
	}
	return &linkedSession{
		client:    whatsmeow.NewClient(device, waLogger.Sub("wa")),
		container: container,
	}, nil
}

func prepareStorePath(path string) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect store directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("store parent %q is not a directory", parent)
	}
	if runtime.GOOS != "windows" && parentInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("store directory %q must not be accessible by group or other users (want mode 0700)", parent)
	}

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return fmt.Errorf("create private store: %w", createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("close private store: %w", closeErr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("inspect store: %w", err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("store %q must be a regular file, not %s", path, info.Mode().Type())
	default:
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("restrict store permissions: %w", err)
		}
		return nil
	}
}

func sqliteDSN(path string) string {
	urlPath := filepath.ToSlash(path)
	if len(urlPath) >= 3 && urlPath[1] == ':' && urlPath[2] == '/' {
		urlPath = "/" + urlPath
	}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	return (&url.URL{Scheme: "file", Path: urlPath, RawQuery: query.Encode()}).String()
}

func (s *linkedSession) Connect(ctx context.Context, qrOut io.Writer, allowQR bool) error {
	if s.client.Store.ID == nil {
		if !allowQR {
			return errors.New("linked device not paired; rerun notify in an interactive terminal and scan the QR code")
		}
		qr, err := s.client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("start QR pairing: %w", err)
		}
		if err := s.client.Connect(); err != nil {
			return fmt.Errorf("connect for QR pairing: %w", err)
		}
		paired := false
		for event := range qr {
			switch event.Event {
			case whatsmeow.QRChannelEventCode:
				fmt.Fprintf(qrOut, "Scan in WhatsApp > Linked devices (valid for %s):\n", event.Timeout.Round(time.Second))
				qrterminal.GenerateHalfBlock(event.Code, qrterminal.L, qrOut)
			case whatsmeow.QRChannelSuccess.Event:
				paired = true
			case whatsmeow.QRChannelEventError:
				return fmt.Errorf("QR pairing: %w", event.Error)
			default:
				if event.Event != whatsmeow.QRChannelSuccess.Event {
					return fmt.Errorf("QR pairing ended: %s", event.Event)
				}
			}
		}
		if !paired {
			return errors.New("QR pairing ended before success")
		}
	} else if err := s.client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if err := waitUntilReady(ctx, s.client, time.Minute); err != nil {
		return err
	}
	if s.client.Store.PushName == "" {
		s.client.Store.PushName = "meowcaller"
	}
	if err := s.client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("send presence failed; continuing")
	}
	return nil
}

func waitUntilReady(ctx context.Context, client *whatsmeow.Client, timeout time.Duration) error {
	ready := make(chan struct{}, 1)
	id := client.AddEventHandler(func(event any) {
		if _, ok := event.(*events.Connected); ok {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})
	defer client.RemoveEventHandler(id)
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

func (s *linkedSession) Close() error {
	s.client.Disconnect()
	return s.container.Close()
}
