package meowcaller

import (
	"context"
	"testing"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
	"github.com/polymorfa/hypermeow"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
	waLog "github.com/polymorfa/hypermeow/util/log"
)

func testGroupEngine(callID string) (*engine, *Call, types.JID) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/676ebee3eca513b5348fab36cae5c560cc791238/datasheets/voip-group-invite-accept.md#L26-L45
	creator := types.NewJID("100", types.HiddenUserServer)
	client := &Client{log: zerolog.Nop()}
	eng := &engine{c: client, calls: make(map[string]*engineCall)}
	client.eng = eng
	call := &Call{
		eng: eng, id: callID, peer: creator, phase: CallPhaseRinging,
		groupState: &GroupCallState{},
	}
	eng.calls[callID] = &engineCall{
		call: call, group: true, creator: creator,
		from: types.NewJID(callID, "call"), direction: CallDirectionIncoming,
	}
	return eng, call, creator
}

func TestGroupAnswerSendsImmediateCallScopedAccept(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/676ebee3eca513b5348fab36cae5c560cc791238/datasheets/voip-group-invite-accept.md#L26-L45
	eng, call, creator := testGroupEngine("GROUP")
	var sent waBinary.Node
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = node
		return nil
	}

	if err := call.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if call.State() != CallPhaseConnecting {
		t.Fatalf("phase = %d, want connecting", call.State())
	}
	if sent.AttrGetter().JID("to") != types.NewJID("GROUP", "call") {
		t.Fatalf("accept target = %s, want GROUP@call", sent.AttrGetter().JID("to"))
	}
	children := sent.GetChildren()
	if len(children) != 1 || children[0].Tag != "accept" {
		t.Fatalf("accept envelope = %#v", sent)
	}
	attrs := children[0].AttrGetter()
	if attrs.String("call-id") != "GROUP" || attrs.JID("call-creator") != creator {
		t.Fatalf("accept identity = %#v", children[0].Attrs)
	}
	if eng.calls["GROUP"].acceptPending {
		t.Fatal("group accept was incorrectly deferred to mute_v2")
	}
}

func TestInitialGroupAckPublishesAuthoritativeRoster(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7cb6045001dafd2514f53e85cd8c3e419c13adbe/datasheets/voip-initial-group-call.md#L27-L33
	eng, call, creator := testGroupEngine("GROUP")
	eng.calls["GROUP"].direction = CallDirectionOutgoing
	participant := types.NewJID("200", types.HiddenUserServer)
	device := types.NewADJID("200", 0, 3)
	ack := &waBinary.Node{
		Tag: "ack",
		Attrs: waBinary.Attrs{
			"class": "call", "type": "offer", "id": "REQUEST",
		},
		Content: []waBinary.Node{{
			Tag: "group_info",
			Attrs: waBinary.Attrs{
				"call-id": "GROUP", "call-creator": creator,
				"transaction-id": "7", "connected-limit": "32",
				"media": "audio", "joinable": "1",
			},
			Content: []waBinary.Node{{
				Tag: "user",
				Attrs: waBinary.Attrs{
					"jid": participant, "state": "connected",
				},
				Content: []waBinary.Node{{
					Tag: "device",
					Attrs: waBinary.Attrs{
						"jid": device, "pid": "4",
					},
				}},
			}},
		}},
	}

	eng.onCallAck(ack)

	state, ok := call.GroupState()
	if !ok || state.TransactionID != 7 || len(state.Participants) != 1 {
		t.Fatalf("group state = %#v, present=%v", state, ok)
	}
	got := state.Participants[0]
	if got.JID != participant || got.State != "connected" ||
		len(got.Devices) != 1 || got.Devices[0].PID != 4 || !got.Devices[0].HasPID {
		t.Fatalf("participant state = %#v", got)
	}
}

func TestUnknownCallControlsUpdateHandScreenAndWaitingRoom(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L31-L56
	eng, call, creator := testGroupEngine("GROUP")
	participant := types.NewJID("200", types.HiddenUserServer)
	call.setGroupState(GroupCallState{
		TransactionID: 1,
		Participants:  []GroupCallParticipant{{JID: participant}},
	})

	handNode := signaling.BuildRaiseHand(
		"GROUP", types.NewJID("GROUP", "call"), creator, "HAND", true,
	)
	handNode.Attrs["from"] = types.NewJID("GROUP", "call")
	handNode.Attrs["participant"] = participant
	handNode.Attrs["t"] = "1"
	eng.onUnknownCallEvent(&handNode)

	state, _ := call.GroupState()
	if !state.Participants[0].HandRaised {
		t.Fatal("remote hand state was not reflected in the group roster")
	}

	screenID := uint32(9)
	screenNode := signaling.BuildScreenShare(
		"GROUP", types.NewJID("GROUP", "call"), creator, "SCREEN",
		signaling.ScreenShareStarted, &screenID,
	)
	screenNode.Attrs["from"] = types.NewJID("GROUP", "call")
	screenNode.Attrs["participant"] = participant
	screenNode.Attrs["t"] = "2"
	eng.onUnknownCallEvent(&screenNode)
	shares := call.ScreenShares()
	if len(shares) != 1 || shares[0].Participant != participant ||
		!shares[0].Active || shares[0].ScreenShareID != screenID {
		t.Fatalf("screen shares = %#v", shares)
	}

	waitingUser := types.NewJID("300", types.HiddenUserServer)
	waitingNode := &waBinary.Node{
		Tag: "call",
		Attrs: waBinary.Attrs{
			"from": types.NewJID("GROUP", "call"), "id": "WAIT", "t": "3",
		},
		Content: []waBinary.Node{{
			Tag: "waiting_room_update",
			Attrs: waBinary.Attrs{
				"call-id": "GROUP", "call-creator": creator,
			},
			Content: []waBinary.Node{{
				Tag: "waiting_room",
				Attrs: waBinary.Attrs{
					"call-id": "GROUP", "call-creator": creator,
					"link-token": "TOKEN", "media": "audio",
					"enabled": "1", "is_admin": "1", "transaction-id": "8",
				},
				Content: []waBinary.Node{{
					Tag: "user",
					Attrs: waBinary.Attrs{
						"jid": waitingUser, "state": "waiting_room_joined",
					},
				}},
			}},
		}},
	}
	eng.onUnknownCallEvent(waitingNode)
	waiting, ok := call.WaitingRoomState()
	if !ok || !waiting.Enabled || !waiting.IsAdmin || waiting.TransactionID != 8 ||
		len(waiting.Users) != 1 || waiting.Users[0].JID != waitingUser {
		t.Fatalf("waiting room = %#v, present=%v", waiting, ok)
	}
}

func TestRawGroupControlSendsTypedAckWithoutUpstreamDoubleHandling(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L31-L56
	eng, call, creator := testGroupEngine("GROUP")
	participant := types.NewJID("200", types.HiddenUserServer)
	var sent waBinary.Node
	var order []string
	call.OnHandRaise(func(HandRaiseState) {
		order = append(order, "callback")
	})
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		order = append(order, "ack")
		sent = node
		return nil
	}
	node := signaling.BuildRaiseHand(
		"GROUP", types.NewJID("GROUP", "call"), creator, "HAND", true,
	)
	node.Attrs["from"] = types.NewJID("GROUP", "call")
	node.Attrs["participant"] = participant
	node.Attrs["t"] = "1"

	if !eng.onCallRaw(&node) {
		t.Fatal("known raw group control was passed to upstream for a second handling")
	}
	attrs := sent.AttrGetter()
	if sent.Tag != "ack" || attrs.String("class") != "call" ||
		attrs.String("type") != "user_action" || attrs.String("id") != "HAND" ||
		attrs.JID("participant") != participant {
		t.Fatalf("typed ack = %#v", sent)
	}
	if len(order) != 2 || order[0] != "ack" || order[1] != "callback" {
		t.Fatalf("group control order = %v, want [ack callback]", order)
	}
}

func TestCallLinkJoinInstallsFollowingGroupUpdate(t *testing.T) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L76-L155
	wa := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	client := &Client{wa: wa, log: zerolog.Nop()}
	eng := newEngine(client)
	creator := types.NewJID("100", types.HiddenUserServer)
	peer := types.NewJID("200", types.HiddenUserServer)
	groupInfo := func(attrs waBinary.Attrs, device waBinary.Attrs) waBinary.Node {
		attrs["call-id"], attrs["call-creator"] = "GROUP", creator
		attrs["transaction-id"], attrs["media"], attrs["connected-limit"] = "7", "audio", "32"
		return waBinary.Node{
			Tag: "group_info", Attrs: attrs,
			Content: []waBinary.Node{{
				Tag: "user", Attrs: waBinary.Attrs{"jid": peer, "state": "connected"},
				Content: []waBinary.Node{{Tag: "device", Attrs: device}},
			}},
		}
	}
	eng.requestCallNode = func(context.Context, waBinary.Node, string) (*waBinary.Node, error) {
		return &waBinary.Node{
			Tag:     "ack",
			Attrs:   waBinary.Attrs{"class": "call", "type": "link_join"},
			Content: []waBinary.Node{groupInfo(waBinary.Attrs{}, waBinary.Attrs{"jid": peer})},
		}, nil
	}
	call, err := eng.joinPublicCallLink(context.Background(), "TOKEN", CallLinkOptions{})
	if err != nil {
		t.Fatalf("join call link: %v", err)
	}
	update := waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": types.NewJID("GROUP", "call")},
		Content: []waBinary.Node{{
			Tag:   "group_update",
			Attrs: waBinary.Attrs{"call-id": "GROUP", "call-creator": creator},
			Content: []waBinary.Node{
				groupInfo(waBinary.Attrs{}, waBinary.Attrs{"jid": peer, "pid": "1"}),
				{
					Tag:     "relay",
					Attrs:   waBinary.Attrs{"transaction-id": "4", "uuid": "relay", "participant_uuid": "participant"},
					Content: []waBinary.Node{{Tag: "key", Content: []byte("relay-key")}},
				},
			},
		}},
	}
	eng.onUnknownCallEvent(&update)

	m := eng.calls["GROUP"]
	if m.groupUpdate == nil || m.groupUpdate.Relay == nil || m.groupUpdate.TransactionID != 7 {
		t.Fatalf("installed group update = %#v", m.groupUpdate)
	}
	state, ok := call.GroupState()
	if !ok || state.TransactionID != 7 || len(state.Participants) != 1 ||
		len(state.Participants[0].Devices) != 1 || !state.Participants[0].Devices[0].HasPID {
		t.Fatalf("group state = %#v, present=%v", state, ok)
	}
}

func TestLateGroupUpdateDoesNotRecreateEndedCall(t *testing.T) {
	eng, _, creator := testGroupEngine("GROUP")
	eng.finishCall("GROUP", "test")
	applied := eng.applyGroupUpdate(groupCallUpdate{
		CallID:        "GROUP",
		CallCreator:   creator,
		TransactionID: 1,
	})
	if applied {
		t.Fatal("late group update was applied")
	}
	eng.mu.Lock()
	_, exists := eng.calls["GROUP"]
	eng.mu.Unlock()
	if exists {
		t.Fatal("late group update recreated a ghost call")
	}
}

func TestDirectCallInviteRosterUsesNegotiatedActiveDevices(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/1ebd064663ac336ff3d1fc65d9baa974148fe73e/datasheets/voip-group-participant-invite.md#L36-L72
	eng, call, creator := testGroupEngine("DIRECT")
	self := types.NewADJID("100", 0, 14)
	peer := types.NewADJID("200", 0, 3)
	call.setPhase(CallPhaseActive)
	m := eng.calls["DIRECT"]
	m.group = false
	m.groupUpdate = nil
	m.selfLID = self.String()
	m.peerLID = peer.String()
	m.creator = creator
	m.localVideo = true
	m.inviteSelfDevice = groupCallDevice{
		JID: self, CapabilityVersion: 1, Capability: []byte{1, 2, 3},
	}
	m.invitePeerDevice = groupCallDevice{
		JID: peer, CapabilityVersion: 1, Capability: []byte{4, 5, 6},
	}

	gotCreator, participants, video, err := eng.groupInviteRoster("DIRECT")
	if err != nil {
		t.Fatalf("groupInviteRoster: %v", err)
	}
	if gotCreator != creator || !video || len(participants) != 2 {
		t.Fatalf("roster header = (%s, %v, %d)", gotCreator, video, len(participants))
	}
	if participants[0].JID != self.ToNonAD() ||
		participants[0].Devices[0].JID != self ||
		participants[1].JID != peer.ToNonAD() ||
		participants[1].Devices[0].JID != peer {
		t.Fatalf("direct invite roster = %#v", participants)
	}
	m.invitePeerDevice.Capability[0] = 0xff
	if participants[1].Devices[0].Capability[0] != 4 {
		t.Fatal("public signaling roster aliases retained capability bytes")
	}
}

func TestInviteDeviceCapabilityCopiesNegotiatedValue(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/1ebd064663ac336ff3d1fc65d9baa974148fe73e/datasheets/voip-group-participant-invite.md#L36-L72
	device := types.NewADJID("200", 0, 3)
	raw := []byte{1, 5, 0xf7, 9}
	node := &waBinary.Node{
		Tag: "preaccept",
		Content: []waBinary.Node{{
			Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: raw,
		}},
	}
	got, ok := inviteDeviceCapability(device, node)
	if !ok || got.JID != device || got.CapabilityVersion != 1 {
		t.Fatalf("negotiated capability = %#v, present=%v", got, ok)
	}
	raw[0] = 0xff
	if got.Capability[0] != 1 {
		t.Fatal("negotiated capability aliases stanza bytes")
	}
}
