package signaling

import (
	"bytes"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
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
	if !bytes.Equal(gotCapability, CapabilityVideoOffer) {
		t.Errorf("video offer capability = %x, want %x", gotCapability, CapabilityVideoOffer)
	}
}

// TestAcceptAdvertisesVideo checks the accept carries a <video> child when requested.
func TestAcceptAdvertisesVideo(t *testing.T) {
	peer, creator := peerJID(), creatorJID()
	accept := BuildAccept(&AcceptParams{
		CallID: "CID", To: peer, CallCreator: creator,
		AudioRates: []string{"16000"}, Video: true,
	})
	tags := childTags(t, accept)
	hasVideo := false
	for _, tg := range tags {
		if tg == "video" {
			hasVideo = true
		}
	}
	if !hasVideo {
		t.Errorf("accept tags = %v, want a video child", tags)
	}
	video, ok := getChild(t, contentNodes(t, accept)[0], "video")
	if !ok {
		t.Fatal("video accept child missing")
	}
	if dec, _ := attrString(video, "dec"); dec != "H264" {
		t.Errorf("video accept dec = %q, want H264", dec)
	}
	if orientation, _ := attrString(video, "device_orientation"); orientation != "0" {
		t.Errorf("video accept device_orientation = %q, want 0", orientation)
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
