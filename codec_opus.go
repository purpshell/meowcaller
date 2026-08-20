package meowcaller

import (
	"errors"
	"fmt"
	"math"

	"github.com/pion/opus"
)

const (
	opusSampleRate     = 16000
	opusFrameSamples   = 960
	opusBitrate        = 24000
	opusComplexity     = 5
	opusMaxPacketBytes = 1275
)

// opusEncoder uses Pion's pure-Go RFC 6716 encoder directly: one 960-sample
// SILK/WB 60 ms frame per Encode call produces one SILK-only Opus packet for
// WhatsApp's PT120 profile, with no multi-packet repackaging in this layer.
type opusEncoder struct {
	encoder *opus.Encoder
	pcm     [opusFrameSamples]int16
	packet  [opusMaxPacketBytes + 1]byte
}

func (e *opusEncoder) Encode(pcm []float32) ([]byte, error) {
	if len(pcm) != opusFrameSamples {
		return nil, fmt.Errorf("opus encoder: got %d samples, want %d", len(pcm), opusFrameSamples)
	}
	for sampleIndex, sample := range pcm {
		e.pcm[sampleIndex] = normalizedFloat32ToInt16(sample)
	}

	n, err := e.encoder.EncodeSILK(e.pcm[:], opus.BandwidthWideband, e.packet[:])
	if err != nil {
		return nil, fmt.Errorf("opus encode 60 ms SILK frame: %w", err)
	}
	if n > opusMaxPacketBytes {
		return nil, fmt.Errorf("opus packet: got %d bytes, maximum %d", n, opusMaxPacketBytes)
	}
	packet := make([]byte, n)
	copy(packet, e.packet[:n])

	return packet, nil
}

func (e *opusEncoder) Close() error {
	return nil
}

// opusDecoder decodes one raw RFC 6716 packet into 16 kHz mono PCM.
type opusDecoder struct {
	decoder opus.Decoder
}

func (d *opusDecoder) Decode(payload []byte) ([]float32, error) {
	if len(payload) == 0 {
		return nil, errors.New("opus decoder: empty packet")
	}
	if len(payload) > opusMaxPacketBytes {
		return nil, fmt.Errorf("opus decoder: packet has %d bytes, maximum %d", len(payload), opusMaxPacketBytes)
	}

	pcm := make([]float32, opusFrameSamples)
	n, err := d.decoder.DecodeToFloat32(payload, pcm)
	if err != nil {
		return nil, fmt.Errorf("opus decode: %w", err)
	}
	if n != opusFrameSamples {
		return nil, fmt.Errorf("opus decoder: got %d samples, want %d", n, opusFrameSamples)
	}
	return pcm, nil
}

func (d *opusDecoder) Close() error {
	return nil
}

func buildEncoderOpus() (audioEncoder, error) {
	encoder, err := opus.NewEncoder(
		opus.WithApplication(opus.ApplicationVoIP),
		opus.WithBitrate(opusBitrate),
		opus.WithComplexity(opusComplexity),
	)
	if err != nil {
		return nil, fmt.Errorf("create pure-Go Opus encoder: %w", err)
	}
	return &opusEncoder{encoder: encoder}, nil
}

func buildDecoderOpus() (decoderFactory, error) {
	return func() (audioDecoder, error) {
		decoder, err := opus.NewDecoderWithOutput(opusSampleRate, 1)
		if err != nil {
			return nil, fmt.Errorf("create pure-Go Opus decoder: %w", err)
		}
		return &opusDecoder{decoder: decoder}, nil
	}, nil
}

func normalizedFloat32ToInt16(sample float32) int16 {
	switch {
	case sample >= 1:
		return math.MaxInt16
	case sample <= -1:
		return math.MinInt16
	default:
		return int16(math.Round(float64(sample * 32768)))
	}
}
