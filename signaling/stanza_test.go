package signaling

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func peerJID() types.JID {
	return types.JID{User: "214482127208608", Server: types.HiddenUserServer}
}

func creatorJID() types.JID {
	return types.JID{User: "243426515787784", Server: types.HiddenUserServer, Device: 19}
}

func contentNodes(t *testing.T, n waBinary.Node) []waBinary.Node {
	t.Helper()
	nodes, ok := n.Content.([]waBinary.Node)
	if !ok {
		t.Fatalf("node %q content is not []Node: %T", n.Tag, n.Content)
	}
	return nodes
}

func childTags(t *testing.T, call waBinary.Node) []string {
	t.Helper()
	action := contentNodes(t, call)[0]
	var tags []string
	for _, c := range contentNodes(t, action) {
		tags = append(tags, c.Tag)
	}
	return tags
}

func getChild(t *testing.T, n waBinary.Node, tag string) (waBinary.Node, bool) {
	t.Helper()
	for _, c := range contentNodes(t, n) {
		if c.Tag == tag {
			return c, true
		}
	}
	return waBinary.Node{}, false
}

func attrString(n waBinary.Node, key string) (string, bool) {
	v, ok := n.Attrs[key].(string)
	return v, ok
}

func eqTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOfferChildOrderIsLoadBearing pins the mandatory <offer> child order (single device key).
func TestOfferChildOrderIsLoadBearing(t *testing.T) {
	t.Skip("blocked: signaling/stanza bodies are stubs; enable when implemented")

	peer, creator := peerJID(), creatorJID()
	dk := OfferDeviceKey{DeviceJid: peer, Ciphertext: []byte{1, 2, 3}, EncType: "pkmsg"}
	call := BuildOffer(&OfferParams{
		CallID: "CID", To: &peer, CallCreator: &creator,
		DeviceKeys:   []OfferDeviceKey{dk},
		PrivacyToken: []byte{0xaa, 0xbb}, Capability: CapabilityOffer, DeviceIdentity: []byte{0xcc},
	})
	want := []string{"privacy", "audio", "audio", "net", "capability", "enc", "encopt", "device-identity"}
	if got := childTags(t, call); !eqTags(got, want) {
		t.Errorf("child tags = %v, want %v", got, want)
	}
	if call.Tag != "call" {
		t.Errorf("outer tag = %q, want call", call.Tag)
	}
	offer := contentNodes(t, call)[0]
	if offer.Tag != "offer" {
		t.Errorf("action tag = %q, want offer", offer.Tag)
	}
	if id, _ := attrString(offer, "call-id"); id != "CID" {
		t.Errorf("call-id = %q, want CID", id)
	}
}

// TestOfferMultiDeviceUsesDestination checks >1 device key emits <destination>, not <enc>.
func TestOfferMultiDeviceUsesDestination(t *testing.T) {
	t.Skip("blocked: signaling/stanza bodies are stubs; enable when implemented")

	peer, creator := peerJID(), creatorJID()
	keys := []OfferDeviceKey{
		{DeviceJid: peer, Ciphertext: []byte{1}, EncType: "pkmsg"},
		{DeviceJid: creator, Ciphertext: []byte{2}, EncType: "msg"},
	}
	call := BuildOffer(&OfferParams{CallID: "CID", To: &peer, CallCreator: &creator, DeviceKeys: keys})
	tags := childTags(t, call)
	hasDest, hasEnc := false, false
	for _, tg := range tags {
		if tg == "destination" {
			hasDest = true
		}
		if tg == "enc" {
			hasEnc = true
		}
	}
	if !hasDest || hasEnc {
		t.Errorf("tags = %v, want destination present and enc absent", tags)
	}
}

// TestAcceptAndPreacceptShape checks the accept and preaccept child orders.
func TestAcceptAndPreacceptShape(t *testing.T) {
	t.Skip("blocked: signaling/stanza bodies are stubs; enable when implemented")

	peer, creator := peerJID(), creatorJID()
	accept := BuildAccept(&AcceptParams{
		CallID: "CID", To: &peer, CallCreator: &creator,
		AudioRates: []string{"16000"}, RelayTe: make([]byte, 6), Capability: CapabilityOffer,
	})
	if got := childTags(t, accept); !eqTags(got, []string{"audio", "te", "net", "encopt", "capability"}) {
		t.Errorf("accept tags = %v", got)
	}
	pre := BuildPreaccept("CID", &peer, &creator, "abcd1234", []string{"8000", "16000"})
	if got := childTags(t, pre); !eqTags(got, []string{"audio", "audio", "encopt", "capability"}) {
		t.Errorf("preaccept tags = %v", got)
	}
	if id, _ := attrString(pre, "id"); id != "abcd1234" {
		t.Errorf("preaccept id = %q, want abcd1234", id)
	}
}

// TestTransportNetProtocolRule checks the net protocol=0 rule (omitted for type "9").
func TestTransportNetProtocolRule(t *testing.T) {
	t.Skip("blocked: signaling/stanza bodies are stubs; enable when implemented")

	peer, creator := peerJID(), creatorJID()
	round, t1type := "1", "1"
	t1 := BuildTransport(&TransportParams{
		CallID: "CID", To: &peer, CallCreator: &creator,
		P2PCandRound: &round, TransportMessageType: &t1type, RelayTe: make([]byte, 6),
	})
	action := contentNodes(t, t1)[0]
	if mt, _ := attrString(action, "transport-message-type"); mt != "1" {
		t.Errorf("transport-message-type = %q, want 1", mt)
	}
	net1, ok := getChild(t, action, "net")
	if !ok {
		t.Fatal("net child missing")
	}
	if proto, _ := attrString(net1, "protocol"); proto != "0" {
		t.Errorf("net protocol = %q, want 0", proto)
	}

	t9type := "9"
	t9 := BuildTransport(&TransportParams{CallID: "CID", To: &peer, CallCreator: &creator, TransportMessageType: &t9type})
	net9, _ := getChild(t, contentNodes(t, t9)[0], "net")
	if _, has := net9.Attrs["protocol"]; has {
		t.Error("type 9 net must not carry a protocol attr")
	}
}

// TestRelayLatencyEncodingAndHeartbeat checks latency encoding + heartbeat addressing.
func TestRelayLatencyEncodingAndHeartbeat(t *testing.T) {
	t.Skip("blocked: signaling/stanza bodies are stubs; enable when implemented")

	if got := EncodeLatency(45); got != "33554477" {
		t.Errorf("EncodeLatency(45) = %q, want 33554477", got)
	}
	peer, creator := peerJID(), creatorJID()
	rl := BuildRelayLatency(&RelayLatencyParams{
		CallID: "CID", To: &peer, CallCreator: &creator,
		LatencyMs: 45, RelayName: "gru1c02", AddressBytes: []byte{1, 2, 3, 4, 5, 6}, Devices: []types.JID{peer},
	})
	action := contentNodes(t, rl)[0]
	te, ok := getChild(t, action, "te")
	if !ok {
		t.Fatal("te child missing")
	}
	if lat, _ := attrString(te, "latency"); lat != "33554477" {
		t.Errorf("te latency = %q", lat)
	}
	if rn, _ := attrString(te, "relay_name"); rn != "gru1c02" {
		t.Errorf("te relay_name = %q", rn)
	}
	if _, ok := getChild(t, action, "destination"); !ok {
		t.Error("destination missing")
	}

	hb := BuildHeartbeat("CALLID", &creator, "DEADBEEF")
	if to, _ := attrString(hb, "to"); to != "CALLID@call" {
		t.Errorf("heartbeat to = %q, want CALLID@call", to)
	}
	if id, _ := attrString(hb, "id"); id != "DEADBEEF" {
		t.Errorf("heartbeat id = %q, want DEADBEEF", id)
	}
}

// TestTerminateWithTargets checks the reason attr and target destination.
func TestTerminateWithTargets(t *testing.T) {
	t.Skip("blocked: signaling/stanza bodies are stubs; enable when implemented")

	peer, creator := peerJID(), creatorJID()
	reason := "accepted_elsewhere"
	term := BuildTerminate(&TerminateParams{
		CallID: "CID", To: &peer, CallCreator: &creator, Reason: &reason, TargetDevices: []types.JID{peer},
	})
	action := contentNodes(t, term)[0]
	if r, _ := attrString(action, "reason"); r != "accepted_elsewhere" {
		t.Errorf("reason = %q", r)
	}
	if _, ok := getChild(t, action, "destination"); !ok {
		t.Error("destination missing")
	}
}
