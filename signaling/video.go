package signaling

import (
	"strconv"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Video call signaling follows the 1:1 video lifecycle implemented by whatsapp-rust.
// A from-start video call advertises a <video> child in the offer and accept. Mid-call
// upgrades and downgrades use standalone <video state=N> call stanzas.

// VideoCodecH264 is the lowercase codec token used by bridge internals.
const VideoCodecH264 = "h264"

// VideoStateDecH264 is the active standalone <video> state dec value observed in
// WhatsApp video negotiation.
const VideoStateDecH264 = "H264"

const (
	VideoDecRequest = "H264"
	VideoDecAccept  = "H264,AV1"
)

const (
	// Captured iPhone offers use this spelling. The tested iPhone receipts but does
	// not answer initial offers using h264/h264 or the legacy orientation attribute.
	videoOfferEncH264 = "h.264"
	videoOfferDecH264 = VideoStateDecH264
)

// WhatsApp's in-call video transition states.
const (
	VideoStateDisabled         = 0
	VideoStateEnabled          = 1
	VideoStateUpgradeRequest   = 3
	VideoStateUpgradeAccept    = 4
	VideoStateUpgradeReject    = 5
	VideoStateStopped          = 6
	VideoStateUpgradeCancel    = 8
	VideoStateUpgradeRequestV2 = 11

	// Backward-compatible names used by existing bridge integrations.
	VideoStateActive  = VideoStateEnabled
	VideoStateUpgrade = VideoStateUpgradeRequestV2
)

type VideoStateParams struct {
	CallID            string
	To                types.JID
	CallCreator       types.JID
	WrapperID         string
	State             int
	Dec               string
	DeviceOrientation *int
}

func BuildVideoStateWithParams(params VideoStateParams) waBinary.Node {
	attrs := waBinary.Attrs{
		"call-id":      params.CallID,
		"call-creator": params.CallCreator,
		"state":        strconv.Itoa(params.State),
	}
	if params.Dec != "" {
		attrs["dec"] = params.Dec
	}
	if params.State == VideoStateUpgradeRequestV2 {
		attrs["voip_settings"] = "video"
	}
	if params.DeviceOrientation != nil {
		attrs["device_orientation"] = strconv.Itoa(*params.DeviceOrientation)
	}
	video := waBinary.Node{Tag: "video", Attrs: attrs}
	return waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{"to": params.To, "id": params.WrapperID}, Content: []waBinary.Node{video}}
}

// BuildVideoState builds an outbound standalone <call><video …/></call> state stanza — used
// to signal our own video on/off + orientation (e.g. to accept a mid-call video upgrade).
// dec is included when non-empty.
func BuildVideoState(callID string, to, callCreator types.JID, wrapperID string, state, deviceOrientation int, dec string, log ...zerolog.Logger) waBinary.Node {
	return BuildVideoStateWithParams(VideoStateParams{
		CallID: callID, To: to, CallCreator: callCreator, WrapperID: wrapperID,
		State: state, Dec: dec, DeviceOrientation: &deviceOrientation,
	})
}

// BuildVideoAck creates the typed acknowledgement required for an in-call video
// transition and preserves companion-device routing metadata from the original stanza.
func BuildVideoAck(original *waBinary.Node) (waBinary.Node, bool) {
	if original == nil {
		return waBinary.Node{}, false
	}
	ag := original.AttrGetter()
	id := ag.String("id")
	from := ag.JID("from")
	if id == "" || from.IsEmpty() {
		return waBinary.Node{}, false
	}
	attrs := waBinary.Attrs{"class": "call", "id": id, "to": from, "type": "video"}
	if participant := ag.JID("participant"); !participant.IsEmpty() && participant != from {
		attrs["participant"] = participant
	}
	if recipient := ag.JID("recipient"); !recipient.IsEmpty() {
		attrs["recipient"] = recipient
	}
	return waBinary.Node{Tag: "ack", Attrs: attrs}, true
}

// videoOfferNode builds the <video> advertisement for an <offer> (sits after the
// <audio> children, before <net>).
func videoOfferNode() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"enc":                videoOfferEncH264,
		"dec":                videoOfferDecH264,
		"screen_width":       "1920",
		"screen_height":      "1080",
		"device_orientation": "0",
	}}
}

// videoAcceptNode builds the <video> advertisement for an <accept>.
func videoAcceptNode() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"dec":                VideoStateDecH264,
		"device_orientation": "0",
	}}
}

// videoPreacceptNode advertises the callee's decoder before the final accept.
func videoPreacceptNode() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"dec":                VideoStateDecH264,
		"device_orientation": "0",
		"screen_width":       "0",
		"screen_height":      "0",
	}}
}

// OfferHasVideo reports whether a parsed <offer> node advertises video — the presence of
// a <video> child marks an inbound video call.
func OfferHasVideo(offer *waBinary.Node) bool {
	if offer == nil {
		return false
	}
	for _, c := range offer.GetChildren() {
		if c.Tag == "video" {
			return true
		}
	}
	return false
}
