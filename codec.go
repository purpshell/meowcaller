package meowcaller

import (
	"errors"

	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
)

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

// selectAudioCodec maps parsed voip_settings to the codec the media loop should
// use. Absent settings or an explicit MLow selection keep the call on MLow; only
// use_mlow_codec_v1=false switches it to Opus.
func selectAudioCodec(vs *signaling.VoipSettings) AudioCodec {
	if vs == nil || !vs.Present || vs.UseMlowCodecV1 {
		return AudioCodecMlow
	}
	return AudioCodecOpus
}

// audioEncoder encodes a single 16 kHz mono PCM frame into a codec payload.
type audioEncoder interface {
	Encode(pcm []float32) ([]byte, error)
	Close() error
}

// audioDecoder decodes a codec payload into a 16 kHz mono PCM frame.
type audioDecoder interface {
	Decode(payload []byte) ([]float32, error)
	Close() error
}

// decoderFactory produces a fresh audioDecoder for each receive stream.
type decoderFactory func() (audioDecoder, error)

// errUnknownCodec is the typed error returned for an unrecognized codec identifier.
var errUnknownCodec = errors.New("unknown audio codec")

// mlowEncoder adapts the package-specific MlowEncoder to audioEncoder.
type mlowEncoder struct {
	*mlow.MlowEncoder
}

func (e *mlowEncoder) Encode(pcm []float32) ([]byte, error) {
	return e.MlowEncoder.Encode(pcm)
}

func (e *mlowEncoder) Close() error {
	return nil
}

// mlowDecoder adapts the package-specific MlowDecoder to audioDecoder.
type mlowDecoder struct {
	*mlow.MlowDecoder
}

func (d *mlowDecoder) Decode(payload []byte) ([]float32, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty MLow payload")
	}
	return d.MlowDecoder.Decode(payload), nil
}

func (d *mlowDecoder) Close() error {
	return nil
}

// buildEncoder returns an encoder for the negotiated codec.
func buildEncoder(codec AudioCodec, log zerolog.Logger) (audioEncoder, error) {
	switch codec {
	case AudioCodecMlow:
		return &mlowEncoder{MlowEncoder: mlow.NewMlowEncoder()}, nil
	case AudioCodecOpus:
		return buildEncoderOpus(log)
	default:
		return nil, errUnknownCodec
	}
}

// buildDecoder returns a decoder factory for the negotiated codec.
func buildDecoder(codec AudioCodec, log zerolog.Logger) (decoderFactory, error) {
	switch codec {
	case AudioCodecMlow:
		return func() (audioDecoder, error) {
			return &mlowDecoder{MlowDecoder: mlow.NewMlowDecoder()}, nil
		}, nil
	case AudioCodecOpus:
		return buildDecoderOpus(log)
	default:
		return nil, errUnknownCodec
	}
}
