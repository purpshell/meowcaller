package meowcaller

import (
	"context"
	"time"

	"github.com/purpshell/meowcaller/relay"
	"github.com/purpshell/meowcaller/rtp"
)

// DefaultAudioReorderWindow is how long an audio packet that arrived ahead of a
// missing one is held for the missing one to show up.
const DefaultAudioReorderWindow = 200 * time.Millisecond

// audioReorderMaxHeld bounds the packets held behind one gap: 60 ms each for
// audio, a couple of frames' worth for video.
const audioReorderMaxHeld = 32

// AudioReorderReport is what the reorder stage did to one kind of inbound media
// (audio, or video) of one call.
type AudioReorderReport struct {
	// Packets is every RTP packet of that kind that arrived.
	Packets uint64
	// Reordered is how many arrived ahead of an earlier packet and were held.
	Reordered uint64
	// Recovered is how many gaps closed in time: the earlier packet arrived
	// within the window and the stream went on in order.
	Recovered uint64
	// Skipped is how many gaps were given up on: the window ran out and the held
	// packets were released without the missing one.
	Skipped uint64
	// Late is how many packets arrived after their gap was given up on, or
	// twice, and were dropped.
	Late uint64
	// MaxHold is the longest a packet was held.
	MaxHold time.Duration
}

type reorderInput struct {
	pkt []byte
	err error
}

type heldAudioPacket struct {
	pkt []byte
	at  time.Time
}

type audioReorderStream struct {
	report   *AudioReorderReport
	started  bool
	expected uint16
	held     map[uint16]heldAudioPacket
}

// reorderingReceiver sits between the relay channel and the media loop. A media
// path that reorders datagrams (a SOCKS5 UDP proxy does, heavily) hands the loop
// packets out of sequence, and video arrives reordered from the relay even on a
// direct path. The audio decoder and playout buffer need audio in sequence, and the
// H.264 assembler drops everything up to the next keyframe on any gap. Audio and
// video RTP are released in sequence-number order, each stream on its own: a packet that
// arrives ahead of a missing one waits up to window for it, then the gap is
// skipped. In-order media and every other packet (STUN, RTCP, app data) pass
// straight through, so a clean path pays no delay.
//
// Recv is called by one goroutine, the media loop; all reorder state lives there.
type reorderingReceiver struct {
	ctx      context.Context
	in       chan reorderInput
	window   time.Duration
	observe  func(ssrc uint32, seq uint16, timestamp uint32)
	streams  map[uint32]*audioReorderStream
	ready    [][]byte
	timer    *time.Timer
	pending  error
	report   AudioReorderReport
	video    AudioReorderReport
	nowFunc  func() time.Time
	deadline time.Time
}

// newReorderingReceiver starts reading recv. The reader stops when recv fails or
// ctx ends.
func newReorderingReceiver(
	ctx context.Context,
	recv func([]byte) (int, error),
	window time.Duration,
	observe func(ssrc uint32, seq uint16, timestamp uint32),
) *reorderingReceiver {
	r := &reorderingReceiver{
		ctx: ctx, in: make(chan reorderInput, 256), window: window, observe: observe,
		streams: map[uint32]*audioReorderStream{}, nowFunc: time.Now,
	}
	r.timer = time.NewTimer(time.Hour)
	r.timer.Stop()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := recv(buf)
			item := reorderInput{err: err}
			if err == nil {
				item.pkt = append([]byte(nil), buf[:n]...)
			}
			select {
			case r.in <- item:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

// Recv returns the next packet for the media loop, like RelayMediaChannel.Recv.
func (r *reorderingReceiver) Recv(buf []byte) (int, error) {
	for {
		if len(r.ready) > 0 {
			pkt := r.ready[0]
			r.ready = r.ready[1:]
			return copy(buf, pkt), nil
		}
		if r.pending != nil {
			return 0, r.pending
		}
		select {
		case item := <-r.in:
			if item.err != nil {
				// Nothing more is coming: release what is held, then fail.
				r.flush(true)
				r.pending = item.err
				continue
			}
			r.push(item.pkt)
		case <-r.timer.C:
			r.deadline = time.Time{}
			r.flush(false)
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
		r.arm()
	}
}

// Report returns the audio counters so far.
func (r *reorderingReceiver) Report() AudioReorderReport { return r.report }

// VideoReport returns the video counters so far.
func (r *reorderingReceiver) VideoReport() AudioReorderReport { return r.video }

func (r *reorderingReceiver) push(raw []byte) {
	pkt, _, valid := relay.UnwrapGroupForwardingPacket(raw)
	if !valid || relay.ClassifyRelayPacket(pkt) != relay.RelayPacketRtp {
		r.ready = append(r.ready, raw)
		return
	}
	header, ok := rtp.ParseRtpHeader(pkt)
	if !ok {
		r.ready = append(r.ready, raw)
		return
	}
	var report *AudioReorderReport
	switch classifyMediaPayload(header) {
	case mediaPayloadAudio:
		report = &r.report
		if r.observe != nil {
			r.observe(header.Ssrc, header.SequenceNumber, header.Timestamp)
		}
	case mediaPayloadVideo:
		report = &r.video
	default:
		r.ready = append(r.ready, raw)
		return
	}
	report.Packets++
	if r.window <= 0 {
		r.ready = append(r.ready, raw)
		return
	}
	st := r.streams[header.Ssrc]
	if st == nil {
		st = &audioReorderStream{report: report, held: map[uint16]heldAudioPacket{}}
		r.streams[header.Ssrc] = st
	}
	seq := header.SequenceNumber
	if !st.started {
		st.started = true
		st.expected = seq + 1
		r.ready = append(r.ready, raw)
		return
	}
	switch ahead := int16(seq - st.expected); {
	case ahead == 0:
		if len(st.held) > 0 {
			report.Recovered++
		}
		r.ready = append(r.ready, raw)
		st.expected++
		r.drain(st)
	case ahead < 0:
		report.Late++
	default:
		if _, dup := st.held[seq]; dup {
			report.Late++
			return
		}
		report.Reordered++
		st.held[seq] = heldAudioPacket{pkt: raw, at: r.nowFunc()}
		if len(st.held) > audioReorderMaxHeld {
			r.skip(st)
		}
	}
}

// drain releases the held packets that now follow in sequence.
func (r *reorderingReceiver) drain(st *audioReorderStream) {
	for {
		held, ok := st.held[st.expected]
		if !ok {
			return
		}
		if hold := r.nowFunc().Sub(held.at); hold > st.report.MaxHold {
			st.report.MaxHold = hold
		}
		delete(st.held, st.expected)
		r.ready = append(r.ready, held.pkt)
		st.expected++
	}
}

// skip gives up on the missing packet: the stream resumes at the earliest held one.
func (r *reorderingReceiver) skip(st *audioReorderStream) {
	if len(st.held) == 0 {
		return
	}
	first := true
	var next uint16
	for seq := range st.held {
		if first || int16(seq-next) < 0 {
			next, first = seq, false
		}
	}
	st.report.Skipped++
	st.expected = next
	r.drain(st)
}

// oldest is when the longest-waiting held packet of st arrived.
func (st *audioReorderStream) oldest() (time.Time, bool) {
	var at time.Time
	for _, held := range st.held {
		if at.IsZero() || held.at.Before(at) {
			at = held.at
		}
	}
	return at, !at.IsZero()
}

// flush skips every gap whose held packets have waited out the window, or all of
// them when everything is true.
func (r *reorderingReceiver) flush(everything bool) {
	now := r.nowFunc()
	for _, st := range r.streams {
		for {
			at, ok := st.oldest()
			if !ok || (!everything && now.Sub(at) < r.window) {
				break
			}
			r.skip(st)
		}
	}
}

// arm sets the timer for the next held packet to run out of window.
func (r *reorderingReceiver) arm() {
	var next time.Time
	for _, st := range r.streams {
		if at, ok := st.oldest(); ok && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	if next.IsZero() {
		if !r.deadline.IsZero() {
			r.timer.Stop()
			r.deadline = time.Time{}
		}
		return
	}
	next = next.Add(r.window)
	if next.Equal(r.deadline) {
		return
	}
	r.deadline = next
	r.timer.Reset(time.Until(next))
}
