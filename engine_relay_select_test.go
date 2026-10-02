package meowcaller

import (
	"testing"

	waBinary "github.com/polymorfa/hypermeow/binary"
)

func te2(name, authTokenID, c2rRTT, isFNA string, ip [4]byte) waBinary.Node {
	attrs := waBinary.Attrs{"relay_name": name, "relay_id": "1", "token_id": "0", "auth_token_id": authTokenID}
	if c2rRTT != "" {
		attrs["c2r_rtt"] = c2rRTT
	}
	if isFNA != "" {
		attrs["is_fna"] = isFNA
	}
	return waBinary.Node{Tag: "te2", Attrs: attrs, Content: []byte{ip[0], ip[1], ip[2], ip[3], 0x0d, 0x96}}
}

func TestInboundRelayWithoutFNAPicksLowestRTTPeerRelay(t *testing.T) {
	rd := parseRelayData(&waBinary.Node{Tag: "relay", Content: []waBinary.Node{
		te2("own", "1", "10", "", [4]byte{10, 0, 0, 1}),
		te2("peer-far", "0", "80", "", [4]byte{10, 0, 0, 2}),
		te2("peer-near", "0", "25", "", [4]byte{10, 0, 0, 3}),
		te2("peer-unmeasured", "0", "", "", [4]byte{10, 0, 0, 4}),
	}})

	if got := getMediaRelayEndpoint(rd, true); got == nil || got.relayName != "peer-near" {
		t.Fatalf("inbound endpoint = %+v, want peer-near", got)
	}
	if got := getMediaRelayEndpoint(rd, false); got == nil || got.relayName != "own" {
		t.Fatalf("outbound endpoint = %+v, want own", got)
	}
}

func TestInboundRelayPrefersFNA(t *testing.T) {
	rd := parseRelayData(&waBinary.Node{Tag: "relay", Content: []waBinary.Node{
		te2("peer-near", "0", "5", "", [4]byte{10, 0, 0, 3}),
		te2("fna", "0", "90", "1", [4]byte{10, 0, 0, 5}),
	}})

	if got := getMediaRelayEndpoint(rd, true); got == nil || got.relayName != "fna" {
		t.Fatalf("inbound endpoint = %+v, want fna", got)
	}
}

func TestInboundRelayWithoutRTTFallsBackToOwnRelay(t *testing.T) {
	rd := parseRelayData(&waBinary.Node{Tag: "relay", Content: []waBinary.Node{
		te2("peer", "0", "", "", [4]byte{10, 0, 0, 2}),
		te2("own", "1", "", "", [4]byte{10, 0, 0, 1}),
	}})

	if got := getMediaRelayEndpoint(rd, true); got == nil || got.relayName != "own" {
		t.Fatalf("inbound endpoint = %+v, want own", got)
	}
}
