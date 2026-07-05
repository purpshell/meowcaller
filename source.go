package meowcaller

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	gomp3 "github.com/hajimehoshi/go-mp3"
	"github.com/pion/opus"
	"github.com/pion/opus/pkg/oggreader"
)

// frameSource turns a stream of decoded 16 kHz mono float32 samples (pushed in
// arbitrary-length chunks) into FrameSamples-long frames. Each decoder fills it via
// push, then ReadFrame drains it; the final partial frame is zero-padded. It is the
// common framing substrate behind WAVFile/MP3File/OpusFile.
type frameSource struct {
	pending []float32            // decoded samples not yet emitted as a full frame
	more    func() (bool, error) // decode the next chunk into pending; false = exhausted
	closer  func() error
	done    bool
}

// push appends decoded 16 kHz mono samples to the pending buffer.
func (f *frameSource) push(samples []float32) {
	f.pending = append(f.pending, samples...)
}

// ReadFrame returns the next FrameSamples mono frame, pulling and decoding more of the
// underlying stream as needed, or io.EOF when the stream is exhausted.
func (f *frameSource) ReadFrame() ([]float32, error) {
	for len(f.pending) < FrameSamples && !f.done {
		ok, err := f.more()
		if err != nil {
			return nil, err
		}
		if !ok {
			f.done = true
		}
	}
	if len(f.pending) == 0 {
		return nil, io.EOF
	}
	frame := make([]float32, FrameSamples)
	n := copy(frame, f.pending)
	f.pending = f.pending[n:]
	return frame, nil
}

// Close releases the underlying decoder/file.
func (f *frameSource) Close() error {
	if f.closer == nil {
		return nil
	}
	err := f.closer()
	f.closer = nil
	return err
}

// downmixResampler converts interleaved PCM at an arbitrary sample rate and channel
// count into 16 kHz mono float32, downmixing channels by averaging and resampling by
// linear interpolation. It is stateful across chunks so streaming decoders can feed it
// a frame at a time without gaps at chunk boundaries.
type downmixResampler struct {
	inRate   int
	channels int
	// pos is the fractional read position (in input mono samples) for the next output
	// sample, carried across pushes.
	pos      float64
	last     float32 // last input mono sample of the previous chunk (for interpolation across boundaries)
	havePrev bool
}

func newDownmixResampler(inRate, channels int) *downmixResampler {
	return &downmixResampler{inRate: inRate, channels: channels}
}

// process consumes one chunk of interleaved s16/float input (already downmixed to mono
// float32 here via the caller's mono slice) and returns the 16 kHz mono samples it
// yields. The caller passes the chunk already collapsed to mono.
func (d *downmixResampler) process(mono []float32) []float32 {
	if len(mono) == 0 {
		return nil
	}
	if d.inRate == SampleRate {
		return mono
	}
	step := float64(d.inRate) / float64(SampleRate)
	// Build a working buffer that includes the carried-over last sample at index -1 so
	// interpolation is continuous across chunk boundaries.
	var src []float32
	base := 0.0
	if d.havePrev {
		src = make([]float32, 0, len(mono)+1)
		src = append(src, d.last)
		src = append(src, mono...)
		base = 1.0 // input index 0 of this chunk sits at src index 1
	} else {
		src = mono
	}
	var out []float32
	for {
		idx := d.pos + base
		i := int(idx)
		if i+1 >= len(src) {
			break
		}
		frac := idx - float64(i)
		s := src[i]*(1-float32(frac)) + src[i+1]*float32(frac)
		out = append(out, s)
		d.pos += step
	}
	// Advance pos into the coordinate frame of the next chunk: subtract the number of
	// whole input samples we have fully consumed from this chunk.
	consumed := float64(len(mono))
	d.pos -= consumed
	d.last = mono[len(mono)-1]
	d.havePrev = true
	return out
}

// WAVFile streams a RIFF/WAVE file as 16 kHz mono FrameSamples frames, downmixing and
// resampling as needed. 16-bit PCM is supported.
func WAVFile(path string) (AudioSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	wr, err := newWavReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	res := newDownmixResampler(wr.sampleRate, wr.channels)
	buf := make([]byte, 8192)
	fs := &frameSource{closer: f.Close}
	fs.more = func() (bool, error) {
		n, err := io.ReadFull(wr.r, buf)
		if n == 0 {
			if err == io.EOF {
				return false, nil
			}
			return false, err
		}
		// Trim to a whole number of (channel-grouped) 16-bit samples.
		frameBytes := wr.channels * 2
		n -= n % frameBytes
		if n == 0 {
			return false, nil
		}
		mono := wavMono(buf[:n], wr.channels)
		fs.push(res.process(mono))
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// Drain any remaining decoded samples on the next iteration; signal that no
			// further input remains after this push.
			return len(fs.pending) >= FrameSamples, nil
		}
		return true, nil
	}
	return fs, nil
}

// MuLawFile streams a raw 8-bit G.711 PCMU file at 8 kHz mono as 16 kHz mono
// FrameSamples frames.
func MuLawFile(path string) (AudioSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return MuLawStream(f), nil
}

// wavMono collapses interleaved s16le PCM to mono float32 by averaging channels.
func wavMono(b []byte, channels int) []float32 {
	frames := len(b) / (channels * 2)
	out := make([]float32, frames)
	for i := 0; i < frames; i++ {
		var acc int32
		for c := 0; c < channels; c++ {
			off := (i*channels + c) * 2
			acc += int32(int16(binary.LittleEndian.Uint16(b[off:])))
		}
		out[i] = float32(acc) / float32(channels) / 32768.0
	}
	return out
}

// wavReader holds the parsed WAVE format and the reader positioned at the data chunk.
type wavReader struct {
	r          io.Reader
	sampleRate int
	channels   int
}

var errBadWav = errors.New("meowcaller: not a 16-bit PCM RIFF/WAVE file")

// newWavReader parses the RIFF/WAVE header, validating 16-bit PCM, and returns a reader
// positioned at the start of the sample data.
func newWavReader(r io.Reader) (*wavReader, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errBadWav
	}
	wr := &wavReader{}
	haveFmt := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, err
		}
		id := string(ch[0:4])
		size := binary.LittleEndian.Uint32(ch[4:8])
		switch id {
		case "fmt ":
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return nil, err
			}
			if len(body) < 16 {
				return nil, errBadWav
			}
			audioFormat := binary.LittleEndian.Uint16(body[0:2])
			wr.channels = int(binary.LittleEndian.Uint16(body[2:4]))
			wr.sampleRate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits := binary.LittleEndian.Uint16(body[14:16])
			// 1 = PCM; 0xFFFE = WAVE_FORMAT_EXTENSIBLE (PCM subformat assumed).
			if (audioFormat != 1 && audioFormat != 0xFFFE) || bits != 16 {
				return nil, fmt.Errorf("%w: format=%d bits=%d", errBadWav, audioFormat, bits)
			}
			if wr.channels < 1 || wr.sampleRate < 1 {
				return nil, errBadWav
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, errBadWav
			}
			wr.r = io.LimitReader(r, int64(size))
			return wr, nil
		default:
			// Skip unknown chunk (with RIFF word-alignment padding).
			skip := int64(size)
			if size%2 == 1 {
				skip++
			}
			if _, err := io.CopyN(io.Discard, r, skip); err != nil {
				return nil, err
			}
		}
	}
}

// MP3File streams an MP3 file as 16 kHz mono FrameSamples frames, downmixing the
// decoder's s16le stereo output to mono and resampling to 16 kHz.
func MP3File(path string) (AudioSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	dec, err := gomp3.NewDecoder(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	// go-mp3 always emits 16-bit little-endian stereo (2 channels) at SampleRate().
	res := newDownmixResampler(dec.SampleRate(), 2)
	buf := make([]byte, 8192)
	fs := &frameSource{closer: f.Close}
	fs.more = func() (bool, error) {
		n, err := io.ReadFull(dec, buf)
		if n == 0 {
			if err == io.EOF {
				return false, nil
			}
			return false, err
		}
		n -= n % 4 // whole stereo s16 frames (2 channels * 2 bytes)
		if n == 0 {
			return false, nil
		}
		mono := wavMono(buf[:n], 2)
		fs.push(res.process(mono))
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return len(fs.pending) >= FrameSamples, nil
		}
		return true, nil
	}
	return fs, nil
}

// OpusFile streams an Ogg/Opus file as 16 kHz mono FrameSamples frames. It reads the
// Ogg pages, decodes each Opus packet to 16 kHz mono PCM, and frames the result.
func OpusFile(path string) (AudioSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ogg, oggHdr, err := oggreader.NewWith(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	dec, err := opus.NewDecoderWithOutput(SampleRate, 1)
	if err != nil {
		f.Close()
		return nil, err
	}
	// Decode buffer: max Opus packet duration is 120 ms, which at 16 kHz mono is 1920
	// samples; round up for safety.
	out := make([]float32, 2048)
	// PreSkip samples (priming) at the head of the stream should be discarded, scaled
	// from the 48 kHz codec clock to our 16 kHz output.
	skip := int(oggHdr.PreSkip) * SampleRate / 48000
	fs := &frameSource{closer: f.Close}
	fs.more = func() (bool, error) {
		for {
			packet, _, err := ogg.ParseNextPacket()
			if err == io.EOF {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if len(packet) == 0 {
				continue
			}
			n, derr := dec.DecodeToFloat32(packet, out)
			if derr != nil {
				return false, fmt.Errorf("meowcaller: opus decode: %w", derr)
			}
			if n == 0 {
				continue
			}
			samples := out[:n]
			if skip > 0 {
				if skip >= len(samples) {
					skip -= len(samples)
					continue
				}
				samples = samples[skip:]
				skip = 0
			}
			pushed := make([]float32, len(samples))
			copy(pushed, samples)
			fs.push(pushed)
			return true, nil
		}
	}
	return fs, nil
}
