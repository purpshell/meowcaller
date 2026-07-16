package meowcaller

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func testEngineWithOutgoingCall() (*engine, *Call) {
	c := &Client{}
	c.eng = newEngine(c)
	call := &Call{id: "CID", peer: peerJID(), phase: CallPhaseCalling}
	c.eng.calls["CID"] = &engineCall{
		call:      call,
		direction: CallDirectionOutgoing,
		from:      peerJID(),
		creator:   creatorJID(),
		isVideo:   true,
	}
	return c.eng, call
}

func TestOutgoingPeerAcceptLifecycle(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	from := types.JID{User: "222222222222222", Server: types.HiddenUserServer}

	eng.onPreAccept(&events.CallPreAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: from},
	})
	if got := call.State(); got != CallPhaseRinging {
		t.Fatalf("after preaccept phase = %d, want Ringing", got)
	}

	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: from},
		Data:          &waBinary.Node{Tag: "accept"},
	})
	if got := call.State(); got != CallPhaseConnecting {
		t.Fatalf("after accept phase = %d, want Connecting", got)
	}
}

func TestOutgoingPeerAcceptDoesNotRegressActiveCall(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	call.setPhase(CallPhaseActive)

	eng.onPreAccept(&events.CallPreAccept{BasicCallMeta: types.BasicCallMeta{CallID: "CID"}})
	eng.onAccept(&events.CallAccept{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID"},
		Data:          &waBinary.Node{Tag: "accept"},
	})
	if got := call.State(); got != CallPhaseActive {
		t.Fatalf("phase = %d, want Active", got)
	}
}

func TestPeerRejectEndsCall(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var reason string
	call.OnEnd(func(r string) { reason = r })

	eng.onReject(&events.CallReject{BasicCallMeta: types.BasicCallMeta{CallID: "CID"}})
	if got := call.State(); got != CallPhaseEnded {
		t.Fatalf("phase = %d, want Ended", got)
	}
	if reason != "rejected" {
		t.Fatalf("reason = %q, want rejected", reason)
	}
}
