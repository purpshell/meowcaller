package meowcaller

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/polymorfa/hypermeow"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
)

// engine is the internal media + signaling engine behind Client/Call. It owns the
// whatsmeow event wiring (offer / preaccept / accept / relaylatency / mute_v2 / ack /
// terminate), the low-level <ack>/<call> node interception, the relay election and the
// per-frame media loop (encode a Player's frames out, decode the peer's frames into a
// sink). This is where the orchestration formerly hand-rolled in examples/cli has been
// lifted to; Client and Call are the public face over it.
type engine struct {
	c *Client

	mu              sync.Mutex
	calls           map[string]*engineCall // keyed by call-id
	sendCallNode    func(context.Context, waBinary.Node) error
	requestCallNode func(context.Context, waBinary.Node, string) (*waBinary.Node, error)
	rawCallHookErr  error
}

// engineCall is the engine's per-call state: the public Call handle plus the inputs
// needed to bring media up (the decrypted callKey and the relay endpoint, both of
// which can arrive separately), the media goroutine cancel handle, and the deferred
// accept bookkeeping.
type engineCall struct {
	call    *Call
	callKey []byte
	relay   *relayData
	selfLID string
	peerLID string

	creator types.JID // call-creator JID (for accept/relaylatency)
	from    types.JID // the <call> "from" — where stanzas are addressed

	direction         CallDirection
	codec             AudioCodec   // audio codec for this call, selected from voip_settings (MLow default)
	localVideo        bool         // this client is sending, or has requested to send, video
	remoteVideo       bool         // the peer is sending video to this client
	videoGate         bool         // outbound upgrade is waiting for peer acceptance
	peerVideoUpgrade  bool         // the peer's inbound upgrade is waiting for local acceptance
	videoTx           *videoSender // video send pipeline, live while media runs
	appDataTx         *appDataSender
	rekeyPeer         func(string) error
	group             bool
	groupUpdate       *groupCallUpdate
	groupReceivers    *participantReceiveRegistry
	groupRawEpoch     []byte
	groupEpochTxID    uint32
	hasGroupEpoch     bool
	started           bool
	cancel            context.CancelFunc // tears down this call's media goroutine
	waitingRoomCancel context.CancelFunc
	inviteSelfDevice  groupCallDevice
	invitePeerDevice  groupCallDevice

	// The callee <accept> is deferred until the caller's <mute_v2> arrives.
	acceptPending bool
	// acceptSent flips once the <accept> is actually on the wire. Video-state
	// stanzas sent before it are a sequence no real client produces, and phones
	// react badly (observed: the caller's microphone goes silent while their
	// camera keeps streaming). pendingVideoEnable and pendingVideoOrientation
	// hold toggles that arrived early; they are applied right after the accept
	// goes out.
	acceptSent              bool
	pendingVideoEnable      *bool
	pendingVideoOrientation *int
}

// newEngine creates the engine for a Client.
func newEngine(c *Client) *engine {
	e := &engine{c: c, calls: map[string]*engineCall{}}
	if c != nil && c.wa != nil {
		e.sendCallNode = func(ctx context.Context, node waBinary.Node) error {
			return c.wa.DangerousInternals().SendNode(ctx, node)
		}
		e.requestCallNode = func(ctx context.Context, node waBinary.Node, requestID string) (*waBinary.Node, error) {
			di := c.wa.DangerousInternals()
			waiter := di.WaitResponse(requestID)
			defer di.CancelResponse(requestID, waiter)
			if err := di.SendNode(ctx, node); err != nil {
				return nil, err
			}
			select {
			case response := <-waiter:
				if response == nil {
					return nil, errors.New("meowcaller: call request returned no response")
				}
				return response, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return e
}

func (e *engine) requireRawCallAdapter() error {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/e9a033b24933cc8d90fa5ff8991b1268ba80a140/client.go#L110-L120
	if e == nil {
		return errors.New("meowcaller: raw call adapter is unavailable")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rawCallHookErr != nil {
		return fmt.Errorf("meowcaller: raw call adapter is unavailable: %w", e.rawCallHookErr)
	}
	return nil
}

// onEndFn returns the Call's OnEnd listener under its lock (the field is unexported
// and guarded by Call.mu; same-package engine code reads it through here).
func (c *Call) onEndFn() func(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onEnd
}

// onReadyFn returns the Call's OnReady listener under its lock.
func (c *Call) onReadyFn() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onReady
}

// playerAndSink returns the Call's current Player and sink under its lock (the engine's
// media loop reads them every frame so a later Subscribe/Receive takes effect live).
func (c *Call) playerAndSink() (*Player, AudioSink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.player, c.sink
}

// install wires the whatsmeow call event handlers and the <ack>/<call> interception.
// Call before the whatsmeow client connects.
func (e *engine) install() {
	if err := e.installCallAckHook(); err != nil {
		e.mu.Lock()
		e.rawCallHookErr = err
		e.mu.Unlock()
		e.c.log.Error().Err(err).Msg("raw call adapter is unavailable")
	}
	e.c.wa.AddEventHandler(func(evt any) {
		switch ev := evt.(type) {
		case *events.CallOffer:
			e.onOffer(ev)
		case *events.CallPreAccept:
			e.onPreAccept(ev)
		case *events.CallAccept:
			e.onAccept(ev)
		case *events.CallRelayLatency:
			e.onRelay(ev.CallID, ev.Data)
			e.onRelayLatency(ev)
		case *events.CallTransport:
			e.onRelay(ev.CallID, ev.Data)
		case *events.CallTerminate:
			e.onTerminate(ev.CallID, ev.Reason)
		case *events.CallReject:
			e.onReject(ev)
		case *events.UnknownCallEvent:
			e.onUnknownCallEvent(ev.Node)
		}
	})
}

func (e *engine) sendReaction(callID, emoji string) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	sender := m.appDataTx
	e.mu.Unlock()
	if sender == nil {
		return errAppDataUnavailable
	}
	return sender.sendReaction(emoji)
}

// entry returns (creating if needed) the per-call state for callID.
func (e *engine) entry(callID string) *engineCall {
	if e.calls[callID] == nil {
		e.calls[callID] = &engineCall{}
	}
	return e.calls[callID]
}

// lookup returns the per-call state for callID, or nil.
func (e *engine) lookup(callID string) *engineCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[callID]
}

func (e *engine) callIsVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && (m.localVideo || m.remoteVideo)
}

func (e *engine) callIsSendingVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[callID] != nil && e.calls[callID].localVideo
}

func (e *engine) callIsReceivingVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[callID] != nil && e.calls[callID].remoteVideo
}

func (e *engine) transmitCallNode(ctx context.Context, node waBinary.Node) error {
	if e.sendCallNode == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	return e.sendCallNode(ctx, node)
}

func (e *engine) nextCallNodeID() string {
	if e.c != nil && e.c.wa != nil {
		return e.c.wa.GenerateMessageID()
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

// sendVideoFrame packetizes one encoded H.264 access unit and sends it to the relay, if a
// video send pipeline is live for the call.
func (e *engine) sendVideoFrame(callID string, au []byte, duration time.Duration) error {
	e.mu.Lock()
	var vs *videoSender
	if m := e.calls[callID]; m != nil {
		vs = m.videoTx
	}
	e.mu.Unlock()
	if vs == nil {
		return errors.New("meowcaller: call has no active video media")
	}
	vs.send(au, duration)
	return nil
}

func (e *engine) transitionVideo(callID string, transition int) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	to, creator, sender := m.from, m.creator, m.videoTx
	localVideoActive := m.localVideo
	switch transition {
	case signaling.VideoStateUpgradeRequestV2:
		m.localVideo = true
		m.videoGate = true
	case signaling.VideoStateUpgradeAccept:
		if !m.peerVideoUpgrade {
			e.mu.Unlock()
			return errors.New("meowcaller: no pending peer video upgrade")
		}
		m.peerVideoUpgrade = false
	case signaling.VideoStateStopped:
		m.localVideo = false
		m.videoGate = false
	default:
		e.mu.Unlock()
		return fmt.Errorf("meowcaller: unsupported local video transition %d", transition)
	}
	e.mu.Unlock()
	if sender != nil {
		if transition == signaling.VideoStateStopped {
			sender.disable()
		} else if transition == signaling.VideoStateUpgradeRequestV2 {
			sender.enable(true)
		}
	}

	build := func(state int, dec string, orientation *int) waBinary.Node {
		return signaling.BuildVideoStateWithParams(signaling.VideoStateParams{
			CallID: callID, To: to, CallCreator: creator, WrapperID: e.nextCallNodeID(),
			State: state, Dec: dec, DeviceOrientation: orientation,
		})
	}
	send := func(state int, dec string, orientation *int) error {
		return e.transmitCallNode(context.Background(), build(state, dec, orientation))
	}

	var err error
	switch transition {
	case signaling.VideoStateUpgradeRequestV2:
		orientation := 0
		err = send(transition, signaling.VideoDecRequest, &orientation)
	case signaling.VideoStateUpgradeAccept:
		if !localVideoActive {
			orientation := 0
			err = send(signaling.VideoStateStopped, "", &orientation)
		}
		if err == nil {
			err = send(transition, signaling.VideoDecAccept, nil)
		}
	case signaling.VideoStateStopped:
		orientation := 0
		err = send(transition, "", &orientation)
	}
	if err == nil || transition == signaling.VideoStateStopped {
		return err
	}

	e.mu.Lock()
	var currentSender *videoSender
	if current := e.calls[callID]; current == m {
		if transition == signaling.VideoStateUpgradeAccept {
			current.peerVideoUpgrade = true
		} else {
			current.localVideo = false
			current.videoGate = false
			currentSender = current.videoTx
		}
	}
	e.mu.Unlock()
	if currentSender != nil {
		currentSender.disable()
	}
	return err
}

func (e *engine) setVideoEnabled(callID string, enabled bool) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	// On an incoming 1:1 call the <accept> is deferred until the caller's
	// mute_v2; a video-state stanza racing ahead of it is out-of-sequence for
	// the peer. Park the request and let sendAccept apply it.
	if !m.group && m.direction == CallDirectionIncoming && !m.acceptSent {
		v := enabled
		m.pendingVideoEnable = &v
		e.mu.Unlock()
		e.c.log.Info().Str("call_id", callID).Bool("enabled", enabled).Msg("video enable deferred until accept is sent")
		return nil
	}
	m.localVideo = enabled
	m.videoGate = false
	to, creator, sender := m.from, m.creator, m.videoTx
	e.mu.Unlock()

	if sender != nil {
		if enabled {
			sender.enable(false)
		} else {
			sender.disable()
		}
	}
	state, dec := signaling.VideoStateDisabled, ""
	if enabled {
		state, dec = signaling.VideoStateEnabled, signaling.VideoStateDecH264
	}
	node := signaling.BuildVideoStateWithParams(signaling.VideoStateParams{
		CallID: callID, To: to, CallCreator: creator, WrapperID: e.nextCallNodeID(),
		State: state, Dec: dec,
	})
	err := e.transmitCallNode(context.Background(), node)
	if err == nil || !enabled {
		return err
	}

	e.mu.Lock()
	if current := e.calls[callID]; current == m {
		current.localVideo = false
	}
	e.mu.Unlock()
	if sender != nil {
		sender.disable()
	}
	return err
}

func (e *engine) setVideoOrientation(callID string, orientation int) error {
	if orientation < 0 || orientation > 3 {
		return fmt.Errorf("meowcaller: video orientation %d is outside 0..3", orientation)
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call has no active video media")
	}
	// Same deferral as the camera toggle: while the accept (and any parked
	// enable) hasn't gone out, hold the orientation and let sendAccept apply
	// it in order.
	if !m.group && m.direction == CallDirectionIncoming && !m.acceptSent {
		v := orientation
		m.pendingVideoOrientation = &v
		e.mu.Unlock()
		e.c.log.Info().Str("call_id", callID).Int("orientation", orientation).Msg("video orientation deferred until accept is sent")
		return nil
	}
	if !m.localVideo {
		e.mu.Unlock()
		return errors.New("meowcaller: call has no active video media")
	}
	to, creator, sender := m.from, m.creator, m.videoTx
	e.mu.Unlock()
	// The stanza announces the rotation, but receivers render by the in-band
	// CVO bits on each packet — both must carry the same value.
	if sender != nil {
		sender.setOrientation(orientation)
	}
	node := signaling.BuildVideoStateWithParams(signaling.VideoStateParams{
		CallID: callID, To: to, CallCreator: creator, WrapperID: e.nextCallNodeID(),
		State: signaling.VideoStateEnabled, DeviceOrientation: &orientation,
	})
	return e.transmitCallNode(context.Background(), node)
}

// placeCall resolves target to a LID, builds and sends the <offer>, registers the Call,
// and returns it; media starts when the peer answers and the relay endpoint arrives.
func (e *engine) placeCall(ctx context.Context, target string, opts CallOptions) (*Call, error) {
	cli := e.c.wa
	self := cli.Store.GetLID()
	if self.IsEmpty() {
		return nil, errors.New("meowcaller: no own LID on this session")
	}
	peerLID, err := resolvePeerLID(ctx, cli, target)
	if err != nil {
		return nil, err
	}
	e.c.log.Info().Str("peer_lid", peerLID.String()).Str("self_lid", self.String()).Msg("resolved peer LID")

	devices, err := cli.GetUserDevices(ctx, []types.JID{peerLID})
	if err != nil {
		return nil, fmt.Errorf("device discovery: %w", err)
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("peer %s has no devices (unreachable / not on WhatsApp)", peerLID)
	}

	var callKey [32]byte
	if _, err := rand.Read(callKey[:]); err != nil {
		return nil, err
	}
	deviceKeys := make([]signaling.OfferDeviceKey, 0, len(devices))
	needIdentity := false
	for _, dev := range devices {
		ct, encType, ni, err := encryptCallKeyForDevice(ctx, cli, dev, callKey[:])
		if err != nil {
			return nil, fmt.Errorf("encrypt callKey for %s: %w", dev, err)
		}
		needIdentity = needIdentity || ni
		deviceKeys = append(deviceKeys, signaling.OfferDeviceKey{DeviceJid: dev, Ciphertext: ct, EncType: encType})
	}

	// pkmsg offers must carry our signed device identity so the peer can verify the new
	// session; the server drops the offer (no ack) otherwise.
	var deviceIdentity []byte
	if needIdentity {
		deviceIdentity, err = proto.Marshal(cli.Store.Account)
		if err != nil {
			return nil, fmt.Errorf("marshal device identity: %w", err)
		}
	}

	// Include the peer's privacy token when we have one (the server requires it to
	// place a call to a contact with privacy enabled).
	var privacyToken []byte
	if pt, err := cli.Store.PrivacyTokens.GetPrivacyToken(ctx, peerLID); err == nil && pt != nil {
		privacyToken = pt.Token
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
		Video:          opts.Video,
	})
	// The builder leaves the <call> stanza id to the I/O layer; without it the server
	// can't route/ack the offer, so it never reaches the callee.
	offer.Attrs["id"] = cli.GenerateMessageID()

	call := &Call{eng: e, id: callID, peer: peerLID, phase: CallPhaseCalling}

	e.mu.Lock()
	m := e.entry(callID)
	m.call = call
	m.callKey = callKey[:]
	m.selfLID = self.String()
	m.peerLID = peerLID.String()
	m.creator = self
	m.from = peerLID
	m.direction = CallDirectionOutgoing
	m.localVideo = opts.Video
	m.remoteVideo = opts.Video
	m.inviteSelfDevice = groupCallDevice{
		JID: self, CapabilityVersion: 1,
		Capability: append([]byte(nil), signaling.CapabilityOffer...),
	}
	e.mu.Unlock()

	e.c.diag.Emit("keying", map[string]any{
		"call_id": callID, "direction": "out", "self_lid": self.String(),
		"peer_lid": peerLID.String(), "device_count": len(deviceKeys),
		"call_key_hex": hex.EncodeToString(callKey[:]),
	})

	if err := cli.DangerousInternals().SendNode(ctx, offer); err != nil {
		return nil, fmt.Errorf("send offer: %w", err)
	}
	e.c.log.Info().Str("call_id", callID).Bool("video", opts.Video).Msg("offer sent; media starts when the relay endpoint arrives")
	e.c.diag.Emit("meta", map[string]any{"event": "offer_sent", "call_id": callID, "peer_lid": peerLID.String(), "direction": "out", "video": opts.Video})
	return call, nil
}

// onOffer handles an inbound <offer> event: it decrypts the callKey, captures any relay
// data, registers the Call in the Ringing phase, sends the <preaccept> eagerly (a
// preparation step, independent of the later Answer/Reject), and fires the
// OnIncomingCall listener. Only the <accept> is deferred to Answer.
func (e *engine) onOffer(ev *events.CallOffer) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/33854919e64bdd4b053054ac9764d8fc63027b57/datasheets/voip-group-invite-accept.md#L28-L40
	groupSnapshot, isGroup, groupErr := signaling.ParseGroupInviteSnapshot(ev.Data)
	if groupErr != nil {
		e.c.log.Warn().Err(groupErr).Str("call_id", ev.CallID).Msg("parse inbound group offer failed")
		return
	}
	if isGroup {
		e.onGroupOffer(ev, groupCallUpdateFromSignaling(*groupSnapshot))
		return
	}

	// A "call ended" notification arrives offer-shaped, carrying is_call_ended/
	// terminate_reason (e.g. accepted_elsewhere). It is not a live call — engaging it
	// (preaccept/accept) just earns an "accept error 500". Ignore it.
	oag := ev.Data.AttrGetter()
	if oag.OptionalString("is_call_ended") == "1" || oag.OptionalString("terminate_reason") != "" {
		e.c.log.Warn().Str("call_id", ev.CallID).Msg("ignoring already-ended offer; not a live call")
		return
	}

	// Offline replay: on reconnect the server re-delivers call stanzas queued
	// while we were away, including offers minutes old whose terminate is right
	// behind them in the same backlog. The wrapper's `e` attribute is the
	// server-computed elapsed seconds (0 on live delivery), so it is immune to
	// local clock skew. Anything older than the caller's own 90s ring timeout
	// cannot be answered — ringing it just races the queued terminate.
	if raw := oag.OptionalString("e"); raw != "" {
		if elapsed, err := strconv.Atoi(raw); err == nil && elapsed > 90 {
			e.c.log.Warn().Str("call_id", ev.CallID).Int("elapsed_s", elapsed).Msg("ignoring stale offer replayed from the offline queue")
			return
		}
	}

	callKey, err := decryptInboundCallKey(context.Background(), e.c.wa, ev)
	if err != nil {
		e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("decrypt callKey failed")
		return
	}
	e.c.log.Info().Int("key_bytes", len(callKey)).Str("call_id", ev.CallID).Msg("decrypted inbound callKey")
	e.c.diag.Emit("keying", map[string]any{
		"call_id": ev.CallID, "direction": "in", "from": ev.From.String(),
		"call_key_hex": hex.EncodeToString(callKey),
	})

	peer := ev.CallCreator
	if peer.IsEmpty() {
		peer = ev.From
	}
	e.c.diag.Emit("meta", map[string]any{
		"event": "offer_received", "call_id": ev.CallID,
		"from": ev.From.String(), "peer": peer.String(),
	})
	call := &Call{eng: e, id: ev.CallID, peer: peer, phase: CallPhaseRinging}

	e.mu.Lock()
	m := e.entry(ev.CallID)
	m.call = call
	m.callKey = callKey
	m.selfLID = e.c.wa.Store.GetLID().String()
	m.peerLID = peer.String()
	m.creator = ev.CallCreator
	m.from = ev.From
	m.direction = CallDirectionIncoming
	// A <video> child marks a call that starts with both video directions enabled.
	isVideo := signaling.OfferHasVideo(ev.Data)
	m.localVideo = isVideo
	m.remoteVideo = isVideo
	m.inviteSelfDevice = groupCallDevice{
		JID: e.c.wa.Store.GetLID(), CapabilityVersion: 1,
		Capability: append([]byte(nil), signaling.CapabilityOffer...),
	}
	if device, ok := inviteDeviceCapability(ev.From, ev.Data); ok {
		m.invitePeerDevice = device
	}
	if r := findRelay(ev.Data); r != nil {
		m.relay = parseRelayData(r)
		if !m.relay.peerJID.IsEmpty() {
			m.peerLID = m.relay.peerJID.String()
		}
	}
	e.applyVoipSettingsCodec(m, ev.Data, ev.CallID)
	e.mu.Unlock()
	if isVideo {
		e.c.log.Info().Str("call_id", ev.CallID).Msg("inbound call advertises video")
	}

	// Preaccept eagerly: it is a preparation step, done independently of the later
	// Answer/Reject decision. It keeps the offer alive and joins the relay election while
	// the integrator decides — even a call the user goes on to decline has usually already
	// been preaccepted.
	if err := e.sendPreaccept(ev.CallID, ev.From, ev.CallCreator, isVideo); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("preaccept failed")
	}

	if fn := e.c.incomingCallHandler(); fn != nil {
		fn(call)
	}
}

func (e *engine) onGroupOffer(ev *events.CallOffer, update groupCallUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/33854919e64bdd4b053054ac9764d8fc63027b57/datasheets/voip-group-invite-accept.md#L28-L40
	peer := ev.CallCreator
	if peer.IsEmpty() {
		peer = ev.From
	}
	call := &Call{eng: e, id: ev.CallID, peer: peer, phase: CallPhaseRinging}
	isVideo := signaling.OfferHasVideo(ev.Data)
	e.mu.Lock()
	m := e.entry(ev.CallID)
	m.call = call
	m.selfLID = e.c.wa.Store.GetLID().String()
	m.peerLID = peer.String()
	m.creator = ev.CallCreator
	if m.creator.IsEmpty() {
		m.creator = peer
	}
	m.from = types.NewJID(ev.CallID, "call")
	m.direction = CallDirectionIncoming
	m.group = true
	m.localVideo = isVideo
	m.remoteVideo = isVideo
	creator := m.creator
	e.mu.Unlock()
	e.applyGroupUpdate(update)

	preaccept, err := signaling.BuildActiveGroupPreaccept(
		ev.CallID,
		creator,
		e.nextCallNodeID(),
		isVideo,
	)
	if err != nil {
		e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("build group preaccept failed")
		return
	}
	if err = e.transmitCallNode(context.Background(), preaccept); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("send group preaccept failed")
		return
	}
	e.c.log.Info().Str("call_id", ev.CallID).Bool("video", isVideo).Msg("active group invite preaccepted")
	if fn := e.c.incomingCallHandler(); fn != nil {
		fn(call)
	}
}

// sendPreaccept sends the <preaccept> for an inbound call — a preparation step done
// eagerly when the offer arrives (see onOffer), independent of the later Answer/Reject
// decision. Video calls also advertise the H.264 decoder before the final accept.
func (e *engine) sendPreaccept(callID string, to, creator types.JID, video bool) error {
	pre := signaling.BuildPreaccept(
		callID,
		to,
		creator,
		e.c.wa.DangerousInternals().GenerateRequestID(),
		[]string{"16000"},
		video,
	)
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), pre); err != nil {
		return fmt.Errorf("send preaccept: %w", err)
	}
	e.c.log.Info().Str("call_id", callID).Msg("preaccepted (preparation; awaiting Answer/Reject)")
	return nil
}

// answer accepts an inbound call: it marks the call to accept (the actual <accept> is
// deferred until the caller's <mute_v2>, which onCallRaw fires) and brings media up. The
// <preaccept> was already sent eagerly when the offer arrived, so Answer only commits to
// the call. Media comes up once callKey+relay are both known.
func (e *engine) answer(c *Call) error {
	m := e.lookup(c.id)
	if m == nil {
		return fmt.Errorf("meowcaller: unknown call %s", c.id)
	}
	if m.group {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/676ebee3eca513b5348fab36cae5c560cc791238/datasheets/voip-group-invite-accept.md#L26-L45
		accept, err := signaling.BuildActiveGroupAccept(c.id, m.creator, e.nextCallNodeID())
		if err != nil {
			return err
		}
		if err = e.transmitCallNode(context.Background(), accept); err != nil {
			return fmt.Errorf("meowcaller: send group accept: %w", err)
		}
		c.setPhase(CallPhaseConnecting)
		e.maybeStartMedia(c.id)
		return nil
	}
	e.mu.Lock()
	m.acceptPending = true
	e.mu.Unlock()

	c.setPhase(CallPhaseConnecting)
	e.maybeStartMedia(c.id)
	return nil
}

// sendAccept sends the deferred callee <accept> (once), in the WA-Web format (metadata +
// single rate — the peer keeps the call alive with this; capability+both-rates fails).
func (e *engine) sendAccept(callID string, to, creator types.JID) {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || !m.acceptPending {
		e.mu.Unlock()
		return
	}
	isVideo := m.localVideo || m.remoteVideo
	m.acceptPending = false
	e.mu.Unlock()

	accept := signaling.BuildAccept(&signaling.AcceptParams{
		CallID: callID, To: to, CallCreator: creator,
		AudioRates: []string{"16000"},
		Metadata:   waBinary.Attrs{"peer_abtest_bucket_id_list": "125208,94276"},
		Video:      isVideo,
	})
	accept.Attrs["id"] = e.c.wa.DangerousInternals().GenerateRequestID()
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), accept); err != nil {
		e.c.log.Error().Err(err).Str("call_id", callID).Msg("send accept failed")
		return
	}
	e.c.log.Info().Str("call_id", callID).Bool("video", isVideo).Msg("accepted (after mute_v2)")

	// The accept is on the wire; release any camera toggle that arrived early.
	e.mu.Lock()
	var pending *bool
	var pendingOrientation *int
	if current := e.calls[callID]; current == m {
		current.acceptSent = true
		pending = current.pendingVideoEnable
		current.pendingVideoEnable = nil
		pendingOrientation = current.pendingVideoOrientation
		current.pendingVideoOrientation = nil
	}
	e.mu.Unlock()
	if pending != nil {
		if err := e.setVideoEnabled(callID, *pending); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("deferred video enable failed")
		}
	}
	if pendingOrientation != nil {
		if err := e.setVideoOrientation(callID, *pendingOrientation); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("deferred video orientation failed")
		}
	}
}

// reject declines an inbound call.
func (e *engine) reject(c *Call) error {
	m := e.lookup(c.id)
	to, creator := c.peer, c.peer
	if m != nil {
		to, creator = m.from, m.creator
	}
	rej := signaling.BuildReject(c.id, to, creator)
	rej.Attrs["id"] = e.nextCallNodeID()
	e.finishCall(c.id, "rejected")
	if err := e.transmitCallNode(context.Background(), rej); err != nil {
		return fmt.Errorf("send reject: %w", err)
	}
	return nil
}

// hangup ends a call (either direction) and tears down its media.
func (e *engine) hangup(c *Call) error {
	m := e.lookup(c.id)
	to, creator := c.peer, c.peer
	if m != nil {
		to, creator = m.from, m.creator
	}
	term := signaling.BuildTerminate(&signaling.TerminateParams{CallID: c.id, To: to, CallCreator: creator})
	term.Attrs["id"] = e.nextCallNodeID()
	e.finishCall(c.id, "hangup")
	if err := e.transmitCallNode(context.Background(), term); err != nil {
		return fmt.Errorf("send terminate: %w", err)
	}
	return nil
}

// onRelay records relay data from a relaylatency/transport/ack stanza and starts media
// once both the callKey and the relay endpoint are known.
func (e *engine) onRelay(callID string, data *waBinary.Node) {
	r := findRelay(data)
	if r == nil {
		return
	}
	rd := parseRelayData(r)
	var rekeyPeer func(string) error
	var peerLID string
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil {
		e.mu.Unlock()
		return
	}
	m.relay = rd
	if !rd.peerJID.IsEmpty() {
		peerLID = rd.peerJID.String()
		if peerLID != m.peerLID {
			m.peerLID = peerLID
			rekeyPeer = m.rekeyPeer
		}
	}
	e.mu.Unlock()
	if rekeyPeer != nil {
		if err := rekeyPeer(peerLID); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Str("peer_lid", peerLID).
				Msg("failed to rekey media to relay-elected peer")
		} else {
			e.c.log.Info().Str("call_id", callID).Str("peer_lid", peerLID).
				Msg("rekeyed media to relay-elected peer")
		}
	}
	e.maybeStartMedia(callID)
}

// onRelayLatency answers the caller's relaylatency probes (the callee's half of the
// relay election). It does NOT send the accept — that is deferred until <mute_v2>.
func (e *engine) onRelayLatency(ev *events.CallRelayLatency) {
	m := e.lookup(ev.CallID)
	if m == nil || m.direction != CallDirectionIncoming {
		return
	}
	rl := findChild(ev.Data, "relaylatency")
	if rl == nil {
		return
	}
	// Only endorse relays we can actually receive on. The caller's probes
	// routinely include its own nearest edge, which is absent from the offer
	// and holds no tokens for us; echoing that entry back tells the caller both
	// sides reach it, the election picks it, and the caller's media moves to a
	// relay we were never connected to.
	e.mu.Lock()
	offered := map[string]bool{}
	if m.relay != nil {
		for i := range m.relay.endpoints {
			if name := m.relay.endpoints[i].relayName; name != "" {
				offered[name] = true
			}
		}
	}
	e.mu.Unlock()

	var probes []rlProbe
	for i := range rl.GetChildren() {
		te := &rl.GetChildren()[i]
		if te.Tag != "te" {
			continue
		}
		ag := te.AttrGetter()
		name := ag.String("relay_name")
		if len(offered) > 0 && !offered[name] {
			e.c.log.Debug().Str("call_id", ev.CallID).Str("relay_name", name).Msg("skipping latency response for relay outside the offer")
			continue
		}
		probes = append(probes, rlProbe{
			latency:   decodeLatency(ag.String("latency")),
			relayName: name,
			addr:      nodeBytes(te),
		})
	}
	for _, p := range probes {
		resp := signaling.BuildRelayLatency(&signaling.RelayLatencyParams{
			CallID:       ev.CallID,
			To:           ev.From,
			CallCreator:  ev.CallCreator,
			LatencyMs:    p.latency,
			RelayName:    p.relayName,
			AddressBytes: p.addr,
		})
		resp.Attrs["id"] = e.c.wa.GenerateMessageID()
		if err := e.c.wa.DangerousInternals().SendNode(context.Background(), resp); err != nil {
			e.c.log.Error().Err(err).Str("call_id", ev.CallID).Msg("send relaylatency failed")
			return
		}
	}
}

// onPreAccept records that the peer's device received and started preparing an outgoing call.
func (e *engine) onPreAccept(ev *events.CallPreAccept) {
	m := e.lookup(ev.CallID)
	if m == nil || m.direction != CallDirectionOutgoing {
		return
	}
	if m.call != nil && m.call.State() == CallPhaseCalling {
		m.call.setPhase(CallPhaseRinging)
	}
	if device, ok := inviteDeviceCapability(ev.From, ev.Data); ok {
		e.mu.Lock()
		if current := e.calls[ev.CallID]; current != nil {
			current.invitePeerDevice = device
		}
		e.mu.Unlock()
	}
	e.c.log.Info().
		Str("call_id", ev.CallID).
		Str("from", ev.From.String()).
		Str("platform", ev.RemotePlatform).
		Msg("peer preaccepted outgoing call")
	e.c.diag.Emit("meta", map[string]any{
		"event": "peer_preaccept", "call_id": ev.CallID,
		"from": ev.From.String(), "platform": ev.RemotePlatform,
	})
}

// onAccept records that the peer answered an outgoing call. Media may already be running
// from relay allocation, but inbound RTP is still what marks the call ready/active.
func (e *engine) onAccept(ev *events.CallAccept) {
	m := e.lookup(ev.CallID)
	if m == nil || m.direction != CallDirectionOutgoing {
		return
	}
	if m.call != nil && m.call.State() == CallPhaseEnded {
		return
	}
	if m.group {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/676ebee3eca513b5348fab36cae5c560cc791238/datasheets/voip-group-invite-accept.md#L26-L45
		if m.call != nil && m.call.State() < CallPhaseConnecting {
			m.call.setPhase(CallPhaseConnecting)
		}
		if m.call != nil {
			m.call.markPeerAccepted()
		}
		e.c.log.Info().Str("call_id", ev.CallID).Str("from", ev.From.String()).Msg("participant accepted group call")
		e.maybeStartMedia(ev.CallID)
		return
	}
	e.mu.Lock()
	var rekeyPeer func(string) error
	answeringPeer := ev.From.String()
	if current := e.calls[ev.CallID]; current != nil {
		if device, ok := inviteDeviceCapability(ev.From, ev.Data); ok {
			current.invitePeerDevice = device
		}
		e.applyVoipSettingsCodec(current, ev.Data, ev.CallID)
		if !ev.From.IsEmpty() {
			current.from = ev.From
			answeringPeer = preferQualifiedPeer(current.peerLID, ev.From)
		}
		if answeringPeer != "" && answeringPeer != current.peerLID {
			current.peerLID = answeringPeer
			rekeyPeer = current.rekeyPeer
		}
	}
	e.mu.Unlock()
	if rekeyPeer != nil {
		if err := rekeyPeer(answeringPeer); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Str("peer_lid", answeringPeer).Msg("failed to rekey media to answering device")
		} else {
			e.c.log.Info().Str("call_id", ev.CallID).Str("peer_lid", answeringPeer).Msg("rekeyed media to answering device")
		}
	}
	if m.call != nil && m.call.State() < CallPhaseConnecting {
		m.call.setPhase(CallPhaseConnecting)
	}
	if m.call != nil {
		m.call.markPeerAccepted()
	}
	e.c.log.Info().
		Str("call_id", ev.CallID).
		Str("from", ev.From.String()).
		Str("platform", ev.RemotePlatform).
		Bool("video", m.localVideo || m.remoteVideo).
		Msg("peer accepted outgoing call")
	e.c.diag.Emit("meta", map[string]any{
		"event": "peer_accept", "call_id": ev.CallID,
		"from": ev.From.String(), "platform": ev.RemotePlatform, "video": m.localVideo || m.remoteVideo,
	})
	e.maybeStartMedia(ev.CallID)
}

func inviteDeviceCapability(device types.JID, node *waBinary.Node) (groupCallDevice, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/1ebd064663ac336ff3d1fc65d9baa974148fe73e/datasheets/voip-group-participant-invite.md#L36-L72
	if device.IsEmpty() {
		return groupCallDevice{}, false
	}
	capability := findChild(node, "capability")
	if capability == nil {
		return groupCallDevice{}, false
	}
	value, ok := capability.Content.([]byte)
	if !ok || len(value) == 0 {
		return groupCallDevice{}, false
	}
	version, err := strconv.ParseUint(capability.AttrGetter().String("ver"), 10, 32)
	if err != nil {
		return groupCallDevice{}, false
	}
	return groupCallDevice{
		JID: device, CapabilityVersion: uint32(version),
		Capability: append([]byte(nil), value...),
	}, true
}

func preferQualifiedPeer(current string, signaled types.JID) string {
	if signaled.IsEmpty() {
		return current
	}
	parsed, err := types.ParseJID(current)
	if err == nil &&
		parsed.User == signaled.User &&
		parsed.Server == signaled.Server &&
		parsed.Device != 0 &&
		signaled.Device == 0 {
		return current
	}
	return signaled.String()
}

// onReject tears down an outgoing call when the peer declines it.
func (e *engine) onReject(ev *events.CallReject) {
	m := e.lookup(ev.CallID)
	if m == nil {
		return
	}
	e.c.log.Info().
		Str("call_id", ev.CallID).
		Str("from", ev.From.String()).
		Msg("peer rejected call")
	e.c.diag.Emit("meta", map[string]any{
		"event": "peer_reject", "call_id": ev.CallID, "from": ev.From.String(),
	})
	e.finishCall(ev.CallID, "rejected")
}

// rlProbe is one relay candidate from a relaylatency probe.
type rlProbe struct {
	latency   uint32
	relayName string
	addr      []byte
}

// applyVoipSettingsCodec finds the <voip_settings> blob under node (an inbound
// <offer> or an outbound call <ack>), parses it, and records the selected audio
// codec on the call. Absent or unparseable settings leave the call on MLow. The
// caller holds e.mu.
func (e *engine) applyVoipSettingsCodec(m *engineCall, node *waBinary.Node, callID string) {
	vsNode := findChild(node, "voip_settings")
	if vsNode == nil {
		return
	}
	content, _ := vsNode.Content.([]byte)
	vs, err := signaling.ParseVoipSettings(content, e.c.log)
	if err != nil {
		e.c.log.Debug().Err(err).Str("call_id", callID).Msg("voip_settings parse failed; keeping mlow")
		return
	}
	m.codec = selectAudioCodec(vs)
	e.c.log.Info().
		Str("call_id", callID).
		Str("codec", m.codec.String()).
		Bool("use_mlow_codec_v1", vs.UseMlowCodecV1).
		Msg("selected audio codec from voip_settings")
}

// onCallAck handles an <ack class="call"> node. For an outbound offer the relay
// allocation arrives here (whatsmeow otherwise drops the ack), which is what lets the
// caller bring up media. An error ack tears the call down.
func (e *engine) onCallAck(ack *waBinary.Node) {
	if errCode := ack.AttrGetter().String("error"); errCode != "" {
		callID := ""
		if en := findChild(ack, "error"); en != nil {
			callID = en.AttrGetter().String("call-id")
		}
		e.c.log.Warn().Str("call_id", callID).Str("error_code", errCode).Msg("call rejected by server")
		e.finishCall(callID, "server:"+errCode)
		return
	}
	groupUpdate, isGroup, groupErr := signaling.ParseInitialGroupCallAck(ack)
	if groupErr != nil {
		e.c.log.Warn().Err(groupErr).Msg("parse initial group call ack failed")
		return
	}
	if isGroup {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/7cb6045001dafd2514f53e85cd8c3e419c13adbe/datasheets/voip-initial-group-call.md#L27-L33
		e.applyGroupUpdate(groupCallUpdateFromSignaling(*groupUpdate))
		return
	}
	r := findRelay(ack)
	if r == nil {
		return
	}
	callID := r.AttrGetter().String("call-id")
	if callID == "" {
		return
	}
	e.c.log.Info().Str("call_id", callID).Msg("relay allocation arrived in call ack")
	e.mu.Lock()
	if m := e.calls[callID]; m != nil {
		e.applyVoipSettingsCodec(m, ack, callID)
	}
	e.mu.Unlock()
	e.onRelay(callID, ack)
}

// onCallRaw sees every raw <call> node before whatsmeow processes it. It fires the
// deferred <accept> when the caller's first <mute_v2> arrives (whatsmeow surfaces no
// mute event, so this is the only place we see it).
// onCallRaw inspects a raw <call> node before whatsmeow processes it. It returns true when
// it has fully handled the node (including sending the appropriate ack), so the caller skips
// whatsmeow's generic typeless ack.
func (e *engine) onCallRaw(callNode *waBinary.Node) bool {
	kids := callNode.GetChildren()
	if len(kids) == 0 {
		return false
	}
	switch kids[0].Tag {
	case "group_update", "enc_rekey", "waiting_room_update", "user_action", "screen_share":
		// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L31-L56
		ack, ok := signaling.BuildCallControlAck(callNode, kids[0].Tag)
		if !ok {
			e.c.log.Warn().
				Str("action", kids[0].Tag).
				Msg("call control ack: missing id/from")
		} else if err := e.transmitCallNode(context.Background(), ack); err != nil {
			e.c.log.Warn().
				Err(err).
				Str("action", kids[0].Tag).
				Str("id", ack.AttrGetter().String("id")).
				Msg("send typed call control ack failed")
		}
		e.onUnknownCallEvent(callNode)
		return true
	case "mute_v2":
		mv := kids[0].AttrGetter()
		callID := mv.String("call-id")
		if callID == "" {
			return false
		}
		muteState := mv.String("mute-state")
		muted := muteState == "1"
		// The deferred <accept> fires on the FIRST mute_v2 only — it arrives right after
		// the relaylatency/transport. Later mute_v2 nodes are in-call mute-state changes
		// (e.g. 1→0) and must not re-run the accept path on an already-accepted call.
		e.mu.Lock()
		m := e.calls[callID]
		pending := m != nil && m.acceptPending
		e.mu.Unlock()
		if m != nil && m.call != nil {
			if fn := m.call.onMuteStateFn(); fn != nil {
				fn(muted)
			}
		}
		if !pending {
			e.c.log.Debug().
				Str("call_id", callID).
				Str("mute_state", muteState).
				Bool("muted", muted).
				Msg("mute_v2 observed; call not awaiting accept")
			return false
		}
		e.c.log.Info().
			Str("call_id", callID).
			Str("mute_state", muteState).
			Bool("muted", muted).
			Msg("first mute_v2 received; sending deferred accept")
		e.sendAccept(callID, callNode.AttrGetter().JID("from"), mv.JID("call-creator"))
		return false
	case "video":
		// Acknowledge the <video> stanza with type="video" — the mid-call video-upgrade
		// (state=11) acceptance signal. whatsmeow's generic typeless ack does not satisfy the
		// sender, which then cancels the upgrade after ~5s before streaming any video.
		e.ackVideoStanza(callNode)
		e.onVideoStanza(&kids[0])
		return true
	}
	return false
}

// ackVideoStanza sends the typed <ack class="call" type="video"> for an inbound <video>
// <call> node (replicating the real WhatsApp client; whatsmeow would otherwise send a bare
// typeless ack that the peer treats as non-acceptance of a video upgrade).
func (e *engine) ackVideoStanza(callNode *waBinary.Node) {
	ack, ok := signaling.BuildVideoAck(callNode)
	if !ok {
		e.c.log.Warn().Msg("video ack: missing id/from, not acking")
		return
	}
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), ack); err != nil {
		e.c.log.Warn().Err(err).Str("id", ack.AttrGetter().String("id")).Msg("send video ack failed")
		return
	}
	e.c.log.Debug().Str("id", ack.AttrGetter().String("id")).Msg("sent type=video ack")
}

// onVideoStanza handles an inbound standalone <video> state stanza — the peer's video
// on/off and device orientation — and fires the Call's OnVideoState listener. whatsmeow
// surfaces no event for it (same as mute_v2), so it is intercepted here.
func (e *engine) onVideoStanza(v *waBinary.Node) {
	ag := v.AttrGetter()
	callID := ag.String("call-id")
	if callID == "" {
		return
	}
	state, _ := strconv.Atoi(ag.OptionalString("state"))
	orientation, _ := strconv.Atoi(ag.OptionalString("device_orientation"))
	e.c.log.Debug().Str("call_id", callID).Int("state", state).Int("orientation", orientation).Msg("inbound video state")
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil {
		e.mu.Unlock()
		return
	}
	sender := m.videoTx
	call := m.call
	to, creator := m.from, m.creator
	requestKeyframe := false
	disableSender := false
	enableSender := false
	announceEnabled := false
	switch state {
	case signaling.VideoStateUpgradeRequest, signaling.VideoStateUpgradeRequestV2:
		m.peerVideoUpgrade = true
	case signaling.VideoStateEnabled:
		m.remoteVideo = true
		if m.localVideo && m.videoGate {
			m.videoGate = false
			enableSender = true
			requestKeyframe = true
		}
	case signaling.VideoStateDisabled, signaling.VideoStateStopped:
		m.remoteVideo = false
	case signaling.VideoStateUpgradeAccept:
		m.localVideo = true
		m.videoGate = false
		announceEnabled = true
	case signaling.VideoStateUpgradeReject, signaling.VideoStateUpgradeCancel:
		m.peerVideoUpgrade = false
		if m.videoGate {
			m.localVideo = false
			m.videoGate = false
			disableSender = true
		}
	}
	e.mu.Unlock()
	if announceEnabled {
		orientation := 0
		node := signaling.BuildVideoStateWithParams(signaling.VideoStateParams{
			CallID: callID, To: to, CallCreator: creator, WrapperID: e.nextCallNodeID(),
			State: signaling.VideoStateEnabled, DeviceOrientation: &orientation,
		})
		if err := e.transmitCallNode(context.Background(), node); err != nil {
			e.mu.Lock()
			if current := e.calls[callID]; current == m {
				current.localVideo = false
				current.videoGate = false
			}
			e.mu.Unlock()
			disableSender = true
			announceEnabled = false
			if e.c != nil {
				e.c.log.Warn().Err(err).Str("call_id", callID).Msg("video enabled announcement failed")
			}
		} else {
			enableSender = true
			requestKeyframe = true
		}
	}
	if sender != nil {
		if enableSender {
			sender.enable(false)
		} else if disableSender {
			sender.disable()
		}
	}
	if requestKeyframe && call != nil {
		call.requestVideoKeyframe()
	}
	if call != nil {
		if fn := call.onVideoStateFn(); fn != nil {
			fn(VideoState{
				Active:      state == signaling.VideoStateEnabled,
				Upgrade:     state == signaling.VideoStateUpgradeRequest || state == signaling.VideoStateUpgradeRequestV2,
				Orientation: orientation,
				Raw:         state,
			})
		}
	}
}

// onTerminate tears down a call's media and fires the Call's OnEnd listener.
func (e *engine) onTerminate(callID, reason string) {
	e.c.log.Info().Str("call_id", callID).Str("reason", reason).Msg("call terminated")
	e.finishCall(callID, reason)
}

func (e *engine) finishCall(callID, reason string) {
	if callID == "" {
		return
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m != nil {
		delete(e.calls, callID)
	}
	var cancel, waitingRoomCancel context.CancelFunc
	var call *Call
	var receivers *participantReceiveRegistry
	if m != nil {
		cancel = m.cancel
		m.cancel = nil
		waitingRoomCancel = m.waitingRoomCancel
		m.waitingRoomCancel = nil
		clear(m.groupRawEpoch)
		m.groupRawEpoch = nil
		receivers = m.groupReceivers
		m.groupReceivers = nil
		call = m.call
	}
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if receivers != nil {
		receivers.clear()
	}
	if waitingRoomCancel != nil {
		waitingRoomCancel()
	}
	if call == nil || call.State() == CallPhaseEnded {
		return
	}
	call.mu.Lock()
	player := call.player
	call.player = nil
	sink := call.sink
	call.sink = nil
	call.mu.Unlock()
	if player != nil {
		player.Stop()
	}
	if sink != nil {
		if err := sink.Close(); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("close call audio sink failed")
		}
	}
	call.setPhase(CallPhaseEnded)
	if fn := call.onEndFn(); fn != nil {
		fn(reason)
	}
}

// installCallAckHook injects an "ack" entry into whatsmeow's unexported nodeHandlers map
// and wraps the "call" handler so the engine also sees the raw <call> node (with its
// stanza id, which the CallOffer event drops). whatsmeow has no <ack> handler — it
// silently drops <ack> nodes — but an outbound call's relay allocation arrives only
// inside <ack class="call" type="offer">, so without intercepting it the caller never
// learns the relay endpoint and media never starts. Called before Connect so the map
// write never races the receive loop.
//
// NOT VALIDATED: reaches into the client's unexported nodeHandlers via reflection +
// unsafe; covered only by a live call against the real relay.
func (e *engine) installCallAckHook() error {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/e9a033b24933cc8d90fa5ff8991b1268ba80a140/client.go#L110-L120
	cli := e.c.wa
	if cli == nil {
		return errors.New("meowcaller: raw call adapter has no whatsmeow client")
	}
	if cli.IsConnected() {
		return errors.New("meowcaller: construct the client before connecting whatsmeow")
	}
	clientValue := reflect.ValueOf(cli)
	if clientValue.Kind() != reflect.Pointer || clientValue.IsNil() {
		return errors.New("meowcaller: raw call adapter received an invalid whatsmeow client")
	}
	clientValue = clientValue.Elem()
	if !clientValue.IsValid() || clientValue.Kind() != reflect.Struct {
		return errors.New("meowcaller: raw call adapter cannot inspect whatsmeow client")
	}
	field := clientValue.FieldByName("nodeHandlers")
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	nodeType := reflect.TypeOf((*waBinary.Node)(nil))
	if !field.IsValid() ||
		!field.CanAddr() ||
		field.Kind() != reflect.Map ||
		field.Type().Key().Kind() != reflect.String ||
		field.Type().Elem().Kind() != reflect.Func ||
		field.Type().Elem().NumIn() != 2 ||
		field.Type().Elem().In(0) != contextType ||
		field.Type().Elem().In(1) != nodeType ||
		field.Type().Elem().NumOut() != 0 {
		return errors.New("meowcaller: upstream whatsmeow node handler layout changed")
	}
	handlers := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	if handlers.IsNil() {
		return errors.New("meowcaller: upstream whatsmeow node handlers are unavailable")
	}
	origAck := handlers.MapIndex(reflect.ValueOf("ack"))
	ackHandler := reflect.MakeFunc(field.Type().Elem(), func(args []reflect.Value) []reflect.Value {
		node := args[1].Interface().(*waBinary.Node)
		if node.AttrGetter().String("class") != "call" {
			if origAck.IsValid() && !origAck.IsNil() {
				origAck.Call(args)
			}
			return nil
		}
		e.onCallAck(node)
		return nil
	})
	handlers.SetMapIndex(reflect.ValueOf("ack"), ackHandler)
	origCall := handlers.MapIndex(reflect.ValueOf("call"))
	callHandler := reflect.MakeFunc(field.Type().Elem(), func(args []reflect.Value) []reflect.Value {
		node := args[1].Interface().(*waBinary.Node)
		// onCallRaw returns true when it fully handled the node (incl. its own ack), so
		// whatsmeow's generic typeless ack is skipped — the <video> upgrade needs a typed
		// type="video" ack, which a bare ack does not satisfy (the peer reverts otherwise).
		if e.onCallRaw(node) {
			return nil
		}
		if origCall.IsValid() && !origCall.IsNil() {
			origCall.Call(args)
		}
		return nil
	})
	handlers.SetMapIndex(reflect.ValueOf("call"), callHandler)
	return nil
}

// ---- whatsmeow glue (ported from examples/cli/call.go) ----

// parseCallTarget turns a CLI-style target into a JID. A string with '@' is a real JID
// (a LID to call directly, or a phone JID to resolve); a bare string is a phone number.
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
		// c.us is the legacy phone-number spelling still commonly used by
		// integrations. Call signaling resolves phone peers through the modern
		// s.whatsapp.net namespace, so normalize the alias before resolving the
		// target and building the call offer.
		if jid.Server == types.LegacyUserServer {
			jid.Server = types.DefaultUserServer
		}
		return jid, nil
	}
	return types.NewJID(strings.TrimPrefix(target, "+"), types.DefaultUserServer), nil
}

// resolvePeerLID turns a target (phone number, phone JID, or @lid JID) into the peer's
// LID — the address the call's E2E keys and SSRCs derive from. A LID is used directly;
// a phone JID is mapped via the LID store, seeded by a usync query if not cached.
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
// fetching a pre-key bundle if no session exists yet. Returns the ciphertext, the enc
// type ("pkmsg" for a fresh session, "msg" for an existing one), and whether the offer
// must carry our <device-identity> (true for pkmsg).
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

// decryptInboundCallKey pulls the <enc> from the offer node and decrypts the
// Message{Call{CallKey}} under our Signal session.
func decryptInboundCallKey(ctx context.Context, cli *whatsmeow.Client, ev *events.CallOffer) ([]byte, error) {
	if ev.Data == nil {
		return nil, errors.New("offer has no data node")
	}
	var enc *waBinary.Node
	for i := range ev.Data.GetChildren() {
		if c := &ev.Data.GetChildren()[i]; c.Tag == "enc" {
			enc = c
			break
		}
	}
	if enc == nil {
		return nil, errors.New("offer has no enc node")
	}
	isPreKey := enc.AttrGetter().String("type") == "pkmsg"
	pt, _, err := cli.DangerousInternals().DecryptDM(ctx, enc, ev.From, isPreKey, ev.Timestamp)
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

// newCallID returns a call id in WhatsApp's shape: 16 random bytes as uppercase hex.
func newCallID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// ---- relay signaling parse (ported from examples/cli/media.go) ----

type relayAddress struct {
	ipv4 string
	port uint16
}

type relayEndpoint struct {
	relayID     uint32
	relayName   string
	tokenID     uint32
	authTokenID uint32
	isFNA       bool
	addresses   []relayAddress
}

type relayData struct {
	relayKeyASCII []byte   // raw <key> content — the STUN MESSAGE-INTEGRITY key
	relayTokens   [][]byte // indexed <token id=…>
	endpoints     []relayEndpoint
	peerJID       types.JID
}

func nodeBytes(n *waBinary.Node) []byte {
	switch c := n.Content.(type) {
	case []byte:
		return c
	case string:
		return []byte(c)
	}
	return nil
}

func childByTag(n *waBinary.Node, tag string) *waBinary.Node {
	kids := n.GetChildren()
	for i := range kids {
		if kids[i].Tag == tag {
			return &kids[i]
		}
	}
	return nil
}

// findRelay recursively locates the <relay> node anywhere under n (it can sit under
// <offer> or a sibling <relaylatency>/<transport>).
func findRelay(n *waBinary.Node) *waBinary.Node {
	if n == nil {
		return nil
	}
	if n.Tag == "relay" {
		return n
	}
	kids := n.GetChildren()
	for i := range kids {
		if r := findRelay(&kids[i]); r != nil {
			return r
		}
	}
	return nil
}

// findChild recursively locates the first node with the given tag under n.
func findChild(n *waBinary.Node, tag string) *waBinary.Node {
	if n == nil {
		return nil
	}
	if n.Tag == tag {
		return n
	}
	kids := n.GetChildren()
	for i := range kids {
		if r := findChild(&kids[i], tag); r != nil {
			return r
		}
	}
	return nil
}

// decodeLatency reverses the relay-latency wire encoding (0x2000000 + rttMs).
func decodeLatency(enc string) uint32 {
	v, err := strconv.ParseUint(enc, 10, 32)
	if err != nil || v < 0x0200_0000 {
		return 0
	}
	return uint32(v) - 0x0200_0000
}

func attrUint(n *waBinary.Node, key string) uint32 {
	v, _ := strconv.ParseUint(n.AttrGetter().String(key), 10, 32)
	return uint32(v)
}

const maxRelayTokens = 64

func parseIndexedTokens(node *waBinary.Node, tag string) [][]byte {
	var tokens [][]byte
	kids := node.GetChildren()
	for i := range kids {
		c := &kids[i]
		if c.Tag != tag {
			continue
		}
		b := nodeBytes(c)
		if b == nil {
			continue
		}
		id := len(tokens)
		if s := c.AttrGetter().String("id"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				id = n
			}
		}
		if id >= maxRelayTokens {
			continue
		}
		for len(tokens) <= id {
			tokens = append(tokens, nil)
		}
		tokens[id] = b
	}
	return tokens
}

// parseRelayData ports parse_relay_data: <key>, indexed <token>, and te2 endpoints.
func parseRelayData(node *waBinary.Node) *relayData {
	rd := &relayData{}
	if key := childByTag(node, "key"); key != nil {
		rd.relayKeyASCII = nodeBytes(key)
	}
	rd.relayTokens = parseIndexedTokens(node, "token")

	kids := node.GetChildren()
	peerPID := node.AttrGetter().String("peer_pid")
	for i := range kids {
		child := &kids[i]
		if child.Tag == "participant" && peerPID != "" && child.AttrGetter().String("pid") == peerPID {
			rd.peerJID = child.AttrGetter().JID("jid")
			continue
		}
		if child.Tag != "te2" {
			continue
		}
		ab := nodeBytes(child)
		if len(ab) != 6 { // IPv4:port only (IPv6 endpoints skipped)
			continue
		}
		ep := relayEndpoint{
			relayID:     attrUint(child, "relay_id"),
			relayName:   child.AttrGetter().String("relay_name"),
			tokenID:     attrUint(child, "token_id"),
			authTokenID: attrUint(child, "auth_token_id"),
			isFNA:       child.AttrGetter().String("is_fna") == "1",
			addresses: []relayAddress{{
				ipv4: fmt.Sprintf("%d.%d.%d.%d", ab[0], ab[1], ab[2], ab[3]),
				port: binary.BigEndian.Uint16(ab[4:6]),
			}},
		}
		rd.endpoints = append(rd.endpoints, ep)
	}
	return rd
}

// getMediaRelayEndpoint prefers an outbound (non-FNA, auth_token_id≠0) endpoint, else
// any non-FNA, else the first. For an inbound call the caller's uplink RTP lands on their
// FNA-marked relay, so we must allocate on that same relay or the relay never bridges the
// peer's media (the callee connects but hears nothing).
func getMediaRelayEndpoint(rd *relayData, inbound bool) *relayEndpoint {
	if inbound {
		for i := range rd.endpoints {
			if e := &rd.endpoints[i]; e.isFNA {
				return e
			}
		}
	}
	for i := range rd.endpoints {
		if e := &rd.endpoints[i]; !e.isFNA && e.authTokenID != 0 {
			return e
		}
	}
	for i := range rd.endpoints {
		if e := &rd.endpoints[i]; !e.isFNA {
			return e
		}
	}
	if len(rd.endpoints) > 0 {
		return &rd.endpoints[0]
	}
	return nil
}
