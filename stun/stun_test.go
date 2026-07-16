package stun

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type stunKat struct {
	Inputs struct {
		SSRC uint32 `json:"ssrc"`
	} `json:"inputs"`
	Stun struct {
		TX              string `json:"tx"`
		RelayToken      string `json:"relayToken"`
		MiKey           string `json:"miKey"`
		Crc32Abc        uint32 `json:"crc32_abc"`
		AttrToken       string `json:"attr_token"`
		XorEndpoint     string `json:"xorEndpoint"`
		NativeSenderSub string `json:"nativeSenderSub"`
		MinimalMi       string `json:"minimalMi"`
		WithFp          string `json:"withFp"`
		WasmAllocate    string `json:"wasmAllocate"`
		Ping            string `json:"ping"`
	} `json:"stun"`
	StunProto struct {
		VoipSenderSubscriptions   string `json:"voip_sender_subscriptions"`
		ApkSenderSubscriptionsNo  string `json:"apk_sender_subscriptions_nopid"`
		ApkSenderSubscriptionsPid string `json:"apk_sender_subscriptions_pid"`
		ApkStreamDescriptors      string `json:"apk_stream_descriptors"`
	} `json:"stun_proto"`
}

func loadStunKat(t *testing.T) stunKat {
	t.Helper()
	raw, err := os.ReadFile("testdata/kats.json")
	if err != nil {
		t.Fatalf("read kats.json: %v", err)
	}
	var k stunKat
	if err := json.Unmarshal(raw, &k); err != nil {
		t.Fatalf("parse kats.json: %v", err)
	}
	return k
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func tx12(t *testing.T, k stunKat) [12]byte {
	t.Helper()
	var tx [12]byte
	copy(tx[:], mustHex(t, k.Stun.TX))
	return tx
}

// TestCrc32IsIEEE checks the FINGERPRINT CRC-32 against the kat and the IEEE constant.
func TestCrc32IsIEEE(t *testing.T) {
	k := loadStunKat(t)
	if got := stunFingerprint([]byte("abc")); uint32(got) != k.Stun.Crc32Abc {
		t.Errorf("crc32(abc) = %#x, want %#x", got, k.Stun.Crc32Abc)
	}
	if got := stunFingerprint([]byte("abc")); got != 0x352441c2 {
		t.Errorf("crc32(abc) = %#x, want 0x352441c2", got)
	}
}

// TestAttrAndEndpointMatchKAT checks attribute encoding, the XOR endpoint, and the
// native sender subscription.
func TestAttrAndEndpointMatchKAT(t *testing.T) {
	k := loadStunKat(t)
	token := mustHex(t, k.Stun.RelayToken)
	if got := hex.EncodeToString(stunAttr(attrRelayToken, token)); got != k.Stun.AttrToken {
		t.Errorf("attr_token = %s, want %s", got, k.Stun.AttrToken)
	}
	ep, ok := EncodeXorRelayEndpoint("157.240.226.133", 3478)
	if !ok {
		t.Fatal("EncodeXorRelayEndpoint returned ok=false")
	}
	if got := hex.EncodeToString(ep[:]); got != k.Stun.XorEndpoint {
		t.Errorf("xorEndpoint = %s, want %s", got, k.Stun.XorEndpoint)
	}
	sub := CreateNativeSenderSubscription(k.Inputs.SSRC)
	if got := hex.EncodeToString(sub[:]); got != k.Stun.NativeSenderSub {
		t.Errorf("nativeSenderSub = %s, want %s", got, k.Stun.NativeSenderSub)
	}
}

// TestEncodeRequestMIAndFingerprint checks the MI-only and MI+FINGERPRINT encodings.
func TestEncodeRequestMIAndFingerprint(t *testing.T) {
	k := loadStunKat(t)
	tx := tx12(t, k)
	attrs := stunAttr(attrRelayToken, mustHex(t, k.Stun.RelayToken))
	miKey := mustHex(t, k.Stun.MiKey)

	minimal := EncodeStunRequest(MsgAllocateRequest, tx, attrs, miKey, false)
	if got := hex.EncodeToString(minimal); got != k.Stun.MinimalMi {
		t.Errorf("minimalMi = %s, want %s", got, k.Stun.MinimalMi)
	}
	withFp := EncodeStunRequest(MsgAllocateRequest, tx, attrs, miKey, true)
	if got := hex.EncodeToString(withFp); got != k.Stun.WithFp {
		t.Errorf("withFp = %s, want %s", got, k.Stun.WithFp)
	}
}

// TestWasmAllocateAndPing checks the WASM allocate request and the ping.
func TestWasmAllocateAndPing(t *testing.T) {
	k := loadStunKat(t)
	tx := tx12(t, k)
	token := mustHex(t, k.Stun.RelayToken)
	miKey := mustHex(t, k.Stun.MiKey)
	ep, ok := EncodeXorRelayEndpoint("157.240.226.133", 3478)
	if !ok {
		t.Fatal("endpoint ok=false")
	}
	alloc := BuildWasmStunAllocateRequest(tx, token, ep, miKey)
	if got := hex.EncodeToString(alloc); got != k.Stun.WasmAllocate {
		t.Errorf("wasmAllocate = %s, want %s", got, k.Stun.WasmAllocate)
	}
	ping := BuildWhatsappPing(tx)
	if got := hex.EncodeToString(ping[:]); got != k.Stun.Ping {
		t.Errorf("ping = %s, want %s", got, k.Stun.Ping)
	}
}

func TestWasmStreamDescriptorsMatchCapturedTemplate(t *testing.T) {
	ssrcs := [9]uint32{
		1170300490,
		2781599269,
		4281963094,
		2798104311,
		3731645995,
		1364979034,
		2983933125,
		4140589437,
		2522729392,
	}
	if got := hex.EncodeToString(CreateWasmStreamDescriptors(ssrcs)); got != hex.EncodeToString(wasmStreamDescriptorsTemplate) {
		t.Errorf("wasm stream descriptors = %s, want %s", got, hex.EncodeToString(wasmStreamDescriptorsTemplate))
	}
}

// TestParseRoundTripsAttributes parses the minimal MI request back into attributes.
func TestParseRoundTripsAttributes(t *testing.T) {
	k := loadStunKat(t)
	minimal := mustHex(t, k.Stun.MinimalMi)
	if !IsStunPacket(minimal) {
		t.Fatal("minimal not classified as STUN packet")
	}
	if mt, ok := StunMessageType(minimal); !ok || mt != MsgAllocateRequest {
		t.Errorf("message type = (%#x, %v), want (%#x, true)", mt, ok, MsgAllocateRequest)
	}
	attrs := ParseStunAttributes(minimal)
	if len(attrs) != 2 {
		t.Fatalf("attr count = %d, want 2", len(attrs))
	}
	if attrs[0].AttrType != attrRelayToken {
		t.Errorf("attr[0].type = %#x, want %#x", attrs[0].AttrType, attrRelayToken)
	}
	if hex.EncodeToString(attrs[0].Value) != k.Stun.RelayToken {
		t.Errorf("attr[0].value = %x, want %s", attrs[0].Value, k.Stun.RelayToken)
	}
	if attrs[1].AttrType != attrMessageIntegrity || len(attrs[1].Value) != 20 {
		t.Errorf("attr[1] = (%#x, len %d), want (%#x, 20)", attrs[1].AttrType, len(attrs[1].Value), attrMessageIntegrity)
	}
}

// TestProtobufPayloadsMatchKAT checks the three protobuf subscription/descriptor blobs.
func TestProtobufPayloadsMatchKAT(t *testing.T) {
	k := loadStunKat(t)
	ssrc := k.Inputs.SSRC
	if got := hex.EncodeToString(CreateVoipSenderSubscriptions(ssrc)); got != k.StunProto.VoipSenderSubscriptions {
		t.Errorf("voip_sender_subscriptions = %s, want %s", got, k.StunProto.VoipSenderSubscriptions)
	}
	if got := hex.EncodeToString(CreateApkSenderSubscriptions(ssrc, nil)); got != k.StunProto.ApkSenderSubscriptionsNo {
		t.Errorf("apk_sender_subscriptions_nopid = %s, want %s", got, k.StunProto.ApkSenderSubscriptionsNo)
	}
	pid := uint32(7)
	if got := hex.EncodeToString(CreateApkSenderSubscriptions(ssrc, &pid)); got != k.StunProto.ApkSenderSubscriptionsPid {
		t.Errorf("apk_sender_subscriptions_pid = %s, want %s", got, k.StunProto.ApkSenderSubscriptionsPid)
	}
	if got := hex.EncodeToString(CreateApkStreamDescriptors(ssrc)); got != k.StunProto.ApkStreamDescriptors {
		t.Errorf("apk_stream_descriptors = %s, want %s", got, k.StunProto.ApkStreamDescriptors)
	}
}

// TestAndroidAllocateCarriesThreeAttrs checks the APK allocate carries the four attrs.
func TestAndroidAllocateCarriesThreeAttrs(t *testing.T) {
	k := loadStunKat(t)
	tx := tx12(t, k)
	token := mustHex(t, k.Stun.RelayToken)
	miKey := mustHex(t, k.Stun.MiKey)
	ssrc := k.Inputs.SSRC
	pkt := BuildAndroidStunAllocateRequest(tx, token, ssrc, nil, miKey, false)
	attrs := ParseStunAttributes(pkt)
	if len(attrs) != 4 {
		t.Fatalf("attr count = %d, want 4", len(attrs))
	}
	want := []uint16{attrRelayToken, attrSenderSubscriptionsV2, attrStreamDescriptors, attrMessageIntegrity}
	for i, w := range want {
		if attrs[i].AttrType != w {
			t.Errorf("attr[%d].type = %#x, want %#x", i, attrs[i].AttrType, w)
		}
	}
	if hex.EncodeToString(attrs[2].Value) != hex.EncodeToString(CreateApkStreamDescriptors(ssrc)) {
		t.Errorf("attr[2].value mismatch")
	}
}

// TestPongMatching checks pong classification with and without a transaction id.
func TestPongMatching(t *testing.T) {
	k := loadStunKat(t)
	tx := tx12(t, k)
	pong := BuildWhatsappPing(tx)
	binary.BigEndian.PutUint16(pong[0:2], MsgWhatsappPong)
	if !IsWhatsappPong(pong[:], tx[:]) {
		t.Error("pong with matching tx not recognized")
	}
	if !IsWhatsappPong(pong[:], nil) {
		t.Error("pong with nil tx not recognized")
	}
	var wrong [12]byte
	if IsWhatsappPong(pong[:], wrong[:]) {
		t.Error("pong with wrong tx falsely matched")
	}
}
