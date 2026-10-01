package meowcaller

import (
	"bytes"
	"testing"

	"github.com/polymorfa/hypermeow/types"
)

func TestGroupStateSnapshotsOwnNestedSlicesAndRejectStaleTransactions(t *testing.T) {
	participant := types.NewJID("100", types.HiddenUserServer)
	call := &Call{}
	state := GroupCallState{
		TransactionID: 2,
		Participants: []GroupCallParticipant{{
			JID: participant,
			Devices: []GroupCallDevice{{
				JID: types.NewADJID("100", 0, 1),
				PID: 7, HasPID: true,
			}},
		}},
	}
	call.setGroupState(state)

	state.Participants[0].Devices[0].PID = 99
	snapshot, ok := call.GroupState()
	if !ok || snapshot.Participants[0].Devices[0].PID != 7 {
		t.Fatalf("group snapshot aliases caller memory: %#v", snapshot)
	}
	snapshot.Participants[0].Devices[0].PID = 42
	again, _ := call.GroupState()
	if again.Participants[0].Devices[0].PID != 7 {
		t.Fatalf("group snapshot aliases stored state: %#v", again)
	}

	call.setGroupState(GroupCallState{TransactionID: 1})
	again, _ = call.GroupState()
	if again.TransactionID != 2 {
		t.Fatalf("stale transaction replaced state: %#v", again)
	}
}

func TestGroupStateAndWaitingRoomListenersReplayCurrentSnapshots(t *testing.T) {
	call := &Call{}
	call.setGroupState(GroupCallState{TransactionID: 4})
	call.setWaitingRoomState(WaitingRoomState{
		Enabled: true, TransactionID: 8,
		Users: []WaitingRoomUser{{State: "waiting_room_joined"}},
	})

	var groupTransaction uint32
	call.OnGroupState(func(state GroupCallState) {
		groupTransaction = state.TransactionID
	})
	var waiting WaitingRoomState
	call.OnWaitingRoomState(func(state WaitingRoomState) {
		waiting = state
	})

	if groupTransaction != 4 {
		t.Fatalf("group replay transaction = %d, want 4", groupTransaction)
	}
	if waiting.TransactionID != 8 || !waiting.Enabled || len(waiting.Users) != 1 {
		t.Fatalf("waiting-room replay = %#v", waiting)
	}
}

func TestGroupAndWaitingRoomListenersAllowReentrantStateChanges(t *testing.T) {
	call := &Call{}
	groupTransactions := make([]uint32, 0, 2)
	call.OnGroupState(func(state GroupCallState) {
		groupTransactions = append(groupTransactions, state.TransactionID)
		if state.TransactionID == 1 {
			call.setGroupState(GroupCallState{TransactionID: 2})
		}
	})
	call.setGroupState(GroupCallState{TransactionID: 1})
	if len(groupTransactions) != 2 || !bytes.Equal(
		[]byte{byte(groupTransactions[0]), byte(groupTransactions[1])}, []byte{1, 2},
	) {
		t.Fatalf("reentrant group transactions = %v", groupTransactions)
	}

	waitingStates := make([]bool, 0, 2)
	call.OnWaitingRoomState(func(state WaitingRoomState) {
		waitingStates = append(waitingStates, state.InWaitingRoom)
		if state.InWaitingRoom {
			call.setWaitingRoomAdmission()
		}
	})
	call.setWaitingRoomState(WaitingRoomState{
		InWaitingRoom: true,
		TransactionID: 1,
	})
	if len(waitingStates) != 2 || !waitingStates[0] || waitingStates[1] {
		t.Fatalf("reentrant waiting-room states = %v", waitingStates)
	}
}

func TestAuthoritativeTransactionZeroReplacesSeededGroupState(t *testing.T) {
	call := &Call{
		groupState: &GroupCallState{
			Participants: []GroupCallParticipant{{State: "selected"}},
		},
	}
	call.setGroupState(GroupCallState{
		Participants: []GroupCallParticipant{{State: "connected"}},
	})
	state, ok := call.GroupState()
	if !ok || len(state.Participants) != 1 || state.Participants[0].State != "connected" {
		t.Fatalf("transaction-zero authoritative state = %#v", state)
	}
}

func TestHandAndScreenShareStateUpdatesPublicSnapshots(t *testing.T) {
	participant := types.NewJID("200", types.HiddenUserServer)
	call := &Call{
		groupState: &GroupCallState{
			TransactionID: 1,
			Participants:  []GroupCallParticipant{{JID: participant}},
		},
	}
	var hand HandRaiseState
	var screen ScreenShareState
	call.OnHandRaise(func(state HandRaiseState) { hand = state })
	call.OnScreenShare(func(state ScreenShareState) { screen = state })

	call.dispatchHandRaise(HandRaiseState{Participant: participant, Raised: true})
	call.dispatchScreenShare(ScreenShareState{
		Participant: participant, Active: true, Version: 2,
		ScreenShareID: 1, HasScreenShareID: true,
	})

	group, _ := call.GroupState()
	if !group.Participants[0].HandRaised || !hand.Raised {
		t.Fatalf("hand state not reflected in roster: %#v, %#v", group, hand)
	}
	shares := call.ScreenShares()
	if len(shares) != 1 || shares[0].Participant != participant ||
		shares[0].ScreenShareID != 1 || screen.Version != 2 {
		t.Fatalf("screen-share state = %#v, callback = %#v", shares, screen)
	}

	call.dispatchScreenShare(ScreenShareState{Participant: participant})
	if shares = call.ScreenShares(); len(shares) != 0 {
		t.Fatalf("stopped screen share remained active: %#v", shares)
	}
}

func TestParticipantVideoFrameCallbackOwnsAccessUnit(t *testing.T) {
	call := &Call{}
	var got ParticipantVideoFrame
	call.OnParticipantVideoFrame(func(frame ParticipantVideoFrame) {
		got = frame
	})
	accessUnit := []byte{0, 0, 0, 1, 0x65, 1}
	want := ParticipantVideoFrame{
		ParticipantID: "300:4@lid",
		Sender:        types.NewJID("300", types.HiddenUserServer),
		Device:        types.NewADJID("300", 0, 4),
		PID:           9,
		HasPID:        true,
		SSRC:          0x12345678,
		Orientation:   3,
		AccessUnit:    accessUnit,
	}
	if !call.dispatchParticipantVideoFrame(want) {
		t.Fatal("participant video callback was not dispatched")
	}
	accessUnit[4] = 0
	if !bytes.Equal(got.AccessUnit, []byte{0, 0, 0, 1, 0x65, 1}) {
		t.Fatalf("participant frame aliases media buffer: %x", got.AccessUnit)
	}
	if got.ParticipantID != want.ParticipantID || got.Device != want.Device ||
		got.PID != want.PID || got.SSRC != want.SSRC {
		t.Fatalf("participant frame metadata = %#v, want %#v", got, want)
	}
}

func TestCallLinkTokenNormalizationAndPublicURL(t *testing.T) {
	token, err := normalizeCallLinkToken("https://call.whatsapp.com/video/TOKEN")
	if err != nil || token != "TOKEN" {
		t.Fatalf("normalize URL = (%q, %v)", token, err)
	}
	link := publicCallLink(token, callLinkMediaVideo)
	if link.URL != "https://call.whatsapp.com/video/TOKEN" || !link.Video {
		t.Fatalf("public link = %#v", link)
	}
	link = publicCallLink(token, callLinkMediaAudio)
	if link.URL != "https://call.whatsapp.com/voice/TOKEN" || link.Video {
		t.Fatalf("public audio link = %#v", link)
	}
	for _, raw := range []string{"https://call.whatsapp.com/voice/TOKEN", "https://call.whatsapp.com/audio/TOKEN"} {
		if token, err = normalizeCallLinkToken(raw); err != nil || token != "TOKEN" {
			t.Fatalf("normalize %q = (%q, %v)", raw, token, err)
		}
	}
	for _, invalid := range []string{"", "audio/TOKEN", "http://call.whatsapp.com/audio/TOKEN"} {
		if _, err = normalizeCallLinkToken(invalid); err == nil {
			t.Fatalf("accepted invalid call link %q", invalid)
		}
	}
}

func TestRemoteGroupCallTargetsExcludeSelfAndAliases(t *testing.T) {
	self := types.NewJID("1", types.HiddenUserServer)
	first := types.NewJID("2", types.HiddenUserServer)
	firstPN := types.NewJID("22", types.DefaultUserServer)
	second := types.NewJID("3", types.HiddenUserServer)
	phoneOnly := types.NewJID("4", types.DefaultUserServer)
	got := remoteGroupCallTargets(
		[]types.GroupParticipant{
			{JID: self, LID: self},
			{JID: first, LID: first, PhoneNumber: firstPN},
			{JID: firstPN, LID: first, PhoneNumber: firstPN},
			{JID: second, LID: second},
			{PhoneNumber: phoneOnly},
		},
		[]types.JID{self},
	)
	if len(got) != 3 ||
		got[0] != first.String() ||
		got[1] != second.String() ||
		got[2] != phoneOnly.String() {
		t.Fatalf("remote targets = %v", got)
	}
}

func TestWaitingRoomPhaseTransitions(t *testing.T) {
	session := NewOutgoingSession(
		"CALL",
		types.NewJID("2", types.HiddenUserServer),
		types.NewJID("1", types.HiddenUserServer),
	)
	if !session.TransitionTo(CallPhaseCalling) ||
		!session.TransitionTo(CallPhaseWaitingRoom) ||
		!session.TransitionTo(CallPhaseConnecting) {
		t.Fatalf("waiting-room transition stopped at %v", session.Phase())
	}
}
