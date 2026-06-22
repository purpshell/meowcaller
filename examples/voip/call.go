package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

// connectClient opens the local store and logs in (QR on first run), returning a
// connected client.
func connectClient(ctx context.Context) (*whatsmeow.Client, error) {
	container, err := sqlstore.New(ctx, "sqlite", "file:wa-voip.db?_pragma=foreign_keys(1)", waLog.Stdout("db", "WARN", true))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	client := whatsmeow.NewClient(device, waLog.Stdout("wa", "INFO", true))

	if client.Store.ID == nil {
		qr, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		for evt := range qr {
			if evt.Event == "code" {
				log.Printf("scan in WhatsApp ▸ Linked devices (valid %.0fs):\n%s", evt.Timeout.Seconds(), evt.Code)
			} else {
				log.Printf("login: %s", evt.Event)
			}
		}
	} else if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	// Connect()/QR pairing return before the socket handshake is done; wait briefly on
	// this first connect until it's ready before issuing any usync/call traffic.
	if !client.WaitForConnection(50 * time.Second) {
		return nil, errors.New("timed out waiting for whatsmeow connection")
	}
	log.Printf("connected as %s", client.Store.GetLID())

	// Sync the critical app-state (push name / settings) so usync, privacy tokens and
	// contacts behave; tolerate a sync failure rather than abort the session.
	if err := client.FetchAppState(ctx, appstate.WAPatchCriticalBlock, false, true); err != nil {
		log.Printf("app-state sync (critical_block): %v — continuing", err)
	}
	// A device with no push name can't send presence; give it one, then announce
	// availability so the server delivers call signaling to us.
	if client.Store.PushName == "" {
		client.Store.PushName = "meowcaller"
	}
	if err := client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		log.Printf("send presence: %v — continuing", err)
	}
	return client, nil
}

// resolvePeerLID turns a CLI target (phone number, phone JID, or @lid JID) into the
// peer's LID — the address the call's E2E keys and SSRCs derive from. This is the
// "resolve the LID before the call" step; a phone JID is mapped via the LID store,
// seeded by a usync query if not cached.
func resolvePeerLID(ctx context.Context, cli *whatsmeow.Client, target string) (types.JID, error) {
	jid, err := types.ParseJID(target)
	if err != nil {
		jid = types.NewJID(strings.TrimPrefix(strings.TrimSpace(target), "+"), types.DefaultUserServer)
	}
	if jid.Server == types.HiddenUserServer {
		return jid, nil // already a LID
	}
	if lid, err := cli.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lid.IsEmpty() {
		return lid, nil
	}
	// usync: GetUserInfo issues the lid-bearing query and persists the PN→LID mapping.
	info, err := cli.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		return types.EmptyJID, fmt.Errorf("usync %s: %w", jid.User, err)
	}
	for _, ui := range info {
		if !ui.LID.IsEmpty() {
			return ui.LID, nil
		}
	}
	if lid, err := cli.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lid.IsEmpty() {
		return lid, nil
	}
	return types.EmptyJID, fmt.Errorf("usync returned no LID for %s (peer unreachable or not on WhatsApp)", jid.User)
}

// callKeyPlaintext wraps the raw callKey as the Signal message body
// Message{Call{CallKey}} (whatsmeow adds Signal padding during encryption).
func callKeyPlaintext(callKey []byte) ([]byte, error) {
	return proto.Marshal(&waE2E.Message{Call: &waE2E.Call{CallKey: callKey}})
}

// encryptCallKeyForDevice encrypts the callKey to one peer device's Signal session,
// fetching a pre-key bundle if no session exists yet. Returns the ciphertext and the
// enc type ("pkmsg" for a fresh session, "msg" for an existing one).
func encryptCallKeyForDevice(ctx context.Context, cli *whatsmeow.Client, dev types.JID, callKey []byte) ([]byte, string, error) {
	pt, err := callKeyPlaintext(callKey)
	if err != nil {
		return nil, "", err
	}
	di := cli.DangerousInternals()
	enc, _, err := di.EncryptMessageForDevice(ctx, pt, dev, nil, nil, nil)
	if err != nil {
		bundles := di.FetchPreKeysNoError(ctx, []types.JID{dev})
		enc, _, err = di.EncryptMessageForDevice(ctx, pt, dev, bundles[dev], nil, nil)
		if err != nil {
			return nil, "", err
		}
	}
	ct, ok := enc.Content.([]byte)
	if !ok {
		return nil, "", errors.New("enc node has no ciphertext")
	}
	return ct, enc.AttrGetter().String("type"), nil
}

// runCall connects, resolves the peer LID, discovers devices, encrypts a fresh
// callKey per device, and sends the <call><offer>.
func runCall(ctx context.Context, target string) error {
	cli, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Disconnect()

	self := cli.Store.GetLID()
	if self.IsEmpty() {
		return errors.New("no own LID on this session")
	}
	peerLID, err := resolvePeerLID(ctx, cli, target)
	if err != nil {
		return err
	}
	log.Printf("resolved peer LID: %s (self %s)", peerLID, self)

	devices, err := cli.GetUserDevices(ctx, []types.JID{peerLID})
	if err != nil {
		return fmt.Errorf("device discovery: %w", err)
	}
	log.Printf("peer has %d device(s): %v", len(devices), devices)

	var callKey [32]byte
	if _, err := rand.Read(callKey[:]); err != nil {
		return err
	}
	deviceKeys := make([]signaling.OfferDeviceKey, 0, len(devices))
	for _, dev := range devices {
		ct, encType, err := encryptCallKeyForDevice(ctx, cli, dev, callKey[:])
		if err != nil {
			return fmt.Errorf("encrypt callKey for %s: %w", dev, err)
		}
		deviceKeys = append(deviceKeys, signaling.OfferDeviceKey{DeviceJid: dev, Ciphertext: ct, EncType: encType})
	}

	// Include the peer's privacy token when we have one (the server requires it to
	// place a call to contacts with privacy enabled; it arrives via receipts/notifs).
	var privacyToken []byte
	if pt, err := cli.Store.PrivacyTokens.GetPrivacyToken(ctx, peerLID); err == nil && pt != nil {
		privacyToken = pt.Token
		log.Printf("attaching privacy token (%d bytes) for %s", len(privacyToken), peerLID)
	} else {
		log.Printf("no privacy token for %s — the offer may be rejected if the peer requires one", peerLID)
	}

	callID := newCallID()
	offer := signaling.BuildOffer(&signaling.OfferParams{
		CallID:       callID,
		To:           peerLID,
		CallCreator:  self,
		DeviceKeys:   deviceKeys,
		PrivacyToken: privacyToken,
		Capability:   signaling.CapabilityOffer,
	})
	if err := cli.DangerousInternals().SendNode(ctx, offer); err != nil {
		return fmt.Errorf("send offer: %w", err)
	}
	log.Printf("📞 offer sent (call-id %s). Live media after accept is the loopback-proven MediaPipeline path over the relay.", callID)
	<-ctx.Done()
	return nil
}

// runListen connects and prints incoming call signaling. With autoAccept, it
// decrypts the offer's callKey and replies preaccept + accept.
func runListen(ctx context.Context, autoAccept bool) error {
	cli, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Disconnect()

	cli.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.CallOffer:
			log.Printf("📞 incoming call %s from %s (auto-accept=%v)", e.CallID, e.From, autoAccept)
			if autoAccept {
				if err := acceptCall(ctx, cli, e); err != nil {
					log.Printf("auto-accept failed: %v", err)
				}
			}
		case *events.CallTerminate:
			log.Printf("call %s terminated: %s", e.CallID, e.Reason)
		}
	})
	log.Printf("listening for calls (auto-accept=%v). Ctrl+C to stop.", autoAccept)
	<-ctx.Done()
	return nil
}

// acceptCall decrypts the inbound callKey and answers with preaccept + accept.
func acceptCall(ctx context.Context, cli *whatsmeow.Client, e *events.CallOffer) error {
	callKey, err := decryptInboundCallKey(ctx, cli, e)
	if err != nil {
		return fmt.Errorf("decrypt callKey: %w", err)
	}
	log.Printf("🔑 decrypted callKey (%d bytes) for call %s", len(callKey), e.CallID)

	rates := []string{"8000", "16000"}
	pre := signaling.BuildPreaccept(e.CallID, e.From, e.CallCreator, newCallID(), rates)
	if err := cli.DangerousInternals().SendNode(ctx, pre); err != nil {
		return fmt.Errorf("send preaccept: %w", err)
	}
	accept := signaling.BuildAccept(&signaling.AcceptParams{
		CallID:      e.CallID,
		To:          e.From,
		CallCreator: e.CallCreator,
		AudioRates:  rates,
		Capability:  signaling.CapabilityOffer,
	})
	if err := cli.DangerousInternals().SendNode(ctx, accept); err != nil {
		return fmt.Errorf("send accept: %w", err)
	}
	log.Printf("✅ accepted call %s with callKey. NOTE: the caller will send <terminate reason=\"setup_failed\"> ~seconds later "+
		"unless we bring up relay media — connect the relay endpoint from the offer, STUN-allocate, and run the "+
		"loopback-proven MediaPipeline. That live hop (relay.ConnectRelayMedia, NOT VALIDATED) is the remaining work.", e.CallID)
	return nil
}

// decryptInboundCallKey pulls the <enc> from the offer node and decrypts the
// Message{Call{CallKey}} under our Signal session.
func decryptInboundCallKey(ctx context.Context, cli *whatsmeow.Client, e *events.CallOffer) ([]byte, error) {
	if e.Data == nil {
		return nil, errors.New("offer has no data node")
	}
	var enc *waBinary.Node
	for i := range e.Data.GetChildren() {
		if c := &e.Data.GetChildren()[i]; c.Tag == "enc" {
			enc = c
			break
		}
	}
	if enc == nil {
		return nil, errors.New("offer has no enc node")
	}
	isPreKey := enc.AttrGetter().String("type") == "pkmsg"
	pt, _, err := cli.DangerousInternals().DecryptDM(ctx, enc, e.From, isPreKey, e.Timestamp)
	if err != nil {
		return nil, err
	}
	var msg waE2E.Message
	if err := proto.Unmarshal(pt, &msg); err != nil {
		return nil, err
	}
	key := msg.GetCall().GetCallKey()
	if len(key) == 0 {
		return nil, errors.New("offer message carried no callKey")
	}
	return key, nil
}

// newCallID returns a random 16-hex-char call/wrapper id.
func newCallID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
