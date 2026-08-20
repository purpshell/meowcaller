package meowcaller

import (
	"errors"
	"fmt"
	"math"

	"github.com/pion/opus"
	"github.com/rs/zerolog"
)

const (
	opusSampleRate         = 16000
	opusFrameSamples       = 960
	opusSubframeSamples    = 320
	opusSubframes          = opusFrameSamples / opusSubframeSamples
	opusBitrate            = 24000
	opusComplexity         = 5
	opusMaxFrameBytes      = 1275
	opusMaxPacketBytes     = 1275
	opusFrameCodeMask      = 0x03
	opusFrameCodeOne       = 0x00
	opusFrameCodeArbitrary = 0x03
)

// opusEncoder uses Pion's pure-Go RFC 6716 encoder. Pion currently exposes
// SILK/WB encoding in 20 ms units, so three consecutive frames are combined
// into one standard 60 ms Code 3 VBR packet for WhatsApp's PT120 profile.
type opusEncoder struct {
	encoder *opus.Encoder
	pcm     [opusSubframeSamples]int16
	packet  [opusMaxFrameBytes + 1]byte
}

func (e *opusEncoder) Encode(pcm []float32) ([]byte, error) {
	if len(pcm) != opusFrameSamples {
		return nil, fmt.Errorf("opus encoder: got %d samples, want %d", len(pcm), opusFrameSamples)
	}

	frames := make([][]byte, 0, opusSubframes)
	for frameIndex := range opusSubframes {
		start := frameIndex * opusSubframeSamples
		for sampleIndex, sample := range pcm[start : start+opusSubframeSamples] {
			e.pcm[sampleIndex] = normalizedFloat32ToInt16(sample)
		}
		n, err := e.encoder.EncodeSILK(e.pcm[:], opus.BandwidthWideband, e.packet[:])
		if err != nil {
			return nil, fmt.Errorf("opus encode 20 ms frame %d: %w", frameIndex, err)
		}
		frame := append([]byte(nil), e.packet[:n]...)
		frames = append(frames, frame)
	}

	packet, err := repacketizeOpusVBR(frames)
	if err != nil {
		return nil, fmt.Errorf("opus repacketize 60 ms: %w", err)
	}
	if len(packet) > opusMaxPacketBytes {
		return nil, fmt.Errorf("opus packet: got %d bytes, maximum %d", len(packet), opusMaxPacketBytes)
	}
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

func buildEncoderOpus(_ zerolog.Logger) (audioEncoder, error) {
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

func buildDecoderOpus(_ zerolog.Logger) (decoderFactory, error) {
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

// repacketizeOpusVBR combines single-frame packets that share one Opus TOC
// configuration into an RFC 6716 Code 3 VBR packet. The final frame length is
// implicit; preceding lengths use the one- or two-byte RFC representation.
func repacketizeOpusVBR(packets [][]byte) ([]byte, error) {
	if len(packets) == 0 || len(packets) > 63 {
		return nil, fmt.Errorf("invalid frame count %d", len(packets))
	}

	var toc byte
	frames := make([][]byte, len(packets))
	lengthBytes := 0
	for i, packet := range packets {
		if len(packet) < 2 {
			return nil, fmt.Errorf("frame %d is too short", i)
		}
		if packet[0]&opusFrameCodeMask != opusFrameCodeOne {
			return nil, fmt.Errorf("frame %d is not a single-frame packet", i)
		}
		frameTOC := packet[0] &^ opusFrameCodeMask
		if i == 0 {
			toc = frameTOC
		} else if frameTOC != toc {
			return nil, fmt.Errorf("frame %d TOC %#02x differs from %#02x", i, frameTOC, toc)
		}
		frames[i] = packet[1:]
		if len(frames[i]) > opusMaxFrameBytes {
			return nil, fmt.Errorf("frame %d has %d bytes, maximum %d", i, len(frames[i]), opusMaxFrameBytes)
		}
		if i < len(packets)-1 {
			lengthBytes += opusFrameLengthBytes(len(frames[i]))
		}
	}

	size := 2 + lengthBytes
	for _, frame := range frames {
		size += len(frame)
	}
	out := make([]byte, 0, size)
	out = append(out, toc|opusFrameCodeArbitrary, 0x80|byte(len(frames)))
	for i := 0; i < len(frames)-1; i++ {
		out = appendOpusFrameLength(out, len(frames[i]))
	}
	for _, frame := range frames {
		out = append(out, frame...)
	}
	return out, nil
}

func opusFrameLengthBytes(length int) int {
	if length < 252 {
		return 1
	}
	return 2
}

func appendOpusFrameLength(dst []byte, length int) []byte {
	if length < 252 {
		return append(dst, byte(length))
	}
	first := 252 + length%4
	return append(dst, byte(first), byte((length-first)/4))
}
