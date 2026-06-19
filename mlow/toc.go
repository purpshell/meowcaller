package mlow

import "errors"

// errNotImplemented marks a scaffolded body whose logic the human has not yet
// directed.
var errNotImplemented = errors.New("mlow: not implemented")

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

// ParseSmplTOC decodes the TOC byte at the head of an inbound MLow frame.
func ParseSmplTOC(b byte) SmplTOC {
	// TODO(human): bit layout + standard-Opus routing. Open decisions:
	//   - when (b & 0xC0) == 0xC0, derive FrameMs from the RFC 6716 config field
	//     and return StdOpus with the other fields zeroed;
	//   - otherwise unpack SID/VAD/rate/frame-size/flags and the derived
	//     Voiced (vad && bit1) / Active (vad || bit1).
	_ = errNotImplemented
	return SmplTOC{}
}
