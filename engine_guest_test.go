package meowcaller

import (
	"testing"

	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/types"
)

func TestHostSeesGuestsInWaitingRoomAndRoster(t *testing.T) {
	eng, call, creator := testGroupEngine("GROUP")
	callJID := types.NewJID("GROUP", "call")
	guest := types.NewJID("300", types.HiddenUserServer)
	member := types.NewJID("400", types.HiddenUserServer)

	eng.onUnknownCallEvent(&waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": callJID, "id": "WAIT", "t": "1"},
		Content: []waBinary.Node{{
			Tag:   "waiting_room_update",
			Attrs: waBinary.Attrs{"call-id": "GROUP", "call-creator": creator},
			Content: []waBinary.Node{{
				Tag: "waiting_room",
				Attrs: waBinary.Attrs{
					"call-id": "GROUP", "call-creator": creator, "link-token": "TOKEN",
					"media": "audio", "enabled": "1", "is_admin": "1", "transaction-id": "2",
				},
				Content: []waBinary.Node{
					{Tag: "user", Attrs: waBinary.Attrs{
						"jid": guest, "state": "waiting_room_joined",
						"account_kind": "guest", "guest_name": "Visitor",
					}},
					{Tag: "user", Attrs: waBinary.Attrs{
						"jid": member, "state": "waiting_room_joined", "push_name": "Member",
					}},
				},
			}},
		}},
	})
	waiting, ok := call.WaitingRoomState()
	if !ok || len(waiting.Users) != 2 {
		t.Fatalf("waiting room = %#v, present=%v", waiting, ok)
	}
	if !waiting.Users[0].Guest || waiting.Users[0].Name != "Visitor" {
		t.Fatalf("guest waiting user = %#v", waiting.Users[0])
	}
	if waiting.Users[1].Guest || waiting.Users[1].Name != "Member" {
		t.Fatalf("member waiting user = %#v", waiting.Users[1])
	}

	eng.onUnknownCallEvent(&waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": callJID, "id": "ROSTER", "t": "2"},
		Content: []waBinary.Node{{
			Tag:   "group_update",
			Attrs: waBinary.Attrs{"call-id": "GROUP", "call-creator": creator},
			Content: []waBinary.Node{{
				Tag: "group_info",
				Attrs: waBinary.Attrs{
					"transaction-id": "3", "media": "audio", "connected-limit": "32",
				},
				Content: []waBinary.Node{{Tag: "user", Attrs: waBinary.Attrs{
					"jid": guest, "state": "connected",
					"account_kind": "guest", "guest_name": "Visitor",
				}}},
			}},
		}},
	})
	state, ok := call.GroupState()
	if !ok || len(state.Participants) != 1 {
		t.Fatalf("group state = %#v, present=%v", state, ok)
	}
	if got := state.Participants[0]; got.JID != guest || !got.Guest || got.Name != "Visitor" {
		t.Fatalf("roster guest = %#v", got)
	}
}
