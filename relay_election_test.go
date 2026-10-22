package meowcaller

import "testing"

func TestInboundMediaRelaySelection(t *testing.T) {
	rd := &relayData{endpoints: []relayEndpoint{
		{relayName: "own", authTokenID: 1, c2rRTT: 20, addresses: []relayAddress{{ipv4: "1.1.1.1", port: 3478}}},
		{relayName: "far", authTokenID: 0, c2rRTT: 150, addresses: []relayAddress{{ipv4: "2.2.2.2", port: 3478}}},
		{relayName: "near", authTokenID: 0, c2rRTT: 40, addresses: []relayAddress{{ipv4: "3.3.3.3", port: 3478}}},
	}}
	if got := getMediaRelayEndpoint(rd, true).relayName; got != "near" {
		t.Fatalf("inbound without is_fna and probes: got %s, want near (foreign, lowest c2r_rtt)", got)
	}
	rd.electedRelay = "far"
	if got := getMediaRelayEndpoint(rd, true).relayName; got != "far" {
		t.Fatalf("inbound steered to the elected relay: got %s, want far", got)
	}
	rd.endpoints[1].isFNA = true
	rd.endpoints[1].relayName = "fna"
	if got := getMediaRelayEndpoint(rd, true).relayName; got != "fna" {
		t.Fatalf("inbound with is_fna: got %s, want fna", got)
	}
	if got := getMediaRelayEndpoint(rd, false).relayName; got != "own" {
		t.Fatalf("outbound: got %s, want own", got)
	}
}

func TestSteerInboundRelay(t *testing.T) {
	rd := &relayData{
		relayTokens: [][]byte{{1}, {2}},
		endpoints: []relayEndpoint{
			{relayName: "waw2c02", tokenID: 1},
			{relayName: "hel3c01", tokenID: 0},
		},
	}
	probes := []rlProbe{{relayName: "fala3c03", latency: 5}, {relayName: "waw2c02", latency: 75}, {relayName: "hel3c01", latency: 83}}
	// The caller's own relay is not offered to us: steer to the closest relay we can allocate on.
	if got := steerInboundRelay(rd, probes); got != "waw2c02" {
		t.Fatalf("steer = %q, want waw2c02", got)
	}
	// A relay without a token is not a target.
	rd.relayTokens[1] = nil
	if got := steerInboundRelay(rd, probes); got != "hel3c01" {
		t.Fatalf("steer without waw token = %q, want hel3c01", got)
	}
	// is_fna wins.
	rd.endpoints = append(rd.endpoints, relayEndpoint{relayName: "fna1", isFNA: true})
	if got := steerInboundRelay(rd, probes); got != "fna1" {
		t.Fatalf("steer with is_fna = %q, want fna1", got)
	}
	// Probes naming none of our relays: no steering (fall back to echoing).
	if got := steerInboundRelay(&relayData{endpoints: []relayEndpoint{{relayName: "x"}}}, probes); got != "" {
		t.Fatalf("steer = %q, want empty", got)
	}
}

func TestDecodeLatencyTags(t *testing.T) {
	cases := map[string]uint32{"33554521": 89, "100663423": 127, "100663333": 37, "12": 0, "x": 0}
	for in, want := range cases {
		if got := decodeLatency(in); got != want {
			t.Fatalf("decodeLatency(%s) = %d, want %d", in, got, want)
		}
	}
}
