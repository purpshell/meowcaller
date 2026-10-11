package meowcaller

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// audioPacket is a minimal audio RTP packet (payload type 120) with the given
// sequence number.
func audioPacket(seq uint16) []byte {
	pkt := make([]byte, 20)
	pkt[0] = 0x80
	pkt[1] = 120
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*960)
	binary.BigEndian.PutUint32(pkt[8:12], 0xfeedbeef)
	return pkt
}

type reorderHarness struct {
	t    *testing.T
	feed chan reorderInput
	rx   *reorderingReceiver
}

func newReorderHarness(t *testing.T, window time.Duration) *reorderHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &reorderHarness{t: t, feed: make(chan reorderInput, 64)}
	recv := func(buf []byte) (int, error) {
		select {
		case item := <-h.feed:
			return copy(buf, item.pkt), item.err
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	h.rx = newReorderingReceiver(ctx, recv, window, nil)
	return h
}

func (h *reorderHarness) send(seqs ...uint16) {
	for _, seq := range seqs {
		h.feed <- reorderInput{pkt: audioPacket(seq)}
	}
}

// next returns the sequence number of the next packet the media loop would read.
func (h *reorderHarness) next() uint16 {
	h.t.Helper()
	buf := make([]byte, 1500)
	n, err := h.rx.Recv(buf)
	if err != nil {
		h.t.Fatalf("Recv: %v", err)
	}
	if n < 4 {
		h.t.Fatalf("short packet: %d bytes", n)
	}
	return binary.BigEndian.Uint16(buf[2:4])
}

func (h *reorderHarness) expect(want ...uint16) {
	h.t.Helper()
	for i, w := range want {
		if got := h.next(); got != w {
			h.t.Fatalf("packet %d: seq %d, want %d", i, got, w)
		}
	}
}

func TestReorderPassesInOrderAudioThrough(t *testing.T) {
	h := newReorderHarness(t, time.Hour)
	h.send(65534, 65535, 0, 1)
	h.expect(65534, 65535, 0, 1)
	if r := h.rx.Report(); r.Packets != 4 || r.Reordered != 0 || r.Late != 0 {
		t.Fatalf("report %+v", r)
	}
}

func TestReorderRestoresSwappedPackets(t *testing.T) {
	// The window never expires here: only the missing packet can release the held ones.
	h := newReorderHarness(t, time.Hour)
	h.send(10, 12, 13, 11, 14)
	h.expect(10, 11, 12, 13, 14)
	if r := h.rx.Report(); r.Reordered != 2 || r.Recovered != 1 || r.Skipped != 0 || r.Late != 0 {
		t.Fatalf("report %+v", r)
	}
}

func TestReorderSkipsAGapAfterTheWindowAndDropsTheLatecomer(t *testing.T) {
	h := newReorderHarness(t, 40*time.Millisecond)
	h.send(1, 3)
	h.expect(1)
	start := time.Now()
	h.expect(3) // released by the timer, with no further packet arriving
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Fatalf("held packet released after %s, before the window", waited)
	}
	h.send(2, 4)
	h.expect(4)
	if r := h.rx.Report(); r.Skipped != 1 || r.Late != 1 || r.Recovered != 0 {
		t.Fatalf("report %+v", r)
	}
}

func TestReorderDoesNotDelayOtherPackets(t *testing.T) {
	h := newReorderHarness(t, time.Hour)
	h.send(1, 3)
	h.expect(1)
	stun := []byte{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	h.feed <- reorderInput{pkt: stun}
	buf := make([]byte, 1500)
	n, err := h.rx.Recv(buf)
	if err != nil || n != len(stun) || buf[4] != 0x21 {
		t.Fatalf("got %d bytes, err %v: the STUN packet did not pass ahead of held audio", n, err)
	}
}

func TestReorderReleasesHeldAudioBeforeTheReadError(t *testing.T) {
	h := newReorderHarness(t, time.Hour)
	h.send(1, 3)
	h.expect(1)
	gone := errors.New("relay gone")
	h.feed <- reorderInput{err: gone}
	h.expect(3)
	if _, err := h.rx.Recv(make([]byte, 1500)); !errors.Is(err, gone) {
		t.Fatalf("err = %v, want the read error", err)
	}
}

func TestReorderOffKeepsArrivalOrder(t *testing.T) {
	h := newReorderHarness(t, 0)
	h.send(1, 3, 2)
	h.expect(1, 3, 2)
}

func TestReorderBoundsWhatItHolds(t *testing.T) {
	h := newReorderHarness(t, time.Hour)
	h.send(1)
	h.expect(1)
	for seq := uint16(3); seq <= 3+audioReorderMaxHeld; seq++ {
		h.send(seq)
	}
	// One more than the bound is held: the gap is given up on without the timer.
	h.expect(3, 4, 5)
	if r := h.rx.Report(); r.Skipped != 1 {
		t.Fatalf("report %+v", r)
	}
}

// videoPacket is a minimal H.264 RTP packet (payload type 97).
func videoPacket(seq uint16) []byte {
	pkt := audioPacket(seq)
	pkt[1] = 97
	binary.BigEndian.PutUint32(pkt[8:12], 0xcafef00d)
	return pkt
}

func TestReorderRestoresVideoOnItsOwnStream(t *testing.T) {
	h := newReorderHarness(t, time.Hour)
	// Video 3 arrives before video 2 while audio flows in order around it.
	h.feed <- reorderInput{pkt: videoPacket(1)}
	h.feed <- reorderInput{pkt: videoPacket(3)}
	h.send(50, 51)
	h.feed <- reorderInput{pkt: videoPacket(2)}
	h.expect(1, 50, 51, 2, 3)
	audio, video := h.rx.Report(), h.rx.VideoReport()
	if audio.Packets != 2 || audio.Reordered != 0 {
		t.Fatalf("audio report %+v", audio)
	}
	if video.Packets != 3 || video.Reordered != 1 || video.Recovered != 1 || video.Late != 0 {
		t.Fatalf("video report %+v", video)
	}
}
