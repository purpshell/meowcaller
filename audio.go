package meowcaller

import (
	"encoding/binary"
	"errors"
	"io"
)

// Audio in meowcaller is 16 kHz mono float32 PCM carried in 60 ms frames — the rate
// and framing the MLow codec encodes. Every AudioSource and AudioSink speaks this
// format; the built-in decoders convert foreign formats (WAV/MP3/Opus/PCMU) into it.
const (
	// SampleRate is the codec's fixed sample rate (16 kHz mono).
	SampleRate = 16000
	// FrameSamples is the per-frame sample count (60 ms at 16 kHz).
	FrameSamples = 960
)

var errSourceClosed = errors.New("meowcaller: audio source closed")

// AudioSource yields successive 16 kHz mono PCM frames of FrameSamples to play into a
// call. ReadFrame returns io.EOF when the source is exhausted (a Player then fires
// OnFinish). Built-in sources decode WAV/MP3/Opus/PCMU/raw PCM; attach one to a call via a
// Player (Call.Subscribe / Call.Play).
type AudioSource interface {
	// ReadFrame returns the next FrameSamples-long mono frame, or io.EOF at the end.
	ReadFrame() ([]float32, error)
	// Close releases any decoder/file resources. Safe to call more than once.
	Close() error
}

// AudioSink consumes the 16 kHz mono PCM frames decoded from the peer's audio. Attach
// one with Call.Receive; built-ins record to a WAV file or forward to a callback. A
// CGO Speaker() sink lives in the meowcaller/audio/malgo subpackage.
type AudioSink interface {
	// WriteFrame consumes one decoded mono frame from the peer.
	WriteFrame(frame []float32) error
	// Close flushes and releases the sink. Safe to call more than once.
	Close() error
}

// SinkFunc adapts a plain function to an AudioSink (Close is a no-op).
type SinkFunc func(frame []float32)

// WriteFrame calls f.
func (f SinkFunc) WriteFrame(frame []float32) error { f(frame); return nil }

// Close is a no-op for SinkFunc.
func (f SinkFunc) Close() error { return nil }

// pcmS16Source plays raw signed-16-bit little-endian mono PCM at 16 kHz from r,
// chunked into FrameSamples frames (zero-padding the final partial frame). It is the
// substrate the WAV/MP3/Opus decoders feed once they have produced 16 kHz mono PCM.
type pcmS16Source struct {
	r   io.ReadCloser
	buf []byte
}

// muLawSource plays raw G.711 PCMU at 8 kHz mono, expanding each input sample to
// two 16 kHz samples so every 480-byte input block becomes one 60 ms call frame.
type muLawSource struct {
	r    io.ReadCloser
	buf  []byte
	done bool
}

// PCMStream plays raw s16le mono 16 kHz PCM read from r. r is closed when the source
// is exhausted or Close is called.
func PCMStream(r io.ReadCloser) AudioSource {
	return &pcmS16Source{r: r, buf: make([]byte, FrameSamples*2)}
}

// MuLawStream plays raw 8-bit G.711 PCMU at 8 kHz mono from r. r is closed when
// the source is exhausted or Close is called.
func MuLawStream(r io.ReadCloser) AudioSource {
	return &muLawSource{r: r, buf: make([]byte, FrameSamples/2)}
}

// ReadFrame reads one frame of s16le PCM and converts it to float32 in [-1, 1).
func (s *pcmS16Source) ReadFrame() ([]float32, error) {
	if s.r == nil {
		return nil, errSourceClosed
	}
	n, err := io.ReadFull(s.r, s.buf)
	if n == 0 {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, err
	}
	for i := n; i < len(s.buf); i++ { // zero-pad a trailing partial frame
		s.buf[i] = 0
	}
	frame := make([]float32, FrameSamples)
	for i := range frame {
		frame[i] = float32(int16(binary.LittleEndian.Uint16(s.buf[2*i:]))) / 32768.0
	}
	if err == io.ErrUnexpectedEOF {
		err = nil // emit this last (padded) frame; next call returns EOF
	}
	return frame, err
}

// Close closes the underlying reader.
func (s *pcmS16Source) Close() error {
	if s.r == nil {
		return nil
	}
	err := s.r.Close()
	s.r = nil
	return err
}

// ReadFrame decodes one 60 ms PCMU block and expands it to 16 kHz mono. A short
// final block is zero-padded; the following call returns io.EOF.
func (s *muLawSource) ReadFrame() ([]float32, error) {
	if s.r == nil {
		return nil, errSourceClosed
	}
	if s.done {
		return nil, io.EOF
	}
	n, err := io.ReadFull(s.r, s.buf)
	if n == 0 {
		if err == io.EOF {
			s.done = true
			return nil, io.EOF
		}
		return nil, err
	}
	frame := make([]float32, FrameSamples)
	for i, encoded := range s.buf[:n] {
		sample := decodeMuLaw(encoded)
		frame[2*i] = sample
		frame[2*i+1] = sample
	}
	if err == io.ErrUnexpectedEOF {
		s.done = true
		err = nil
	}
	return frame, err
}

// Close closes the underlying PCMU stream.
func (s *muLawSource) Close() error {
	if s.r == nil {
		return nil
	}
	err := s.r.Close()
	s.r = nil
	return err
}

// decodeMuLaw expands one G.711 PCMU code word to normalized signed PCM.
func decodeMuLaw(encoded byte) float32 {
	// Source of truth: ITU-T G.711 (11/1988), section 3.2, mu-law expansion.
	value := ^encoded
	magnitude := (int(value&0x0f) << 3) + 0x84
	magnitude <<= (value >> 4) & 0x07
	magnitude -= 0x84
	if value&0x80 != 0 {
		magnitude = -magnitude
	}
	return float32(magnitude) / 32768
}
