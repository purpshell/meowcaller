package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite"
)

// meowcallerDBPath is meowcaller's own call store, deliberately a different file
// from whatsmeow's wa-voip.db auth store.
const meowcallerDBPath = "meowcaller.db"

// connectClient opens whatsmeow's auth store (its own file, separate from the
// meowcaller call store) and logs in (QR on first run), returning a connected
// client. busy_timeout absorbs brief lock contention so a busy session doesn't
// error out with "database is locked".
func connectClient(ctx context.Context) (*whatsmeow.Client, error) {
	// Present as a Google Chrome web client. The connection already advertises the
	// WEB platform; these companion props make the linked-device entry read
	// "Google Chrome (Mac OS)" instead of the default. DeviceProps is read at
	// pairing time, so re-pair for an already-linked device to pick this up.
	store.DeviceProps.Os = proto.String("Mac OS")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()

	container, err := sqlstore.New(ctx, "sqlite", "file:wa-voip.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", waLog.Stdout("db", "WARN", true))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	client := whatsmeow.NewClient(device, waLog.Stdout("wa", "DEBUG", true))
	// whatsmeow drops <ack> nodes; the outbound call's relay allocation rides in
	// <ack class="call">. Install the interceptor before Connect.
	installCallAckHook(client)

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
	// After QR pairing the server sends a 515 and whatsmeow disconnects to reconnect
	// with the new creds. WaitForConnection bails on that *expected* disconnect, so we
	// instead wait for the Connected event (dispatched only after authentication) and
	// the connected+logged-in state to settle across the reconnect.
	if err := waitUntilReady(ctx, client, 60*time.Second); err != nil {
		return nil, err
	}
	log.Printf("connected as %s", client.Store.GetLID())

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

// resolvePeerLID turns a CLI target (phone number, phone JID, or @lid JID) into the
// peer's LID — the address the call's E2E keys and SSRCs derive from. A LID is used
// directly; a phone JID is mapped via the LID store, seeded by a usync query if not
// cached.
//
// parseCallTarget turns a CLI target into a JID. A string with '@' is a real JID (a
// LID to call directly, or a phone JID to resolve); a bare string is a phone number.
// ParseJID does NOT error on a missing '@' (it puts the whole string in the server
// field), so we branch on '@' ourselves rather than trusting a parse error.
func parseCallTarget(target string) (types.JID, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return types.EmptyJID, errors.New("empty call target")
	}
	if strings.ContainsRune(target, '@') {
		jid, err := types.ParseJID(target)
		if err != nil {
			return types.EmptyJID, fmt.Errorf("parse target JID %q: %w", target, err)
		}
		return jid, nil
	}
	return types.NewJID(strings.TrimPrefix(target, "+"), types.DefaultUserServer), nil
}

func resolvePeerLID(ctx context.Context, cli *whatsmeow.Client, target string) (types.JID, error) {
	jid, err := parseCallTarget(target)
	if err != nil {
		return types.EmptyJID, err
	}
	if jid.Server == types.HiddenUserServer {
		return jid, nil // already a LID — call it directly
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
// fetching a pre-key bundle if no session exists yet. Returns the ciphertext, the
// enc type ("pkmsg" for a fresh session, "msg" for an existing one), and whether
// the offer must carry our <device-identity> (true for pkmsg, so the peer can
// verify the new session — without it the server drops the offer unacked).
func encryptCallKeyForDevice(ctx context.Context, cli *whatsmeow.Client, dev types.JID, callKey []byte) ([]byte, string, bool, error) {
	pt, err := callKeyPlaintext(callKey)
	if err != nil {
		return nil, "", false, err
	}
	di := cli.DangerousInternals()
	enc, needIdentity, err := di.EncryptMessageForDevice(ctx, pt, dev, nil, nil, nil)
	if err != nil {
		bundles := di.FetchPreKeysNoError(ctx, []types.JID{dev})
		enc, needIdentity, err = di.EncryptMessageForDevice(ctx, pt, dev, bundles[dev], nil, nil)
		if err != nil {
			return nil, "", false, err
		}
	}
	ct, ok := enc.Content.([]byte)
	if !ok {
		return nil, "", false, errors.New("enc node has no ciphertext")
	}
	return ct, enc.AttrGetter().String("type"), needIdentity, nil
}

// runCall connects, resolves the peer LID, discovers devices, encrypts a fresh
// callKey per device, and sends the <call><offer>.
func runCall(ctx context.Context, target string) error {
	cli, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Disconnect()

	store, err := openMeowStore(ctx, meowcallerDBPath)
	if err != nil {
		return err
	}
	defer store.Close()

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
	needIdentity := false
	for _, dev := range devices {
		ct, encType, ni, err := encryptCallKeyForDevice(ctx, cli, dev, callKey[:])
		if err != nil {
			return fmt.Errorf("encrypt callKey for %s: %w", dev, err)
		}
		needIdentity = needIdentity || ni
		deviceKeys = append(deviceKeys, signaling.OfferDeviceKey{DeviceJid: dev, Ciphertext: ct, EncType: encType})
	}

	// pkmsg offers must carry our signed device identity so the peer can verify the
	// new session; the server drops the offer (no ack) otherwise.
	var deviceIdentity []byte
	if needIdentity {
		deviceIdentity, err = proto.Marshal(cli.Store.Account)
		if err != nil {
			return fmt.Errorf("marshal device identity: %w", err)
		}
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
		CallID:         callID,
		To:             peerLID,
		CallCreator:    self,
		DeviceKeys:     deviceKeys,
		PrivacyToken:   privacyToken,
		Capability:     signaling.CapabilityOffer,
		DeviceIdentity: deviceIdentity,
	})
	// The builder leaves the <call> stanza id to the I/O layer. Without a stanza id
	// the server can't route/ack the offer, so it never reaches the callee.
	offer.Attrs["id"] = cli.GenerateMessageID()
	// Pre-seed the media coordinator with our generated callKey, then bring up media
	// when the relay endpoint arrives (relaylatency/transport) after the peer accepts.
	coord := newCoordinator(ctx, cli, store)
	setCallAckHandler(coord.onCallAck)
	m := coord.entry(callID)
	m.callKey = callKey[:]
	m.selfLID = self.String()
	m.peerLID = peerLID.String()
	m.direction = "outgoing"
	coord.persist(callID, "calling", m)
	cli.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.CallRelayLatency:
			coord.onRelay(e.CallID, e.Data)
		case *events.CallTransport:
			coord.onRelay(e.CallID, e.Data)
		case *events.CallTerminate:
			log.Printf("call %s terminated: %s", e.CallID, e.Reason)
			coord.onTerminate(e.CallID)
		}
	})

	if err := cli.DangerousInternals().SendNode(ctx, offer); err != nil {
		return fmt.Errorf("send offer: %w", err)
	}
	log.Printf("📞 offer sent (call-id %s); media starts when the relay endpoint arrives. Ctrl+C to stop.", callID)
	<-ctx.Done()
	return nil
}

// callMedia tracks the per-call inputs needed to start media: the decrypted
// callKey (from the offer) and the relay data (from the offer or a later
// relaylatency/transport stanza). Media starts once both are present.
type callMedia struct {
	callKey   []byte
	relay     *relayData
	selfLID   string
	peerLID   string
	direction string
	started   bool
}

// coordinator answers inbound offers and brings up the media loop once the relay
// endpoint arrives.
type coordinator struct {
	ctx   context.Context
	cli   *whatsmeow.Client
	store *meowStore
	mu    sync.Mutex
	cmap  map[string]*callMedia
}

func newCoordinator(ctx context.Context, cli *whatsmeow.Client, store *meowStore) *coordinator {
	return &coordinator{ctx: ctx, cli: cli, store: store, cmap: map[string]*callMedia{}}
}

// persist writes a call's current meowcaller-side state to the meowcaller store.
// It never aborts the call: a store error is logged and the call continues.
func (c *coordinator) persist(callID, phase string, m *callMedia) {
	if c.store == nil {
		return
	}
	rec := callRecord{
		CallID:    callID,
		Direction: m.direction,
		SelfLID:   m.selfLID,
		PeerLID:   m.peerLID,
		CallKey:   m.callKey,
		Phase:     phase,
	}
	if m.relay != nil {
		if ep := getMediaRelayEndpoint(m.relay); ep != nil && len(ep.addresses) > 0 {
			rec.RelayIP = ep.addresses[0].ipv4
			rec.RelayPort = ep.addresses[0].port
		}
	}
	if err := c.store.SaveCall(c.ctx, rec); err != nil {
		log.Printf("meowcaller-db: save call %s: %v", callID, err)
	}
}

func (c *coordinator) entry(callID string) *callMedia {
	if c.cmap[callID] == nil {
		c.cmap[callID] = &callMedia{}
	}
	return c.cmap[callID]
}

// onOffer decrypts the callKey, answers preaccept + accept, and records relay data
// if it rode along in the offer.
func (c *coordinator) onOffer(e *events.CallOffer) {
	callKey, err := decryptInboundCallKey(c.ctx, c.cli, e)
	if err != nil {
		log.Printf("decrypt callKey for %s: %v", e.CallID, err)
		return
	}
	log.Printf("🔑 decrypted callKey (%d bytes) for %s", len(callKey), e.CallID)

	rates := []string{"8000", "16000"}
	pre := signaling.BuildPreaccept(e.CallID, e.From, e.CallCreator, newCallID(), rates)
	if err := c.cli.DangerousInternals().SendNode(c.ctx, pre); err != nil {
		log.Printf("send preaccept: %v", err)
		return
	}
	accept := signaling.BuildAccept(&signaling.AcceptParams{
		CallID: e.CallID, To: e.From, CallCreator: e.CallCreator,
		AudioRates: rates, Capability: signaling.CapabilityOffer,
	})
	// Like the offer, the <call> stanza id is supplied by the I/O layer.
	accept.Attrs["id"] = c.cli.GenerateMessageID()
	if err := c.cli.DangerousInternals().SendNode(c.ctx, accept); err != nil {
		log.Printf("send accept: %v", err)
		return
	}
	log.Printf("✅ accepted %s — bringing up media when the relay endpoint arrives", e.CallID)

	peer := e.CallCreator
	if peer.IsEmpty() {
		peer = e.From
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.entry(e.CallID)
	m.callKey = callKey
	m.selfLID = c.cli.Store.GetLID().String()
	m.peerLID = peer.String()
	m.direction = "incoming"
	if r := findRelay(e.Data); r != nil {
		m.relay = parseRelayData(r)
	}
	c.persist(e.CallID, "accepted", m)
	c.maybeStart(e.CallID, m)
}

// onRelay records relay data from a relaylatency/transport stanza.
func (c *coordinator) onRelay(callID string, data *waBinary.Node) {
	r := findRelay(data)
	if r == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.entry(callID)
	m.relay = parseRelayData(r)
	c.persist(callID, "relay", m)
	c.maybeStart(callID, m)
}

// onCallAck handles an <ack class="call"> node. For an outbound offer the relay
// allocation arrives here (whatsmeow otherwise drops the ack), which is what lets
// the caller bring up media.
func (c *coordinator) onCallAck(ack *waBinary.Node) {
	r := findRelay(ack)
	if r == nil {
		return
	}
	callID := r.AttrGetter().String("call-id")
	if callID == "" {
		return
	}
	log.Printf("📥 relay allocation arrived in call ack for %s", callID)
	c.onRelay(callID, ack)
}

// onTerminate records a call's end in the meowcaller store.
func (c *coordinator) onTerminate(callID string) {
	if c.store == nil {
		return
	}
	if err := c.store.SetPhase(c.ctx, callID, "terminated"); err != nil {
		log.Printf("meowcaller-db: terminate %s: %v", callID, err)
	}
}

// maybeStart launches the media loop once the callKey and relay endpoint are known.
func (c *coordinator) maybeStart(callID string, m *callMedia) {
	if m.started || m.callKey == nil || m.relay == nil {
		return
	}
	m.started = true
	c.persist(callID, "media", m)
	log.Printf("▶ starting media for %s", callID)
	go func() {
		if err := runMedia(c.ctx, callID, m.callKey, m.selfLID, m.peerLID, m.relay); err != nil {
			log.Printf("media for %s ended: %v", callID, err)
		}
	}()
}

// runListen connects and, with autoAccept, answers incoming calls and pipes media.
func runListen(ctx context.Context, autoAccept bool) error {
	cli, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Disconnect()

	store, err := openMeowStore(ctx, meowcallerDBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if n, err := store.CountCalls(ctx); err == nil {
		log.Printf("meowcaller store: %s (%d call(s) recorded), separate from whatsmeow's wa-voip.db", meowcallerDBPath, n)
	}
	coord := newCoordinator(ctx, cli, store)
	setCallAckHandler(coord.onCallAck)

	cli.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.CallOffer:
			log.Printf("📞 incoming call %s from %s (auto-accept=%v)", e.CallID, e.From, autoAccept)
			if autoAccept {
				coord.onOffer(e)
			}
		case *events.CallRelayLatency:
			if autoAccept {
				coord.onRelay(e.CallID, e.Data)
			}
		case *events.CallTransport:
			if autoAccept {
				coord.onRelay(e.CallID, e.Data)
			}
		case *events.CallTerminate:
			log.Printf("call %s terminated: %s", e.CallID, e.Reason)
			coord.onTerminate(e.CallID)
		}
	})
	log.Printf("listening for calls (auto-accept=%v). Ctrl+C to stop.", autoAccept)
	<-ctx.Done()
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

// newCallID returns a call/wrapper id in WhatsApp's shape: 16 random bytes as
// uppercase hex (32 chars).
func newCallID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return strings.ToUpper(hex.EncodeToString(b[:]))
}
