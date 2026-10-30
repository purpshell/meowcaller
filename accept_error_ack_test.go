package meowcaller

import (
	"testing"
	"time"
)

func TestAudioSinceErrorAck(t *testing.T) {
	c := &Call{}
	ack := time.Now()
	if audioSince(c, ack) {
		t.Fatal("no audio yet: must report none")
	}
	c.lastInboundAudio.Store(ack.Add(-time.Second).UnixNano())
	if audioSince(c, ack) {
		t.Fatal("audio before the ack (early media) must not count")
	}
	c.lastInboundAudio.Store(ack.Add(200 * time.Millisecond).UnixNano())
	if !audioSince(c, ack) {
		t.Fatal("audio after the ack: the accept landed")
	}
}
