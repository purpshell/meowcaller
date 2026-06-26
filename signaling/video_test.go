package signaling

import (
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
