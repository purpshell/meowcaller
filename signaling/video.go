package signaling

import (
	"strconv"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Video call signaling, ported from WaCalls (jotadev66, MIT) feat/video-calls and
// adjusted against captured WhatsApp iPhone video-call signaling. A 1:1 video call
// advertises a <video> child in the <offer>/<accept>, and an inbound video call is
// detected by the presence of that child. See README Credits.

// VideoCodecH264 is the lowercase codec token used by bridge internals.
const VideoCodecH264 = "h264"

// VideoStateDecH264 is the active standalone <video> state dec value observed in
// WhatsApp video negotiation.
const VideoStateDecH264 = "H264"

const (
	// Captured iPhone offers use this spelling in the initial <offer>/<accept>
	// negotiation. The callee receipts but does not answer offers that use h264/h264.
	videoNegotiationEncH264 = "h.264"
	videoNegotiationDecH264 = VideoStateDecH264
)

// Observed <video> "state" values. 1 = active (video on) and 11 = mid-call upgrade (carries
// the inline media payload); the intermediate setup values (2/4/6) are unconfirmed.
const (
	VideoStateActive  = 1
	VideoStateUpgrade = 11
)

// BuildVideoState builds an outbound standalone <call><video …/></call> state stanza — used
// to signal our own video on/off + orientation (e.g. to accept a mid-call video upgrade).
// dec is included when non-empty.
func BuildVideoState(callID string, to, callCreator types.JID, wrapperID string, state, deviceOrientation int, dec string, log ...zerolog.Logger) waBinary.Node {
	attrs := waBinary.Attrs{
		"call-id":            callID,
		"call-creator":       callCreator,
		"state":              strconv.Itoa(state),
		"device_orientation": strconv.Itoa(deviceOrientation),
	}
	if dec != "" {
		attrs["dec"] = dec
	}
	video := waBinary.Node{Tag: "video", Attrs: attrs}
	return waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{"to": to, "id": wrapperID}, Content: []waBinary.Node{video}}
}

// videoOfferNode builds the <video> advertisement for an <offer> (sits after the
// <audio> children, before <net>).
func videoOfferNode() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"enc":                videoNegotiationEncH264,
		"dec":                videoNegotiationDecH264,
		"screen_width":       "1920",
		"screen_height":      "1080",
		"device_orientation": "0",
	}}
}

// videoAcceptNode builds the <video> advertisement for an <accept>.
func videoAcceptNode() waBinary.Node {
	return waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{
		"dec":                videoNegotiationDecH264,
		"device_orientation": "0",
	}}
}

// OfferHasVideo reports whether a parsed <offer> node advertises video — the presence of
// a <video> child marks an inbound video call.
func OfferHasVideo(offer *waBinary.Node) bool {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/helpers.go#L11-L18
	//                  https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/callmanager_signaling.go#L24
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
