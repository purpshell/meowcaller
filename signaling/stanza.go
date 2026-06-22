package signaling

import (
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
	// TODO
	// agent suggestion: strconv.FormatUint(uint64(0x02000000+rttMs), 10) (uint32 add already wraps).
	// human input:
	return ""
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
	To             *types.JID
	CallCreator    *types.JID
	DeviceKeys     []OfferDeviceKey
	PrivacyToken   []byte // nil = absent
	Capability     []byte // nil = absent
	DeviceIdentity []byte // nil = absent
}

// BuildOffer builds <call to=peer><offer …>…</offer></call> with the mandatory
// child order: privacy → audio(8k) → audio(16k) → net → capability →
// destination|enc → encopt → device-identity.
func BuildOffer(p *OfferParams) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L42-L100
	// TODO
	// agent suggestion: append children in source order; >1 device key emits <destination> of <to><enc>,
	// else a bare <enc>; wrap with offerAction("offer", …) and callWrap(to, nil, …).
	// human input:
	return waBinary.Node{}
}

// AcceptParams are the inputs to BuildAccept.
type AcceptParams struct {
	CallID       string
	To           *types.JID
	CallCreator  *types.JID
	AudioRates   []string
	RelayTe      []byte // nil = absent
	Rte          []byte // nil = absent
	VoipSettings []byte // nil = absent
	Capability   []byte // nil = absent
}

// BuildAccept builds <accept>: audio → [te priority=2] → net medium=2 → encopt →
// [capability] → [rte] → [voip_settings].
func BuildAccept(p *AcceptParams) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L124-L162
	// TODO
	// agent suggestion: audio children from AudioRates; optional te(priority=2); net medium=2; encopt;
	// optional capability/rte/voip_settings; offerAction("accept", …) + callWrap.
	// human input:
	return waBinary.Node{}
}

// BuildPreaccept builds <preaccept>: audio → encopt → capability(preaccept blob),
// wrapped with the random wrapper id.
func BuildPreaccept(callID string, to, callCreator *types.JID, wrapperID string, audioRates []string) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L171-L201
	// TODO
	// agent suggestion: audio children; encopt keygen=2; capability ver=1 bytes=CapabilityPreaccept;
	// offerAction("preaccept", …) + callWrap(to, &wrapperID, …).
	// human input:
	return waBinary.Node{}
}

// TransportParams are the inputs to BuildTransport.
type TransportParams struct {
	CallID               string
	To                   *types.JID
	CallCreator          *types.JID
	P2PCandRound         *string // nil = absent
	TransportMessageType *string // nil = absent
	RelayTe              []byte  // nil = absent
}

// BuildTransport builds <transport>: optional <te priority=1> then
// <net medium=2 [protocol=0]> (protocol omitted only when type == "9").
func BuildTransport(p *TransportParams) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L203-L243
	// TODO
	// agent suggestion: transport action with call-id/call-creator + optional p2p-cand-round/
	// transport-message-type; optional te(priority=1); net medium=2 with protocol=0 unless type=="9".
	// human input:
	return waBinary.Node{}
}

// RelayLatencyParams are the inputs to BuildRelayLatency.
type RelayLatencyParams struct {
	CallID       string
	To           *types.JID
	CallCreator  *types.JID
	LatencyMs    uint32
	RelayName    string
	AddressBytes []byte
	Devices      []types.JID // omit for inbound callee
}

// BuildRelayLatency builds <relaylatency> with a <te latency relay_name> and an
// optional <destination>.
func BuildRelayLatency(p *RelayLatencyParams) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L244-L262
	// TODO
	// agent suggestion: te with latency=EncodeLatency(LatencyMs), relay_name, bytes=AddressBytes;
	// optional destinationTo(Devices); offerAction("relaylatency", …) + callWrap.
	// human input:
	return waBinary.Node{}
}

// BuildHeartbeat builds <call to={callID}@call id=…><heartbeat …/></call>.
func BuildHeartbeat(callID string, callCreator *types.JID, wrapperID string) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L263-L283
	// TODO
	// agent suggestion: heartbeat action with call-id/call-creator; outer call with to=callID+"@call",
	// id=wrapperID, content=[action].
	// human input:
	return waBinary.Node{}
}

// TerminateParams are the inputs to BuildTerminate.
type TerminateParams struct {
	CallID        string
	To            *types.JID
	CallCreator   *types.JID
	Reason        *string // nil = absent
	TargetDevices []types.JID
}

// BuildTerminate builds <terminate> with optional reason and target <destination>.
func BuildTerminate(p *TerminateParams) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L284-L296
	// TODO
	// agent suggestion: terminate action with call-id/call-creator + optional reason; if TargetDevices
	// non-empty add destinationTo; callWrap.
	// human input:
	return waBinary.Node{}
}

// BuildMuteV2 builds <mute_v2 call-id call-creator mute-state>.
func BuildMuteV2(callID string, to, callCreator *types.JID, muteState string) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L297-L305
	// TODO
	// agent suggestion: mute_v2 action with call-id/call-creator/mute-state; callWrap(to, nil, action).
	// human input:
	return waBinary.Node{}
}

// BuildReject builds <reject call-id call-creator>.
func BuildReject(callID string, to, callCreator *types.JID) waBinary.Node {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/stanza.rs#L306-L316
	// TODO
	// agent suggestion: reject action with call-id/call-creator; callWrap(to, nil, action).
	// human input:
	return waBinary.Node{}
}
