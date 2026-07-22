package meowcaller

const (
	audioPlayoutPrefillSamples = 2 * FrameSamples
	audioPlayoutMaxGapSamples  = 2 * SampleRate
)

type audioPlayoutFrame struct {
	timestamp uint32
	pcm       []float32
}

// audioPlayoutBuffer converts packet-oriented decoder output into a continuous
// RTP-timestamped PCM stream. Holding two packet intervals gives the downstream
// real-time track enough headroom for normal relay jitter.
type audioPlayoutBuffer struct {
	pending        *audioPlayoutFrame
	prefill        [][]float32
	prefillSamples int
	started        bool
}

func newAudioPlayoutBuffer() *audioPlayoutBuffer {
	return &audioPlayoutBuffer{}
}

func (p *audioPlayoutBuffer) Push(timestamp uint32, frame []float32, sink AudioSink) (startedNow bool, err error) {
	if p.pending == nil {
		p.pending = &audioPlayoutFrame{timestamp: timestamp, pcm: frame}
		return false, nil
	}

	delta := timestamp - p.pending.timestamp
	if delta == 0 || delta > audioPlayoutMaxGapSamples {
		p.reset(timestamp, frame)
		return false, nil
	}

	ready := alignAudioFrame(p.pending.pcm, int(delta))
	p.pending = &audioPlayoutFrame{timestamp: timestamp, pcm: frame}
	if p.started && sink != nil {
		return false, sink.WriteFrame(ready)
	}
	if p.started {
		p.started = false
		p.prefill = nil
		p.prefillSamples = 0
	}

	p.prefill = append(p.prefill, ready)
	p.prefillSamples += len(ready)
	for p.prefillSamples > audioPlayoutMaxGapSamples && len(p.prefill) > 1 {
		p.prefillSamples -= len(p.prefill[0])
		p.prefill = p.prefill[1:]
	}
	if sink == nil || p.prefillSamples < audioPlayoutPrefillSamples {
		return false, nil
	}

	for len(p.prefill) > 0 {
		buffered := p.prefill[0]
		if err = sink.WriteFrame(buffered); err != nil {
			return false, err
		}
		p.prefillSamples -= len(buffered)
		p.prefill = p.prefill[1:]
	}
	p.started = true
	p.prefill = nil
	return true, nil
}

func (p *audioPlayoutBuffer) Flush(sink AudioSink) error {
	if !p.started || p.pending == nil || sink == nil {
		return nil
	}
	err := sink.WriteFrame(p.pending.pcm)
	p.pending = nil
	return err
}

func (p *audioPlayoutBuffer) reset(timestamp uint32, frame []float32) {
	p.pending = &audioPlayoutFrame{timestamp: timestamp, pcm: frame}
	p.prefill = nil
	p.prefillSamples = 0
	p.started = false
}

func alignAudioFrame(frame []float32, samples int) []float32 {
	if samples == len(frame) {
		return frame
	}
	aligned := make([]float32, samples)
	copy(aligned, frame)
	return aligned
}
