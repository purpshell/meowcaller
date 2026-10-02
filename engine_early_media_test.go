package meowcaller

import (
	"testing"

	waBinary "github.com/polymorfa/hypermeow/binary"
)

func testEngineWithIncomingCall() (*engine, *Call, *int) {
	c := &Client{}
	c.eng = newEngine(c)
	call := &Call{eng: c.eng, id: "CID", peer: peerJID(), phase: CallPhaseRinging}
	ready := 0
	call.OnReady(func() { ready++ })
	c.eng.calls["CID"] = &engineCall{
		call:      call,
		direction: CallDirectionIncoming,
		from:      peerJID(),
		creator:   creatorJID(),
	}
	return c.eng, call, &ready
}

func TestFirstInboundRTPWhileRingingWaitsForAnswer(t *testing.T) {
	eng, call, ready := testEngineWithIncomingCall()

	eng.onFirstInboundRTP("CID", call)
	if call.State() != CallPhaseRinging || *ready != 0 {
		t.Fatalf("before answer: phase=%v ready=%d, want ringing and not ready", call.State(), *ready)
	}

	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if call.State() != CallPhaseActive || *ready != 1 {
		t.Fatalf("after answer: phase=%v ready=%d, want active and ready once", call.State(), *ready)
	}
}

func TestFirstInboundRTPAfterAnswerActivates(t *testing.T) {
	eng, call, ready := testEngineWithIncomingCall()

	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if call.State() != CallPhaseConnecting || *ready != 0 {
		t.Fatalf("after answer: phase=%v ready=%d, want connecting", call.State(), *ready)
	}

	eng.onFirstInboundRTP("CID", call)
	if call.State() != CallPhaseActive || *ready != 1 {
		t.Fatalf("after first RTP: phase=%v ready=%d, want active and ready once", call.State(), *ready)
	}
}

func TestMuteV2WhileRingingIsKeptForAnswer(t *testing.T) {
	eng, call, _ := testEngineWithIncomingCall()
	node := &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": peerJID(), "id": "N1"},
		Content: []waBinary.Node{{Tag: "mute_v2", Attrs: waBinary.Attrs{
			"call-id": "CID", "call-creator": creatorJID(), "mute-state": "0",
		}}},
	}

	if handled := eng.onCallRaw(node); handled {
		t.Fatal("mute_v2 while ringing should not be consumed")
	}
	m := eng.calls[call.ID()]
	if !m.earlyMuteSeen || m.earlyMuteFrom != peerJID() || m.earlyMuteCreator != creatorJID() {
		t.Fatalf("early mute not recorded: seen=%v from=%s creator=%s", m.earlyMuteSeen, m.earlyMuteFrom, m.earlyMuteCreator)
	}
	if call.State() != CallPhaseRinging {
		t.Fatalf("phase = %v, want ringing", call.State())
	}
}
