package meowcaller

// AudioCodec identifies the wire audio codec negotiated for a call. WhatsApp 1:1
// audio is carried in RTP payload type 120 regardless of codec; the codec itself
// is chosen by signaling (the server's voip_settings), not the RTP payload type.
type AudioCodec int8

const (
	// AudioCodecMlow is Meta's 16 kHz MLow codec (the default).
	AudioCodecMlow AudioCodec = iota
	// AudioCodecOpus is RFC 6716 Opus.
	AudioCodecOpus
)

// String renders the codec name for logs.
func (c AudioCodec) String() string {
	switch c {
	case AudioCodecOpus:
		return "opus"
	default:
		return "mlow"
	}
}
