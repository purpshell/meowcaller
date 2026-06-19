package mlow

// SmplTOC is the decoded first byte of an inbound MLow frame: how to interpret
// the rest of the frame, or that it is a standard Opus packet to route elsewhere.
type SmplTOC struct {
	StdOpus    bool
	SID        bool
	VAD        bool
	SampleRate int
	FrameMs    int
	Voiced     bool
	Active     bool
	Flag2      bool
	Flag0      bool
}

// standardOpusFrameMs returns the frame duration (ms) of a standard Opus packet
// from the config field b>>3 (RFC 6716 Table 2). 2.5 ms is rounded up to 3.
func standardOpusFrameMs(b byte) int {
	config := b >> 3
	switch {
	case config < 12: // SILK NB/MB/WB
		return []int{10, 20, 40, 60}[config&3]
	case config < 16: // Hybrid
		return []int{10, 20}[(config-12)&1]
	default:
		switch config & 3 {
		case 0:
			return 3 // 2.5 ms rounded up
		case 1:
			return 5
		case 2:
			return 10
		default:
			return 20
		}
	}
}

// ParseSmplTOC decodes the TOC byte at the head of an inbound MLow frame.
func ParseSmplTOC(b byte) SmplTOC {
	if b&0xC0 == 0xC0 {
		return SmplTOC{
			StdOpus:    true,
			SampleRate: 16000,
			FrameMs:    standardOpusFrameMs(b),
		}
	}
	bit1 := (b>>1)&1 != 0
	vad := (b>>6)&1 != 0
	sampleRate := 16000
	if b&0x20 != 0 {
		sampleRate = 32000
	}
	return SmplTOC{
		SID:        b>>7 != 0,
		VAD:        vad,
		SampleRate: sampleRate,
		FrameMs:    []int{10, 20, 60, 120}[(b>>3)&3],
		Voiced:     vad && bit1,
		Active:     vad || bit1,
		Flag2:      (b>>2)&1 != 0,
		Flag0:      b&1 != 0,
	}
}
