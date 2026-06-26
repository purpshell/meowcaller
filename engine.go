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
	"unsafe"

	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
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

	mu    sync.Mutex
	calls map[string]*engineCall // keyed by call-id
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

	direction CallDirection
	codec     AudioCodec   // audio codec for this call, selected from voip_settings (MLow default)
	isVideo   bool         // inbound offer advertised <video> (video call)
	videoTx   *videoSender // video send pipeline, live while media runs
	started   bool
	cancel    context.CancelFunc // tears down this call's media goroutine

	// The callee <accept> is deferred until the caller's <mute_v2> arrives.
	acceptPending bool
}

// newEngine creates the engine for a Client.
func newEngine(c *Client) *engine {
	return &engine{c: c, calls: map[string]*engineCall{}}
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
	e.installCallAckHook()
	e.c.wa.AddEventHandler(func(evt any) {
		switch ev := evt.(type) {
		case *events.CallOffer:
			e.onOffer(ev)
		case *events.CallRelayLatency:
			e.onRelay(ev.CallID, ev.Data)
			e.onRelayLatency(ev)
		case *events.CallTransport:
			e.onRelay(ev.CallID, ev.Data)
		case *events.CallTerminate:
			e.onTerminate(ev.CallID, ev.Reason)
		}
	})
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

// sendVideoFrame packetizes one encoded H.264 access unit and sends it to the relay, if a
// video send pipeline is live for the call.
func (e *engine) sendVideoFrame(callID string, au []byte) error {
	e.mu.Lock()
	var vs *videoSender
	if m := e.calls[callID]; m != nil {
		vs = m.videoTx
	}
	e.mu.Unlock()
	if vs == nil {
		return errors.New("meowcaller: call has no active video media")
	}
	vs.send(au)
	return nil
}

// placeCall resolves target to a LID, builds and sends the <offer>, registers the Call,
// and returns it; media starts when the peer answers and the relay endpoint arrives.
func (e *engine) placeCall(ctx context.Context, target string) (*Call, error) {
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
	e.mu.Unlock()

	e.c.diag.Emit("keying", map[string]any{
		"call_id": callID, "direction": "out", "self_lid": self.String(),
		"peer_lid": peerLID.String(), "device_count": len(deviceKeys),
		"call_key_hex": hex.EncodeToString(callKey[:]),
	})

	if err := cli.DangerousInternals().SendNode(ctx, offer); err != nil {
		return nil, fmt.Errorf("send offer: %w", err)
	}
	e.c.log.Info().Str("call_id", callID).Msg("offer sent; media starts when the relay endpoint arrives")
	e.c.diag.Emit("meta", map[string]any{"event": "offer_sent", "call_id": callID, "peer_lid": peerLID.String(), "direction": "out"})
	return call, nil
}

// onOffer handles an inbound <offer> event: it decrypts the callKey, captures any relay
// data, registers the Call in the Ringing phase, sends the <preaccept> eagerly (a
// preparation step, independent of the later Answer/Reject), and fires the
// OnIncomingCall listener. Only the <accept> is deferred to Answer.
func (e *engine) onOffer(ev *events.CallOffer) {
	// A "call ended" notification arrives offer-shaped, carrying is_call_ended/
	// terminate_reason (e.g. accepted_elsewhere). It is not a live call — engaging it
	// (preaccept/accept) just earns an "accept error 500". Ignore it.
	oag := ev.Data.AttrGetter()
	if oag.OptionalString("is_call_ended") == "1" || oag.OptionalString("terminate_reason") != "" {
		e.c.log.Warn().Str("call_id", ev.CallID).Msg("ignoring already-ended offer; not a live call")
		return
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
	// Detect a video call by the <video> child of the offer (ported from WaCalls).
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/callmanager_signaling.go#L24
	isVideo := signaling.OfferHasVideo(ev.Data)
	m.isVideo = isVideo
	if r := findRelay(ev.Data); r != nil {
		m.relay = parseRelayData(r)
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
	if err := e.sendPreaccept(ev.CallID, ev.From, ev.CallCreator); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Msg("preaccept failed")
	}

	if fn := e.c.incomingCallHandler(); fn != nil {
		fn(call)
	}
}

// sendPreaccept sends the <preaccept> for an inbound call — a preparation step done
// eagerly when the offer arrives (see onOffer), independent of the later Answer/Reject
// decision. Single rate 16000 + encopt + capability, NO metadata — built inline to match
// the captured WA-Web preaccept body exactly.
func (e *engine) sendPreaccept(callID string, to, creator types.JID) error {
	pre := waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"to": to, "id": e.c.wa.DangerousInternals().GenerateRequestID()},
		Content: []waBinary.Node{{
			Tag:   "preaccept",
			Attrs: waBinary.Attrs{"call-id": callID, "call-creator": creator},
			Content: []waBinary.Node{
				{Tag: "audio", Attrs: waBinary.Attrs{"enc": "opus", "rate": "16000"}},
				{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}},
				{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: signaling.CapabilityOffer},
			},
		}},
	}
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
	m.acceptPending = false
	e.mu.Unlock()

	accept := signaling.BuildAccept(&signaling.AcceptParams{
		CallID: callID, To: to, CallCreator: creator,
		AudioRates: []string{"16000"},
		Metadata:   waBinary.Attrs{"peer_abtest_bucket_id_list": "125208,94276"},
	})
	accept.Attrs["id"] = e.c.wa.DangerousInternals().GenerateRequestID()
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), accept); err != nil {
		e.c.log.Error().Err(err).Str("call_id", callID).Msg("send accept failed")
		return
	}
	e.c.log.Info().Str("call_id", callID).Msg("accepted (after mute_v2)")
}

// reject declines an inbound call.
func (e *engine) reject(c *Call) error {
	m := e.lookup(c.id)
	to, creator := c.peer, c.peer
	if m != nil {
		to, creator = m.from, m.creator
	}
	rej := signaling.BuildReject(c.id, to, creator)
	rej.Attrs["id"] = e.c.wa.GenerateMessageID()
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), rej); err != nil {
		return fmt.Errorf("send reject: %w", err)
	}
	e.stopMedia(c.id)
	c.setPhase(CallPhaseEnded)
	if fn := c.onEndFn(); fn != nil {
		fn("rejected")
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
	term.Attrs["id"] = e.c.wa.GenerateMessageID()
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), term); err != nil {
		return fmt.Errorf("send terminate: %w", err)
	}
	e.stopMedia(c.id)
	c.setPhase(CallPhaseEnded)
	if fn := c.onEndFn(); fn != nil {
		fn("hangup")
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
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil {
		e.mu.Unlock()
		return
	}
	m.relay = parseRelayData(r)
	e.mu.Unlock()
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
	var probes []rlProbe
	for i := range rl.GetChildren() {
		te := &rl.GetChildren()[i]
		if te.Tag != "te" {
			continue
		}
		ag := te.AttrGetter()
		probes = append(probes, rlProbe{
			latency:   decodeLatency(ag.String("latency")),
			relayName: ag.String("relay_name"),
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
		e.stopMedia(callID)
		if m := e.lookup(callID); m != nil && m.call != nil {
			m.call.setPhase(CallPhaseEnded)
			if fn := m.call.onEndFn(); fn != nil {
				fn("server:" + errCode)
			}
		}
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
	case "mute_v2":
		mv := kids[0].AttrGetter()
		callID := mv.String("call-id")
		if callID == "" {
			return false
		}
		// The deferred <accept> fires on the FIRST mute_v2 only — it arrives right after
		// the relaylatency/transport. Later mute_v2 nodes are in-call mute-state changes
		// (e.g. 1→0) and must not re-run the accept path on an already-accepted call.
		e.mu.Lock()
		m := e.calls[callID]
		pending := m != nil && m.acceptPending
		e.mu.Unlock()
		if !pending {
			e.c.log.Debug().
				Str("call_id", callID).
				Str("mute_state", mv.String("mute-state")).
				Msg("mute_v2 ignored; call not awaiting accept")
			return false
		}
		e.c.log.Info().Str("call_id", callID).Msg("first mute_v2 received; sending deferred accept")
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
	ag := callNode.AttrGetter()
	id := ag.String("id")
	from := ag.JID("from") // "from" is a JID attr (cf. the mute_v2 path), not a plain string
	if id == "" || from.IsEmpty() {
		e.c.log.Warn().Str("id", id).Msg("video ack: missing id/from, not acking")
		return
	}
	ack := waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{
		"class": "call", "id": id, "to": from, "type": "video",
	}}
	if err := e.c.wa.DangerousInternals().SendNode(context.Background(), ack); err != nil {
		e.c.log.Warn().Err(err).Str("id", id).Msg("send video ack failed")
		return
	}
	e.c.log.Debug().Str("id", id).Msg("sent type=video ack")
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
	m := e.lookup(callID)
	if m == nil {
		return
	}
	if m.call != nil {
		if fn := m.call.onVideoStateFn(); fn != nil {
			fn(VideoState{
				Active:      state == signaling.VideoStateActive,
				Upgrade:     state == signaling.VideoStateUpgrade,
				Orientation: orientation,
				Raw:         state,
			})
		}
	}
	// On a mid-call video upgrade (state=11), reply with our own <video> state so the call is
	// promoted to video server-side — the type="video" ack alone leaves the sender thinking
	// the callee never entered video mode, so it reverts to state=0 after ~5s. NOT VALIDATED.
	if state == signaling.VideoStateUpgrade {
		reply := signaling.BuildVideoState(callID, m.from, m.creator, e.c.wa.GenerateMessageID(),
			signaling.VideoStateActive, 0, signaling.VideoCodecH264)
		if err := e.c.wa.DangerousInternals().SendNode(context.Background(), reply); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("send video accept failed")
		} else {
			e.c.log.Info().Str("call_id", callID).Msg("sent <video> accept (state=1) for upgrade")
		}
	}
}

// onTerminate tears down a call's media and fires the Call's OnEnd listener.
func (e *engine) onTerminate(callID, reason string) {
	e.c.log.Info().Str("call_id", callID).Str("reason", reason).Msg("call terminated")
	e.stopMedia(callID)
	if m := e.lookup(callID); m != nil && m.call != nil {
		m.call.setPhase(CallPhaseEnded)
		if fn := m.call.onEndFn(); fn != nil {
			fn(reason)
		}
	}
}

// stopMedia cancels a call's media goroutine if it's running.
func (e *engine) stopMedia(callID string) {
	if callID == "" {
		return
	}
	e.mu.Lock()
	if m := e.calls[callID]; m != nil && m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	e.mu.Unlock()
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
func (e *engine) installCallAckHook() {
	cli := e.c.wa
	field := reflect.ValueOf(cli).Elem().FieldByName("nodeHandlers")
	handlers := *(*map[string]func(context.Context, *waBinary.Node))(unsafe.Pointer(field.UnsafeAddr()))
	handlers["ack"] = func(_ context.Context, node *waBinary.Node) {
		if node.AttrGetter().String("class") != "call" {
			return
		}
		e.onCallAck(node)
	}
	origCall := handlers["call"]
	handlers["call"] = func(ctx context.Context, node *waBinary.Node) {
		// onCallRaw returns true when it fully handled the node (incl. its own ack), so
		// whatsmeow's generic typeless ack is skipped — the <video> upgrade needs a typed
		// type="video" ack, which a bare ack does not satisfy (the peer reverts otherwise).
		if e.onCallRaw(node) {
			return
		}
		if origCall != nil {
			origCall(ctx, node)
		}
	}
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
	for i := range kids {
		te2 := &kids[i]
		if te2.Tag != "te2" {
			continue
		}
		ab := nodeBytes(te2)
		if len(ab) != 6 { // IPv4:port only (IPv6 endpoints skipped)
			continue
		}
		ep := relayEndpoint{
			relayID:     attrUint(te2, "relay_id"),
			relayName:   te2.AttrGetter().String("relay_name"),
			tokenID:     attrUint(te2, "token_id"),
			authTokenID: attrUint(te2, "auth_token_id"),
			isFNA:       te2.AttrGetter().String("is_fna") == "1",
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
// any non-FNA, else the first.
func getMediaRelayEndpoint(rd *relayData) *relayEndpoint {
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
