// Command cli is a cross-platform demo of the meowcaller managed calling API.
//
//	cli call <target>             Log in and place a 1:1 call; mic ↔ speaker.
//	cli play <target> <file>      Place a call and play a .mp3/.wav/.opus file
//	                              to the peer (peer audio goes to the speaker).
//	cli listen                    Log in and print incoming call signaling.
//	cli autoaccept [file.wav]     Log in and auto-answer incoming calls, wiring
//	                              mic ↔ speaker (or recording the peer to file.wav).
//
// meowcaller wraps an already-connected whatsmeow client: this command owns the
// whatsmeow login/QR boilerplate and the logger, then hands the connected client
// to meowcaller.NewClient and drives everything through the managed Call API.
//
// Audio is captured/played via miniaudio (the meowcaller/audio/malgo subpackage),
// so it runs on macOS, Linux and Windows with the OS default mic and speaker.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	// As the top-level program, this command owns logger configuration (the library
	// packages only accept a logger). A console writer keeps the demo readable; the
	// logger is embedded in the context so the boilerplate resolves it with
	// zerolog.Ctx(ctx), and passed to meowcaller via WithLogger.
	// --diagdump <dir> is a developer flag: pull it (and its value) out of os.Args
	// before the positional dispatch, then tee the structured log stream into the
	// diag recorder so the raw wa/Recv|wa/Send stanza XML lands in xmpp.jsonl.
	diagDir, args := parseDiagDump(os.Args)
	os.Args = args

	level := zerolog.DebugLevel
	if lvl, err := zerolog.ParseLevel(os.Getenv("MEOW_LOG_LEVEL")); err == nil && lvl != zerolog.NoLevel {
		level = lvl
	}
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05.000"}
	var rec *diag.Recorder
	var logWriter io.Writer = consoleWriter
	if diagDir != "" {
		var derr error
		rec, derr = diag.NewRecorder(diagDir)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "diagdump: %v\n", derr)
			os.Exit(2)
		}
		defer rec.Close()
		// whatsmeow logs each stanza's XML at debug; keep at least debug so the xmpp
		// stream is populated even if MEOW_LOG_LEVEL asked for something quieter.
		if level > zerolog.DebugLevel {
			level = zerolog.DebugLevel
		}
		logWriter = zerolog.MultiLevelWriter(consoleWriter, newDiagSplitter(rec))
		fmt.Fprintf(os.Stderr, "diagdump: writing call diagnostics (incl. raw key/media material) to %s\n", diagDir)
	}
	logger := zerolog.New(logWriter).
		Level(level).
		With().Timestamp().Logger()

	if len(os.Args) < 2 {
		usage()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithContext(ctx)

	var err error
	switch os.Args[1] {
	case "call":
		if len(os.Args) < 3 {
			usage()
		}
		err = runCall(ctx, rec, os.Args[2], "")
	case "play":
		if len(os.Args) < 4 {
			usage()
		}
		err = runCall(ctx, rec, os.Args[2], os.Args[3])
	case "listen":
		err = runListen(ctx, rec, false, "")
	case "autoaccept":
		recordPath := ""
		if len(os.Args) > 2 {
			recordPath = os.Args[2]
		}
		err = runListen(ctx, rec, true, recordPath)
	default:
		usage()
	}
	if err != nil {
		logger.Fatal().Err(err).Str("command", os.Args[1]).Msg("cli command failed")
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cli [--diagdump <dir>] <call <target> | play <target> <file.mp3|wav|opus> | listen | autoaccept [record.wav]>")
	os.Exit(2)
}

// parseDiagDump pulls "--diagdump <dir>" (or "--diagdump=<dir>") out of argv,
// returning the dir ("" if absent) and argv with the flag removed so the rest of
// main keeps using os.Args positionally.
func parseDiagDump(argv []string) (string, []string) {
	out := make([]string, 0, len(argv))
	dir := ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--diagdump":
			if i+1 < len(argv) {
				dir = argv[i+1]
				i++
			}
		case strings.HasPrefix(a, "--diagdump="):
			dir = strings.TrimPrefix(a, "--diagdump=")
		default:
			out = append(out, a)
		}
	}
	return dir, out
}

// diagSplitter receives each finished zerolog event line (a JSON object) and routes
// it into the diag Recorder by its "sublogger" field: whatsmeow's wa/Recv and
// wa/Send stanza dumps (raw XML in the "message" field) go to the xmpp stream,
// everything else to a general log stream. It is the source of truth for raw wire
// XML; engine-side semantic diagnostics are emitted separately via the recorder.
type diagSplitter struct{ rec *diag.Recorder }

func newDiagSplitter(rec *diag.Recorder) *diagSplitter { return &diagSplitter{rec: rec} }

func (d *diagSplitter) Write(p []byte) (int, error) {
	var ev map[string]any
	if err := json.Unmarshal(p, &ev); err != nil {
		// Not JSON (shouldn't happen for a zerolog event); swallow, never break logging.
		return len(p), nil
	}
	stream := "log"
	if sub, ok := ev["sublogger"].(string); ok && strings.HasPrefix(sub, "wa") {
		stream = "xmpp"
	}
	d.rec.Emit(stream, ev)
	return len(p), nil
}

// runCall logs in, places a managed call to target, and attaches audio. With an
// empty file the local mic is sent and the peer is played to the speaker; with a
// file path the file is streamed to the peer instead (peer still goes to speaker).
func runCall(ctx context.Context, rec *diag.Recorder, target, file string) error {
	log := zerolog.Ctx(ctx)
	wa, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer wa.Disconnect()

	client := meowcaller.NewClient(wa, meowcaller.WithLogger(*zerolog.Ctx(ctx)), meowcaller.WithDiagnostics(rec))

	call, err := client.Call(ctx, target)
	if err != nil {
		return fmt.Errorf("place call: %w", err)
	}
	wireLifecycle(ctx, call)

	if file != "" {
		src, err := openFileSource(file)
		if err != nil {
			return fmt.Errorf("open %s: %w", file, err)
		}
		call.OnReady(func() {
			log.Info().Str("file", file).Msg("media ready; playing file to peer")
			call.Play(src)
		})
	} else if err := wireMic(call); err != nil {
		return err
	}
	if err := wireSpeaker(call); err != nil {
		return err
	}

	log.Info().Str("call_id", call.ID()).Msg("call placed; media starts when the peer answers. Ctrl+C to stop")
	<-ctx.Done()
	return nil
}

// runListen logs in and reports incoming calls. With autoAccept it answers each
// one and wires mic ↔ speaker, or records the peer's audio to recordPath (.wav).
func runListen(ctx context.Context, rec *diag.Recorder, autoAccept bool, recordPath string) error {
	log := zerolog.Ctx(ctx)
	wa, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer wa.Disconnect()

	client := meowcaller.NewClient(wa, meowcaller.WithLogger(*zerolog.Ctx(ctx)), meowcaller.WithDiagnostics(rec))
	client.OnIncomingCall(func(call *meowcaller.Call) {
		log.Info().Str("call_id", call.ID()).Str("peer", call.Peer().String()).Bool("auto_accept", autoAccept).Msg("incoming call")
		if !autoAccept {
			return
		}
		if err := call.Answer(); err != nil {
			log.Error().Err(err).Str("call_id", call.ID()).Msg("answer failed")
			return
		}
		wireLifecycle(ctx, call)
		if err := wireMic(call); err != nil {
			log.Error().Err(err).Msg("open mic failed")
		}
		// Ephemeral video bridge: open the printed URL in a browser to SEE the peer's video
		// and SEND your camera (the browser does H.264 decode/encode via WebCodecs;
		// meowcaller carries it over the relay). Closed on Ctrl+C.
		if vb, err := newVideoBridge(*zerolog.Ctx(ctx)); err == nil {
			call.ReceiveVideo(meowcaller.VideoSinkFunc(vb.WriteFrame))
			call.OnVideoState(func(s meowcaller.VideoState) { vb.SetOrientation(s.Orientation) })
			vb.OnFrame(func(au []byte) { _ = call.SendVideo(au) })
			go func() { <-ctx.Done(); _ = vb.Close() }()
			log.Info().Str("video_url", vb.URL()).Bool("offer_is_video", call.IsVideo()).Msg("open in a browser to see/send video")
		} else {
			log.Error().Err(err).Msg("video bridge failed")
		}
		if recordPath != "" {
			rec, err := meowcaller.WAVRecorder(recordPath)
			if err != nil {
				log.Error().Err(err).Str("path", recordPath).Msg("open recorder failed")
				return
			}
			call.Receive(rec)
			log.Info().Str("path", recordPath).Msg("recording peer audio to file")
		} else if err := wireSpeaker(call); err != nil {
			log.Error().Err(err).Msg("open speaker failed")
		}
	})

	log.Info().Bool("auto_accept", autoAccept).Msg("listening for calls. Ctrl+C to stop")
	<-ctx.Done()
	return nil
}

// wireMic captures the OS mic and plays it into the call as outbound audio.
func wireMic(call *meowcaller.Call) error {
	mic, err := malgo.Mic()
	if err != nil {
		return fmt.Errorf("open mic: %w", err)
	}
	call.Play(mic)
	return nil
}

// wireSpeaker routes the peer's decoded audio to the OS speaker.
func wireSpeaker(call *meowcaller.Call) error {
	speaker, err := malgo.Speaker()
	if err != nil {
		return fmt.Errorf("open speaker: %w", err)
	}
	call.Receive(speaker)
	return nil
}

// wireLifecycle logs the call's ready/end/state transitions.
func wireLifecycle(ctx context.Context, call *meowcaller.Call) {
	log := zerolog.Ctx(ctx)
	call.OnReady(func() { log.Info().Str("call_id", call.ID()).Msg("media flowing") })
	call.OnEnd(func(reason string) { log.Info().Str("call_id", call.ID()).Str("reason", reason).Msg("call ended") })
	call.OnStateChange(func(p meowcaller.CallPhase) {
		log.Info().Str("call_id", call.ID()).Int("phase", int(p)).Msg("call state")
	})
}

// openFileSource opens a .mp3/.wav/.opus file as an AudioSource via the managed
// decoders (extension-based; the constructors return an error on a format mismatch).
func openFileSource(file string) (meowcaller.AudioSource, error) {
	switch ext := strings.ToLower(filepath.Ext(file)); ext {
	case ".mp3":
		return meowcaller.MP3File(file)
	case ".wav":
		return meowcaller.WAVFile(file)
	case ".opus":
		return meowcaller.OpusFile(file)
	default:
		return nil, fmt.Errorf("unsupported audio file extension %q (want .mp3/.wav/.opus)", ext)
	}
}

// connectClient opens whatsmeow's auth store and logs in (QR on first run),
// returning a connected client. This is the consumer's responsibility —
// meowcaller wraps an already-connected client. busy_timeout absorbs brief lock
// contention so a busy session doesn't error out with "database is locked".
func connectClient(ctx context.Context) (*whatsmeow.Client, error) {
	log := zerolog.Ctx(ctx)
	// Present as a Google Chrome web client. The connection already advertises the
	// WEB platform; these companion props make the linked-device entry read
	// "Google Chrome (Mac OS)" instead of the default. DeviceProps is read at
	// pairing time, so re-pair for an already-linked device to pick this up.
	store.DeviceProps.Os = proto.String("Mac OS")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()

	container, err := sqlstore.New(ctx, "sqlite", "file:wa-voip.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", waLog.Zerolog(*zerolog.Ctx(ctx)).Sub("db"))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	client := whatsmeow.NewClient(device, waLog.Zerolog(*zerolog.Ctx(ctx)).Sub("wa"))

	if client.Store.ID == nil {
		qr, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		for evt := range qr {
			if evt.Event == "code" {
				log.Info().Int("valid_s", int(evt.Timeout.Seconds())).Str("qr_code", evt.Code).Msg("scan in WhatsApp > Linked devices")
			} else {
				log.Info().Str("event", evt.Event).Msg("login event")
			}
		}
	} else if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	// After QR pairing the server sends a 515 and whatsmeow disconnects to reconnect
	// with the new creds. WaitForConnection bails on that *expected* disconnect, so we
	// instead wait for the Connected event (dispatched only after authentication) and
	// the connected+logged-in state to settle across the reconnect.
	if err := waitUntilReady(ctx, client, 60*time.Second); err != nil {
		return nil, err
	}
	log.Info().Str("self_lid", client.Store.GetLID().String()).Msg("connected")

	// A device with no push name can't send presence; give it one, then announce
	// availability so the server delivers call signaling to us.
	if client.Store.PushName == "" {
		client.Store.PushName = "meowcaller"
	}
	if err := client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		log.Warn().Err(err).Msg("send presence failed; continuing")
	}
	return client, nil
}

// waitUntilReady blocks until the client is connected and logged in, tolerating the
// expected post-pair (515) disconnect+reconnect. It keys off events.Connected, which
// whatsmeow dispatches only after successful authentication, so it returns once the
// reconnect-with-creds has fully settled rather than aborting on the planned drop.
func waitUntilReady(ctx context.Context, client *whatsmeow.Client, timeout time.Duration) error {
	ready := make(chan struct{}, 8)
	id := client.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Connected); ok {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	})
	defer client.RemoveEventHandler(id)

	deadline := time.After(timeout)
	for !(client.IsConnected() && client.IsLoggedIn()) {
		select {
		case <-ready:
		case <-deadline:
			return errors.New("timed out waiting for whatsmeow connection")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
