package signaling

import (
	"bytes"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// TestOfferAdvertisesVideo checks the <video> child lands after the audios, before <net>.
func TestOfferAdvertisesVideo(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	dk := OfferDeviceKey{DeviceJid: peer, Ciphertext: []byte{1}, EncType: "pkmsg"}
	call := BuildOffer(&OfferParams{
		CallID: "CID", To: peer, CallCreator: creator,
		DeviceKeys: []OfferDeviceKey{dk}, Capability: CapabilityOffer, Video: true,
	})
	want := []string{"audio", "audio", "video", "net", "capability", "enc", "encopt"}
	if got := childTags(t, call); !eqTags(got, want) {
		t.Errorf("video offer child tags = %v, want %v", got, want)
	}
	offer := contentNodes(t, call)[0]
	video, ok := getChild(t, offer, "video")
	if !ok {
		t.Fatal("video offer child missing")
	}
	if enc, _ := attrString(video, "enc"); enc != "h.264" {
		t.Errorf("video offer enc = %q, want h.264", enc)
	}
	if dec, _ := attrString(video, "dec"); dec != "H264" {
		t.Errorf("video offer dec = %q, want H264", dec)
	}
	if _, has := video.Attrs["orientation"]; has {
		t.Error("video offer must not carry legacy orientation attr")
	}
	capability, ok := getChild(t, offer, "capability")
	if !ok {
		t.Fatal("video offer capability child missing")
	}
	gotCapability, ok := capability.Content.([]byte)
	if !ok {
		t.Fatalf("video offer capability content = %T, want []byte", capability.Content)
	}
	wantCapability := []byte{0x01, 0x05, 0xf7, 0x09, 0xe0, 0xfa, 0x13}
	if !bytes.Equal(gotCapability, wantCapability) {
		t.Errorf("video offer capability = %x, want %x", gotCapability, wantCapability)
	}
}

// TestFromStartVideoAcceptMatchesCapturedCalleeShape pins the complete captured answer.
func TestFromStartVideoAcceptMatchesCapturedCalleeShape(t *testing.T) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/d37b1756d05fb34c9b6c2410c48dd20d27394929/wacore/src/stanza/call.rs#L2119-L2134
	peer, creator := peerJID(), creatorJID()
	accept := BuildAccept(&AcceptParams{
		CallID: "CID", To: peer, WrapperID: "accept-wrap", CallCreator: creator,
		AudioRates: []string{"16000"},
		Metadata: waBinary.Attrs{
			"peer_abtest_bucket":         "bucket-a",
			"peer_abtest_bucket_id_list": "11,22",
		},
		Video: true,
	})
	want := []string{"audio", "video", "net", "encopt", "metadata"}
	if got := childTags(t, accept); !eqTags(got, want) {
		t.Errorf("accept tags = %v, want %v", got, want)
	}
	if id, _ := attrString(accept, "id"); id != "accept-wrap" {
		t.Errorf("accept wrapper id = %q, want accept-wrap", id)
	}
	video, ok := getChild(t, contentNodes(t, accept)[0], "video")
	if !ok {
		t.Fatal("from-start video accept omitted the callee video marker")
	}
	if dec, _ := attrString(video, "dec"); dec != VideoStateDecH264 {
		t.Errorf("video accept dec = %q, want %s", dec, VideoStateDecH264)
	}
	if orientation, _ := attrString(video, "device_orientation"); orientation != "0" {
		t.Errorf("video accept device_orientation = %q, want 0", orientation)
	}
	if _, has := video.Attrs["enc"]; has {
		t.Error("video accept must not carry an encoder attribute")
	}
	action := contentNodes(t, accept)[0]
	metadata, ok := getChild(t, action, "metadata")
	if !ok {
		t.Fatal("video accept metadata missing")
	}
	if got, _ := attrString(metadata, "peer_abtest_bucket"); got != "bucket-a" {
		t.Errorf("peer_abtest_bucket = %q, want bucket-a", got)
	}
	if got, _ := attrString(metadata, "peer_abtest_bucket_id_list"); got != "11,22" {
		t.Errorf("peer_abtest_bucket_id_list = %q, want 11,22", got)
	}
	if _, ok := getChild(t, action, "te"); ok {
		t.Fatal("from-start video accept unexpectedly echoed a relay endpoint")
	}
	if _, ok := getChild(t, action, "capability"); ok {
		t.Fatal("from-start video accept unexpectedly advertised a capability")
	}
}

func TestVideoPreacceptMatchesCapturedCalleeShape(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	pre := BuildPreaccept("CID", peer, creator, "wrap", []string{"16000"}, true)

	want := []string{"audio", "video", "encopt", "capability"}
	if got := childTags(t, pre); !eqTags(got, want) {
		t.Errorf("video preaccept tags = %v, want %v", got, want)
	}
	action := contentNodes(t, pre)[0]
	video, ok := getChild(t, action, "video")
	if !ok {
		t.Fatal("video preaccept child missing")
	}
	for attr, wantValue := range map[string]string{
		"dec":                "H264",
		"device_orientation": "0",
		"screen_width":       "0",
		"screen_height":      "0",
	} {
		if got, _ := attrString(video, attr); got != wantValue {
			t.Errorf("video preaccept %s = %q, want %q", attr, got, wantValue)
		}
	}
	capability, ok := getChild(t, action, "capability")
	if !ok {
		t.Fatal("video preaccept capability missing")
	}
	gotCapability, ok := capability.Content.([]byte)
	if !ok {
		t.Fatalf("video preaccept capability content = %T, want []byte", capability.Content)
	}
	wantCapability := []byte{0x01, 0x05, 0xf7, 0x09, 0xe0, 0xbb, 0x13}
	if !bytes.Equal(gotCapability, wantCapability) {
		t.Errorf("video preaccept capability = %x, want %x", gotCapability, wantCapability)
	}
}

func TestVoicePreacceptKeepsAudioOnlyCapability(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	pre := BuildPreaccept("CID", peer, creator, "wrap", []string{"16000"}, false)
	action := contentNodes(t, pre)[0]
	if _, ok := getChild(t, action, "video"); ok {
		t.Fatal("voice preaccept unexpectedly contains video")
	}
	capability, ok := getChild(t, action, "capability")
	if !ok {
		t.Fatal("voice preaccept capability missing")
	}
	want := []byte{0x01, 0x05, 0xf7, 0x09, 0xe0, 0xbb, 0x07}
	if got, ok := capability.Content.([]byte); !ok || !bytes.Equal(got, want) {
		t.Errorf("voice preaccept capability = %x, want %x", got, want)
	}
}

func TestVoiceOfferAndAcceptUseCapturedCapabilityWithoutVideo(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	dk := OfferDeviceKey{DeviceJid: peer, Ciphertext: []byte{1}, EncType: "pkmsg"}
	offer := BuildOffer(&OfferParams{
		CallID: "CID", To: peer, CallCreator: creator,
		DeviceKeys: []OfferDeviceKey{dk}, Capability: CapabilityOffer,
	})
	offerAction := contentNodes(t, offer)[0]
	if _, ok := getChild(t, offerAction, "video"); ok {
		t.Fatal("voice offer unexpectedly contains video")
	}
	assertCapabilityBytes(t, offerAction, []byte{0x01, 0x05, 0xf7, 0x09, 0xe0, 0xbb, 0x13})

	accept := BuildAccept(&AcceptParams{
		CallID: "CID", To: peer, WrapperID: "accept-wrap", CallCreator: creator,
		AudioRates: []string{"16000"}, Capability: CapabilityOffer,
	})
	acceptAction := contentNodes(t, accept)[0]
	if _, ok := getChild(t, acceptAction, "video"); ok {
		t.Fatal("voice accept unexpectedly contains video")
	}
	assertCapabilityBytes(t, acceptAction, []byte{0x01, 0x05, 0xf7, 0x09, 0xe0, 0xbb, 0x13})
}

func assertCapabilityBytes(t *testing.T, action waBinary.Node, want []byte) {
	t.Helper()
	capability, ok := getChild(t, action, "capability")
	if !ok {
		t.Fatal("capability child missing")
	}
	got, ok := capability.Content.([]byte)
	if !ok || !bytes.Equal(got, want) {
		t.Errorf("capability = %x, want %x", got, want)
	}
}

// TestOfferHasVideo checks inbound video detection by <video> child presence.
func TestOfferHasVideo(t *testing.T) {
	withVideo := waBinary.Node{Tag: "offer", Content: []waBinary.Node{
		{Tag: "audio"}, {Tag: "video"}, {Tag: "net"},
	}}
	if !OfferHasVideo(&withVideo) {
		t.Error("OfferHasVideo(with video) = false, want true")
	}
	audioOnly := waBinary.Node{Tag: "offer", Content: []waBinary.Node{{Tag: "audio"}, {Tag: "net"}}}
	if OfferHasVideo(&audioOnly) {
		t.Error("OfferHasVideo(audio-only) = true, want false")
	}
	if OfferHasVideo(nil) {
		t.Error("OfferHasVideo(nil) = true, want false")
	}
}

func TestVideoStateUsesNegotiatedDecoderList(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	call := BuildVideoState("CID", peer, creator, "wrap", VideoStateActive, 0, VideoStateDecH264)
	video, ok := getChild(t, call, "video")
	if !ok {
		t.Fatal("video state child missing")
	}
	if dec, _ := attrString(video, "dec"); dec != "H264" {
		t.Errorf("video state dec = %q, want H264", dec)
	}
}

func TestVideoUpgradeRequestMatchesWhatsAppWebShape(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	orientation := 0
	call := BuildVideoStateWithParams(VideoStateParams{
		CallID: "CID", To: peer, CallCreator: creator, WrapperID: "wrap",
		State: VideoStateUpgradeRequestV2, Dec: VideoDecRequest, DeviceOrientation: &orientation,
	})
	video, ok := getChild(t, call, "video")
	if !ok {
		t.Fatal("video state child missing")
	}
	if state, _ := attrString(video, "state"); state != "11" {
		t.Fatalf("state = %q, want 11", state)
	}
	if dec, _ := attrString(video, "dec"); dec != "H264" {
		t.Fatalf("dec = %q, want H264", dec)
	}
	if settings, _ := attrString(video, "voip_settings"); settings != "video" {
		t.Fatalf("voip_settings = %q, want video", settings)
	}
	if orientation, _ := attrString(video, "device_orientation"); orientation != "0" {
		t.Fatalf("device_orientation = %q, want 0", orientation)
	}
}

func TestVideoUpgradeAcceptThenEnabledShapes(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	accept := BuildVideoStateWithParams(VideoStateParams{
		CallID: "CID", To: peer, CallCreator: creator, WrapperID: "accept",
		State: VideoStateUpgradeAccept, Dec: VideoDecAccept,
	})
	video, ok := getChild(t, accept, "video")
	if !ok {
		t.Fatal("video accept child missing")
	}
	if state, _ := attrString(video, "state"); state != "4" {
		t.Fatalf("accept state = %q, want 4", state)
	}
	if dec, _ := attrString(video, "dec"); dec != "H264,AV1" {
		t.Fatalf("accept dec = %q, want H264,AV1", dec)
	}

	enabled := BuildVideoStateWithParams(VideoStateParams{
		CallID: "CID", To: peer, CallCreator: creator, WrapperID: "enabled",
		State: VideoStateEnabled,
	})
	video, ok = getChild(t, enabled, "video")
	if !ok {
		t.Fatal("video enabled child missing")
	}
	if state, _ := attrString(video, "state"); state != "1" {
		t.Fatalf("enabled state = %q, want 1", state)
	}
	if _, ok := video.Attrs["dec"]; ok {
		t.Fatal("enabled state unexpectedly includes dec")
	}
}

func TestVideoStoppedCarriesExplicitOrientation(t *testing.T) {
	orientation := 0
	call := BuildVideoStateWithParams(VideoStateParams{
		CallID: "CID", To: peerJID(), CallCreator: creatorJID(), WrapperID: "stop",
		State: VideoStateStopped, DeviceOrientation: &orientation,
	})
	video, ok := getChild(t, call, "video")
	if !ok {
		t.Fatal("video stopped child missing")
	}
	if state, _ := attrString(video, "state"); state != "6" {
		t.Fatalf("stopped state = %q, want 6", state)
	}
	if orientation, _ := attrString(video, "device_orientation"); orientation != "0" {
		t.Fatalf("device_orientation = %q, want 0", orientation)
	}
}

func TestVideoAckPreservesCompanionRouting(t *testing.T) {
	from := peerJID()
	participant := types.JID{User: "333333333333333", Server: types.HiddenUserServer}
	recipient := types.JID{User: "444444444444444", Server: types.HiddenUserServer}
	original := &waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{
		"id": "wrap", "from": from, "participant": participant, "recipient": recipient,
	}}
	ack, ok := BuildVideoAck(original)
	if !ok {
		t.Fatal("BuildVideoAck rejected a routable stanza")
	}
	if got := ack.AttrGetter().JID("participant"); got != participant {
		t.Fatalf("participant = %s, want %s", got, participant)
	}
	if got := ack.AttrGetter().JID("recipient"); got != recipient {
		t.Fatalf("recipient = %s, want %s", got, recipient)
	}
}
