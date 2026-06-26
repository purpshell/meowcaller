package signaling

import (
	"strconv"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Outbound call-signaling builders (offer/accept/preaccept/transport/relaylatency/
// heartbeat/terminate/mute/reject) as free Node builders. The <offer> child order
// is load-bearing (the server returns 439 if it is wrong). Stanza ids generated
// from random bytes are passed in so the builders stay pure.

// CapabilityOffer is the capability blob for <offer>/<accept> (ver=1).
var CapabilityOffer = []byte{0x01, 0x05, 0xf7, 0x09, 0xe4, 0xbb, 0x13}

// CapabilityPreaccept is the capability blob for <preaccept> (ver=1).
var CapabilityPreaccept = []byte{0x01, 0x05, 0xf7, 0x09, 0xe4, 0xbb, 0x07}

// EncodeLatency is the relay latency wire encoding: 0x2000000 + rttMs.
func EncodeLatency(rttMs uint32) string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L17-L19
	return strconv.FormatUint(uint64(0x02000000+rttMs), 10)
}

// OfferDeviceKey is one per-device encrypted callKey entry inside <offer>.
type OfferDeviceKey struct {
	DeviceJid  types.JID
	Ciphertext []byte
	EncType    string // "pkmsg" or "msg"
}

// OfferParams are the inputs to BuildOffer.
type OfferParams struct {
	CallID         string
	To             types.JID
	CallCreator    types.JID
	DeviceKeys     []OfferDeviceKey
	PrivacyToken   []byte // nil = absent
	Capability     []byte // nil = absent
	DeviceIdentity []byte // nil = absent
	Video          bool   // true = advertise <video> (video call)
}

// BuildOffer builds <call to=peer><offer …>…</offer></call> with the mandatory
// child order: privacy → audio(8k) → audio(16k) → net → capability →
// destination|enc → encopt → device-identity.
func BuildOffer(p *OfferParams, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L42-L100
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", p.CallID).
		Int("device_keys", len(p.DeviceKeys)).
		Bool("has_privacy_token", p.PrivacyToken != nil).
		Bool("has_capability", p.Capability != nil).
		Bool("has_device_identity", p.DeviceIdentity != nil).
		Msg("building offer stanza")
	var children []waBinary.Node
	if p.PrivacyToken != nil {
		children = append(children, waBinary.Node{Tag: "privacy", Content: p.PrivacyToken})
	}
	children = append(children, audioOpus("8000"), audioOpus("16000"))
	if p.Video {
		children = append(children, videoOfferNode())
	}
	children = append(children, waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": "3"}})
	if p.Capability != nil {
		children = append(children, waBinary.Node{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: p.Capability})
	}
	if len(p.DeviceKeys) > 1 {
		lg.Trace().Int("device_keys", len(p.DeviceKeys)).Msg("offer: multi-device, using destination")
		tos := make([]waBinary.Node, len(p.DeviceKeys))
		for i, dk := range p.DeviceKeys {
			tos[i] = waBinary.Node{Tag: "to", Attrs: waBinary.Attrs{"jid": dk.DeviceJid}, Content: []waBinary.Node{encNode(dk)}}
		}
		children = append(children, waBinary.Node{Tag: "destination", Content: tos})
	} else if len(p.DeviceKeys) == 1 {
		lg.Trace().Msg("offer: single device, using inline enc")
		children = append(children, encNode(p.DeviceKeys[0]))
	}
	children = append(children, waBinary.Node{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}})
	if p.DeviceIdentity != nil {
		children = append(children, waBinary.Node{Tag: "device-identity", Content: p.DeviceIdentity})
	}
	return callWrap(p.To, nil, offerAction("offer", p.CallID, p.CallCreator, children))
}

// encNode builds one <enc v=2 type=… count=0> child carrying the ciphertext.
func encNode(dk OfferDeviceKey) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L101-L108
	return waBinary.Node{
		Tag:     "enc",
		Attrs:   waBinary.Attrs{"v": "2", "type": dk.EncType, "count": "0"},
		Content: dk.Ciphertext,
	}
}

// AcceptParams are the inputs to BuildAccept.
type AcceptParams struct {
	CallID       string
	To           types.JID
	CallCreator  types.JID
	AudioRates   []string
	RelayTe      []byte         // nil = absent
	Rte          []byte         // nil = absent
	VoipSettings []byte         // nil = absent
	Capability   []byte         // nil = absent
	Metadata     waBinary.Attrs // nil = absent
	Video        bool           // true = advertise <video> (video call)
}

// BuildAccept builds <accept>: audio → [te priority=2] → net medium=2 → encopt →
// [capability] → [metadata] → [rte] → [voip_settings].
func BuildAccept(p *AcceptParams, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L124-L162
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", p.CallID).
		Strs("audio_rates", p.AudioRates).
		Bool("has_relay_te", p.RelayTe != nil).
		Bool("has_rte", p.Rte != nil).
		Bool("has_voip_settings", p.VoipSettings != nil).
		Bool("has_capability", p.Capability != nil).
		Bool("has_metadata", p.Metadata != nil).
		Msg("building accept stanza")
	children := make([]waBinary.Node, 0, len(p.AudioRates)+5)
	for _, rate := range p.AudioRates {
		children = append(children, audioOpus(rate))
	}
	if p.RelayTe != nil {
		children = append(children, waBinary.Node{Tag: "te", Attrs: waBinary.Attrs{"priority": "2"}, Content: p.RelayTe})
	}
	children = append(children, waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": "2"}})
	children = append(children, waBinary.Node{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}})
	if p.Video {
		children = append(children, videoAcceptNode())
	}
	if p.Capability != nil {
		children = append(children, waBinary.Node{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: p.Capability})
	}
	if p.Metadata != nil {
		children = append(children, waBinary.Node{Tag: "metadata", Attrs: p.Metadata})
	}
	if p.Rte != nil {
		children = append(children, waBinary.Node{Tag: "rte", Content: p.Rte})
	}
	if p.VoipSettings != nil {
		children = append(children, waBinary.Node{Tag: "voip_settings", Attrs: waBinary.Attrs{"uncompressed": "1"}, Content: p.VoipSettings})
	}
	return callWrap(p.To, nil, offerAction("accept", p.CallID, p.CallCreator, children))
}

// audioOpus builds one <audio enc=opus rate=…> advertisement child.
func audioOpus(rate string) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L163-L169
	return waBinary.Node{Tag: "audio", Attrs: waBinary.Attrs{"enc": "opus", "rate": rate}}
}

// BuildPreaccept builds <preaccept>: audio → encopt → capability(preaccept blob),
// wrapped with the random wrapper id.
func BuildPreaccept(callID string, to, callCreator types.JID, wrapperID string, audioRates []string, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L171-L201
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", callID).
		Str("wrapper_id", wrapperID).
		Strs("audio_rates", audioRates).
		Msg("building preaccept stanza")
	children := make([]waBinary.Node, 0, len(audioRates)+2)
	for _, rate := range audioRates {
		children = append(children, audioOpus(rate))
	}
	children = append(children, waBinary.Node{Tag: "encopt", Attrs: waBinary.Attrs{"keygen": "2"}})
	children = append(children, waBinary.Node{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: CapabilityPreaccept})
	return callWrap(to, &wrapperID, offerAction("preaccept", callID, callCreator, children))
}

// TransportParams are the inputs to BuildTransport.
type TransportParams struct {
	CallID               string
	To                   types.JID
	CallCreator          types.JID
	P2PCandRound         *string // nil = absent
	TransportMessageType *string // nil = absent
	RelayTe              []byte  // nil = absent
}

// BuildTransport builds <transport>: optional <te priority=1> then
// <net medium=2 [protocol=0]> (protocol omitted only when type == "9").
func BuildTransport(p *TransportParams, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L203-L243
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", p.CallID).
		Bool("has_relay_te", p.RelayTe != nil).
		Bool("has_p2p_cand_round", p.P2PCandRound != nil).
		Bool("has_transport_message_type", p.TransportMessageType != nil).
		Msg("building transport stanza")
	attrs := waBinary.Attrs{"call-id": p.CallID, "call-creator": p.CallCreator}
	if p.P2PCandRound != nil {
		attrs["p2p-cand-round"] = *p.P2PCandRound
	}
	if p.TransportMessageType != nil {
		attrs["transport-message-type"] = *p.TransportMessageType
	}
	var children []waBinary.Node
	if p.RelayTe != nil {
		children = append(children, waBinary.Node{Tag: "te", Attrs: waBinary.Attrs{"priority": "1"}, Content: p.RelayTe})
	}
	netAttrs := waBinary.Attrs{"medium": "2"}
	if p.TransportMessageType == nil || *p.TransportMessageType != "9" {
		netAttrs["protocol"] = "0"
	} else {
		lg.Trace().Msg("transport: type 9, omitting net protocol attr")
	}
	children = append(children, waBinary.Node{Tag: "net", Attrs: netAttrs})
	return callWrap(p.To, nil, waBinary.Node{Tag: "transport", Attrs: attrs, Content: children})
}

// RelayLatencyParams are the inputs to BuildRelayLatency.
type RelayLatencyParams struct {
	CallID       string
	To           types.JID
	CallCreator  types.JID
	LatencyMs    uint32
	RelayName    string
	AddressBytes []byte
	Devices      []types.JID // omit for inbound callee
}

// BuildRelayLatency builds <relaylatency> with a <te latency relay_name> and an
// optional <destination>.
func BuildRelayLatency(p *RelayLatencyParams, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L244-L262
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", p.CallID).
		Uint32("latency_ms", p.LatencyMs).
		Str("relay_name", p.RelayName).
		Int("address_bytes", len(p.AddressBytes)).
		Int("devices", len(p.Devices)).
		Msg("building relaylatency stanza")
	children := []waBinary.Node{{
		Tag:     "te",
		Attrs:   waBinary.Attrs{"latency": EncodeLatency(p.LatencyMs), "relay_name": p.RelayName},
		Content: p.AddressBytes,
	}}
	if len(p.Devices) > 0 {
		children = append(children, destinationTo(p.Devices))
	}
	return callWrap(p.To, nil, offerAction("relaylatency", p.CallID, p.CallCreator, children))
}

// BuildHeartbeat builds <call to={callID}@call id=…><heartbeat …/></call>.
func BuildHeartbeat(callID string, callCreator types.JID, wrapperID string, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L263-L283
	lg := pickLog(log)
	lg.Trace().
		Str("call_id", callID).
		Str("wrapper_id", wrapperID).
		Msg("building heartbeat stanza")
	action := waBinary.Node{Tag: "heartbeat", Attrs: waBinary.Attrs{"call-id": callID, "call-creator": callCreator}}
	return waBinary.Node{
		Tag:     "call",
		Attrs:   waBinary.Attrs{"to": callID + "@call", "id": wrapperID},
		Content: []waBinary.Node{action},
	}
}

// TerminateParams are the inputs to BuildTerminate.
type TerminateParams struct {
	CallID        string
	To            types.JID
	CallCreator   types.JID
	Reason        *string // nil = absent
	TargetDevices []types.JID
}

// BuildTerminate builds <terminate> with optional reason and target <destination>.
func BuildTerminate(p *TerminateParams, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L284-L296
	reason := ""
	if p.Reason != nil {
		reason = *p.Reason
	}
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", p.CallID).
		Str("reason", reason).
		Int("target_devices", len(p.TargetDevices)).
		Msg("building terminate stanza")
	attrs := waBinary.Attrs{"call-id": p.CallID, "call-creator": p.CallCreator}
	if p.Reason != nil {
		attrs["reason"] = *p.Reason
	}
	var content []waBinary.Node
	if len(p.TargetDevices) > 0 {
		content = []waBinary.Node{destinationTo(p.TargetDevices)}
	}
	return callWrap(p.To, nil, waBinary.Node{Tag: "terminate", Attrs: attrs, Content: content})
}

// BuildMuteV2 builds <mute_v2 call-id call-creator mute-state>.
func BuildMuteV2(callID string, to, callCreator types.JID, muteState string, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L297-L305
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", callID).
		Str("mute_state", muteState).
		Msg("building mute_v2 stanza")
	action := waBinary.Node{Tag: "mute_v2", Attrs: waBinary.Attrs{"call-id": callID, "call-creator": callCreator, "mute-state": muteState}}
	return callWrap(to, nil, action)
}

// BuildReject builds <reject call-id call-creator>.
func BuildReject(callID string, to, callCreator types.JID, log ...zerolog.Logger) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L306-L316
	lg := pickLog(log)
	lg.Debug().
		Str("call_id", callID).
		Msg("building reject stanza")
	action := waBinary.Node{Tag: "reject", Attrs: waBinary.Attrs{"call-id": callID, "call-creator": callCreator}}
	return callWrap(to, nil, action)
}

// offerAction builds an action node (offer/accept/preaccept/relaylatency) carrying
// call-id + call-creator and the given children.
func offerAction(tag, callID string, callCreator types.JID, children []waBinary.Node) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L317-L324
	return waBinary.Node{
		Tag:     tag,
		Attrs:   waBinary.Attrs{"call-id": callID, "call-creator": callCreator},
		Content: children,
	}
}

// destinationTo builds <destination> wrapping one <to jid=…> per device.
func destinationTo(devices []types.JID) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L325-L332
	tos := make([]waBinary.Node, len(devices))
	for i, jid := range devices {
		tos[i] = waBinary.Node{Tag: "to", Attrs: waBinary.Attrs{"jid": jid}}
	}
	return waBinary.Node{Tag: "destination", Content: tos}
}

// callWrap wraps an action in <call to=… [id=…]>.
func callWrap(to types.JID, id *string, action waBinary.Node) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L333-L341
	attrs := waBinary.Attrs{"to": to}
	if id != nil {
		attrs["id"] = *id
	}
	return waBinary.Node{Tag: "call", Attrs: attrs, Content: []waBinary.Node{action}}
}
