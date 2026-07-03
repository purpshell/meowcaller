package meowcaller

import (
	"context"
	"testing"
)

func TestCallMediaSinksDefaultOff(t *testing.T) {
	call := &Call{}
	if _, sink := callPlayerSink(call); sink != nil {
		t.Fatal("new call unexpectedly has an inbound audio sink")
	}
	if sink := callVideoSink(call); sink != nil {
		t.Fatal("new call unexpectedly has an inbound video sink")
	}

	audioSink := SinkFunc(func([]float32) {})
	videoSink := VideoSinkFunc(func([]byte) {})
	call.Receive(audioSink)
	call.ReceiveVideo(videoSink)
	if _, sink := callPlayerSink(call); sink == nil {
		t.Fatal("Receive did not enable inbound audio")
	}
	if sink := callVideoSink(call); sink == nil {
		t.Fatal("ReceiveVideo did not enable inbound video")
	}
}

// regSession builds an outgoing session for the registry tests (reuses the JID
// helpers from session_test.go in this package).
func regSession(id string) *CallSession {
	return NewOutgoingSession(id, peerJID(), creatorJID())
}

func isCancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// TestInsertTransitionRemove pins the registry bookkeeping contract.
func TestInsertTransitionRemove(t *testing.T) {
	reg := NewCallRegistry()
	if !reg.Insert(regSession("CID")) {
		t.Fatal("first insert should succeed")
	}
	if reg.Insert(regSession("CID")) {
		t.Error("duplicate insert should fail")
	}
	if ph, ok := reg.Phase("CID"); !ok || ph != CallPhaseIdle {
		t.Errorf("phase = (%d, %v), want (Idle, true)", ph, ok)
	}
	if !reg.Transition("CID", CallPhaseCalling) {
		t.Error("legal transition rejected")
	}
	if ph, _ := reg.Phase("CID"); ph != CallPhaseCalling {
		t.Errorf("phase = %d, want Calling", ph)
	}
	if reg.Transition("UNKNOWN", CallPhaseCalling) {
		t.Error("transition on unknown call should fail")
	}
	if !reg.Remove("CID") {
		t.Error("remove of existing call should return true")
	}
	if reg.Remove("CID") {
		t.Error("remove of absent call should return false")
	}
	if reg.ActiveCount() != 0 {
		t.Errorf("active count = %d, want 0", reg.ActiveCount())
	}
}

// TestRemoveCancelsMediaTask confirms Remove cancels the call's media task.
func TestRemoveCancelsMediaTask(t *testing.T) {
	reg := NewCallRegistry()
	reg.Insert(regSession("A"))
	ctx, cancel := context.WithCancel(context.Background())
	reg.SetMediaTask("A", cancel)
	if !reg.Remove("A") {
		t.Fatal("remove failed")
	}
	if !isCancelled(ctx) {
		t.Error("removed call's media task must be cancelled")
	}
}

// TestAbortAllCancelsMediaTasks confirms AbortAll cancels every task and empties the registry.
func TestAbortAllCancelsMediaTasks(t *testing.T) {
	reg := NewCallRegistry()
	reg.Insert(regSession("A"))
	reg.Insert(regSession("B"))
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	reg.SetMediaTask("A", cancelA)
	reg.SetMediaTask("B", cancelB)
	if reg.AbortAll() != 2 {
		t.Errorf("AbortAll returned %d, want 2", reg.AbortAll())
	}
	if !isCancelled(ctxA) || !isCancelled(ctxB) {
		t.Error("AbortAll must cancel every media task")
	}
	if reg.ActiveCount() != 0 {
		t.Error("AbortAll must empty the registry")
	}
}

// TestReplaceCancelsOldMediaTask confirms a replacing SetMediaTask cancels the prior handle.
func TestReplaceCancelsOldMediaTask(t *testing.T) {
	reg := NewCallRegistry()
	reg.Insert(regSession("A"))
	oldCtx, oldCancel := context.WithCancel(context.Background())
	reg.SetMediaTask("A", oldCancel)
	newCtx, newCancel := context.WithCancel(context.Background())
	reg.SetMediaTask("A", newCancel)
	if !isCancelled(oldCtx) {
		t.Error("replaced media task must be cancelled")
	}
	if isCancelled(newCtx) {
		t.Error("replacement task must stay live")
	}
	reg.Remove("A")
	if !isCancelled(newCtx) {
		t.Error("replacement cancelled on remove")
	}
}

// TestSetMediaTaskOnUnknownCallCancels confirms an orphan handle is cancelled immediately.
func TestSetMediaTaskOnUnknownCallCancels(t *testing.T) {
	reg := NewCallRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	reg.SetMediaTask("GONE", cancel) // never inserted
	if !isCancelled(ctx) {
		t.Error("orphan media task must be cancelled immediately")
	}
}
