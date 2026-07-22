package meowcaller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

type fakeAcceptTimer struct {
	mu      sync.Mutex
	fn      func()
	stopped bool
	fired   bool
}

func (t *fakeAcceptTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped && !t.fired
	t.stopped = true
	return wasActive
}

func (t *fakeAcceptTimer) Fire() {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	fn := t.fn
	t.mu.Unlock()
	fn()
}

type fakeAcceptClock struct {
	mu     sync.Mutex
	timers []*fakeAcceptTimer
}

func (c *fakeAcceptClock) AfterFunc(_ time.Duration, fn func()) acceptTimer {
	t := &fakeAcceptTimer{fn: fn}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return t
}

func (c *fakeAcceptClock) Timer(t *testing.T, index int) *fakeAcceptTimer {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if index >= len(c.timers) {
		t.Fatalf("timer %d missing; have %d", index, len(c.timers))
	}
	return c.timers[index]
}

type acceptHarness struct {
	eng      *engine
	call     *Call
	clock    *fakeAcceptClock
	mu       sync.Mutex
	nodes    []waBinary.Node
	sendErr  error
	sendFn   func(context.Context, waBinary.Node) error
	events   []IncomingAcceptEvent
	endCount int
}

func newAcceptHarness(video, relayReady bool) *acceptHarness {
	h := &acceptHarness{clock: &fakeAcceptClock{}}
	client := &Client{log: zerolog.Nop(), incomingAcceptFallbackTimeout: time.Second}
	h.eng = newEngine(client)
	client.eng = h.eng
	h.eng.afterFunc = h.clock.AfterFunc
	h.eng.sendCallNode = func(ctx context.Context, node waBinary.Node) error {
		h.mu.Lock()
		h.nodes = append(h.nodes, node)
		fn, err := h.sendFn, h.sendErr
		h.mu.Unlock()
		if fn != nil {
			return fn(ctx, node)
		}
		return err
	}
	peer := types.NewJID("15551234567", types.DefaultUserServer)
	h.call = &Call{eng: h.eng, id: "CID", peer: peer, phase: CallPhaseRinging}
	h.call.OnIncomingAccept(func(event IncomingAcceptEvent) {
		h.mu.Lock()
		h.events = append(h.events, event)
		h.mu.Unlock()
	})
	h.call.OnEnd(func(string) {
		h.mu.Lock()
		h.endCount++
		h.mu.Unlock()
	})
	m := &engineCall{
		call: h.call, direction: CallDirectionIncoming, from: peer, creator: peer,
		localVideo: video, remoteVideo: video,
		accept: incomingAccept{preacceptSent: true},
	}
	if relayReady {
		m.relay = &relayData{endpoints: []relayEndpoint{{
			relayName:   "test-relay",
			wireAddress: []byte{57, 144, 233, 57, 0x0d, 0x96},
		}}}
	}
	h.eng.calls[h.call.id] = m
	return h
}

func TestIncomingFinalAcceptCarriesSelectedRelayEndpointAndCapability(t *testing.T) {
	h := newAcceptHarness(true, true)
	h.eng.onCallRaw(h.muteNode())
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}

	accept := h.firstAccept(t)
	action := accept.GetChildren()[0]
	var relayTE, capability *waBinary.Node
	for i := range action.GetChildren() {
		child := &action.GetChildren()[i]
		switch child.Tag {
		case "te":
			relayTE = child
		case "capability":
			capability = child
		}
	}
	if relayTE == nil {
		t.Fatal("final accept omitted the selected relay endpoint")
	}
	if got := nodeBytes(relayTE); string(got) != string([]byte{57, 144, 233, 57, 0x0d, 0x96}) {
		t.Fatalf("final accept relay endpoint = %x, want selected relay endpoint", got)
	}
	if capability == nil {
		t.Fatal("final accept omitted the negotiated capability")
	}
}

func TestIncomingVideoAnnouncesLocalVideoAfterFinalAcceptExactlyOnce(t *testing.T) {
	h := newAcceptHarness(true, true)
	h.eng.onCallRaw(h.muteNode())
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	h.eng.onCallRaw(h.muteNode())

	h.mu.Lock()
	nodes := append([]waBinary.Node(nil), h.nodes...)
	h.mu.Unlock()
	if len(nodes) != 2 {
		t.Fatalf("sent nodes = %d, want final accept followed by one video announcement", len(nodes))
	}
	if got := nodes[0].GetChildren()[0].Tag; got != "accept" {
		t.Fatalf("first action = %s, want accept", got)
	}
	video := nodes[1].GetChildren()[0]
	if video.Tag != "video" || video.AttrGetter().Int("state") != signaling.VideoStateEnabled {
		t.Fatalf("second action = <%s state=%d>, want <video state=1>", video.Tag, video.AttrGetter().Int("state"))
	}
	if got := video.AttrGetter().String("dec"); got != signaling.VideoStateDecH264 {
		t.Fatalf("video announcement dec = %q, want H264", got)
	}
}

func (h *acceptHarness) acceptCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, node := range h.nodes {
		if children := node.GetChildren(); len(children) > 0 && children[0].Tag == "accept" {
			n++
		}
	}
	return n
}

func (h *acceptHarness) firstAccept(t *testing.T) waBinary.Node {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, node := range h.nodes {
		if children := node.GetChildren(); len(children) > 0 && children[0].Tag == "accept" {
			return node
		}
	}
	t.Fatal("accept stanza missing")
	return waBinary.Node{}
}

func (h *acceptHarness) muteNode() *waBinary.Node {
	return &waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{"from": h.call.peer}, Content: []waBinary.Node{{
		Tag: "mute_v2", Attrs: waBinary.Attrs{"call-id": h.call.id, "call-creator": h.call.peer, "mute-state": "0"},
	}}}
}

func TestIncomingAcceptAnswerThenMuteSendsExactlyOnce(t *testing.T) {
	h := newAcceptHarness(false, true)
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	timer := h.clock.Timer(t, 0)
	h.eng.onCallRaw(h.muteNode())
	timer.Fire()
	h.eng.onCallRaw(h.muteNode())
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("accept count = %d, want 1", got)
	}
	if state := h.eng.calls[h.call.id].accept.state; state != incomingAcceptSent {
		t.Fatalf("state = %v, want sent", state)
	}
}

func TestIncomingAcceptMuteBeforeAnswerSendsImmediately(t *testing.T) {
	h := newAcceptHarness(false, true)
	h.eng.onCallRaw(h.muteNode())
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("accept count = %d, want 1", got)
	}
	if len(h.clock.timers) != 0 {
		t.Fatal("fallback timer armed after mute_v2 was already observed")
	}
}

func TestIncomingAcceptMuteWaitsForRequiredTransport(t *testing.T) {
	h := newAcceptHarness(false, false)
	h.eng.onCallRaw(h.muteNode())
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	if got := h.acceptCount(); got != 0 {
		t.Fatalf("accept count before transport = %d, want 0", got)
	}
	h.eng.onRelay(h.call.id, &waBinary.Node{Tag: "relay"})
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("accept count after transport = %d, want 1", got)
	}
}

func TestIncomingAcceptFallbackWaitsForTransportAndSendsOnce(t *testing.T) {
	h := newAcceptHarness(false, false)
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	if len(h.clock.timers) != 0 {
		t.Fatal("fallback armed before relay transport was ready")
	}
	h.eng.mu.Lock()
	h.eng.calls[h.call.id].relay = &relayData{}
	h.eng.mu.Unlock()
	h.eng.armIncomingAcceptFallback(h.call.id)
	timer := h.clock.Timer(t, 0)
	timer.Fire()
	timer.Fire()
	if err := h.eng.trySendFinalAccept(h.call.id, AcceptTriggerFallback); err != nil {
		t.Fatal(err)
	}
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("accept count = %d, want 1", got)
	}
}

func TestIncomingAcceptConcurrentMuteAndFallbackSendsOnce(t *testing.T) {
	h := newAcceptHarness(true, true)
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	timer := h.clock.Timer(t, 0)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); timer.Fire() }()
	go func() { defer wg.Done(); h.eng.onCallRaw(h.muteNode()) }()
	wg.Wait()
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("accept count = %d, want 1", got)
	}
}

func TestIncomingAcceptAnswerIsIdempotent(t *testing.T) {
	h := newAcceptHarness(false, true)
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	if len(h.clock.timers) != 1 {
		t.Fatalf("timer count = %d, want 1", len(h.clock.timers))
	}
	h.clock.Timer(t, 0).Fire()
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("accept count = %d, want 1", got)
	}
}

func TestIncomingAcceptCleanupCancelsFallback(t *testing.T) {
	tests := []struct {
		name string
		end  func(*acceptHarness)
	}{
		{"local_reject", func(h *acceptHarness) { _ = h.call.Reject() }},
		{"remote_terminate", func(h *acceptHarness) { h.eng.onTerminate(h.call.id, "remote") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newAcceptHarness(false, true)
			if err := h.call.Answer(); err != nil {
				t.Fatal(err)
			}
			timer := h.clock.Timer(t, 0)
			tt.end(h)
			timer.Fire()
			h.eng.onCallRaw(h.muteNode())
			if got := h.acceptCount(); got != 0 {
				t.Fatalf("accept count after cleanup = %d, want 0", got)
			}
			if h.eng.lookup(h.call.id) != nil {
				t.Fatal("call remained registered after cleanup")
			}
			if h.endCount != 1 {
				t.Fatalf("end callbacks = %d, want 1", h.endCount)
			}
		})
	}
}

func TestIncomingAcceptSendFailureIsTypedAndNotRetried(t *testing.T) {
	h := newAcceptHarness(false, true)
	h.sendErr = errors.New("write failed")
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	timer := h.clock.Timer(t, 0)
	timer.Fire()
	timer.Fire()
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
	if h.eng.lookup(h.call.id) != nil || h.call.State() != CallPhaseEnded {
		t.Fatal("failed accept did not clean up the call")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	foundTypedFailure := false
	for _, event := range h.events {
		var typed *IncomingAcceptError
		if errors.As(event.Err, &typed) && typed.Kind == "accept_send_failed" {
			foundTypedFailure = true
		}
	}
	if !foundTypedFailure {
		t.Fatal("typed incoming accept failure event missing")
	}
}

func TestIncomingAcceptEndDuringSendCancelsIOAndDoesNotCommit(t *testing.T) {
	h := newAcceptHarness(false, true)
	started := make(chan struct{})
	h.sendFn = func(ctx context.Context, _ waBinary.Node) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	if err := h.call.Answer(); err != nil {
		t.Fatal(err)
	}
	timer := h.clock.Timer(t, 0)
	done := make(chan struct{})
	go func() {
		timer.Fire()
		close(done)
	}()
	<-started
	h.eng.onTerminate(h.call.id, "remote")
	<-done
	if h.eng.lookup(h.call.id) != nil || h.call.State() != CallPhaseEnded {
		t.Fatal("call was not cleaned up during accept send")
	}
	if got := h.acceptCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
}

func TestIncomingVoiceAndVideoUseTheSameFinalAcceptHandshake(t *testing.T) {
	for _, video := range []bool{false, true} {
		t.Run(map[bool]string{false: "voice", true: "video"}[video], func(t *testing.T) {
			h := newAcceptHarness(video, true)
			h.eng.onCallRaw(h.muteNode())
			if err := h.call.Answer(); err != nil {
				t.Fatal(err)
			}
			stanza := h.firstAccept(t)
			accept := stanza.GetChildren()[0]
			hasVideo := false
			for _, child := range accept.GetChildren() {
				if child.Tag == "video" {
					hasVideo = true
				}
			}
			if hasVideo {
				t.Fatal("final accept included an experimental video child")
			}
		})
	}
}
