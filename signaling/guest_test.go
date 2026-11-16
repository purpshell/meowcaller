package signaling

import (
	"testing"

	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/types"
)

func TestWaitingRoomUsersCarryGuestKindAndName(t *testing.T) {
	creator := types.JID{User: "100001", Server: types.HiddenUserServer, Device: 1}
	guest := types.NewJID("200002", types.HiddenUserServer)
	member := types.NewJID("300003", types.HiddenUserServer)
	unnamed := types.NewJID("400004", types.HiddenUserServer)
	action := waBinary.Node{
		Tag:   "waiting_room_update",
		Attrs: waBinary.Attrs{"call-id": "CID", "call-creator": creator},
		Content: []waBinary.Node{{
			Tag: "waiting_room",
			Attrs: waBinary.Attrs{
				"call-id": "CID", "call-creator": creator, "link-token": "TOKEN",
				"media": "video", "enabled": "1", "is_admin": "1",
			},
			Content: []waBinary.Node{
				{Tag: "user", Attrs: waBinary.Attrs{
					"jid": guest, "state": "waiting_room_joined",
					"account_kind": "guest", "guest_name": "Visitor",
				}},
				{Tag: "user", Attrs: waBinary.Attrs{
					"jid": member, "state": "waiting_room_joined", "push_name": "Member",
				}},
				{Tag: "user", Attrs: waBinary.Attrs{"jid": unnamed, "state": "waiting_room_left"}},
			},
		}},
	}
	room, err := ParseWaitingRoomUpdate(&action)
	if err != nil {
		t.Fatalf("ParseWaitingRoomUpdate: %v", err)
	}
	if len(room.Users) != 3 {
		t.Fatalf("users = %+v", room.Users)
	}
	if got := room.Users[0]; !got.IsGuest() || got.AccountKind != AccountKindGuest || got.Name != "Visitor" {
		t.Fatalf("guest = %+v", got)
	}
	if got := room.Users[1]; got.IsGuest() || got.Name != "Member" {
		t.Fatalf("member = %+v", got)
	}
	if got := room.Users[2]; got.IsGuest() || got.Name != "" || got.AccountKind != "" {
		t.Fatalf("unnamed = %+v", got)
	}
}

func TestGroupRosterPrefersPushNameOverGuestName(t *testing.T) {
	creator := types.JID{User: "100001", Server: types.HiddenUserServer, Device: 1}
	guest := types.NewJID("200002", types.HiddenUserServer)
	both := types.NewJID("300003", types.HiddenUserServer)
	action := waBinary.Node{
		Tag:   "group_update",
		Attrs: waBinary.Attrs{"call-id": "CID", "call-creator": creator},
		Content: []waBinary.Node{{
			Tag: "group_info",
			Attrs: waBinary.Attrs{
				"transaction-id": "3", "media": "audio", "connected-limit": "32",
			},
			Content: []waBinary.Node{
				{Tag: "user", Attrs: waBinary.Attrs{
					"jid": guest, "state": "connected", "account_kind": "guest", "guest_name": "Visitor",
				}},
				{Tag: "user", Attrs: waBinary.Attrs{
					"jid": both, "state": "connected", "push_name": "Pushed", "guest_name": "Typed",
				}},
			},
		}},
	}
	update, err := ParseGroupUpdate(&action)
	if err != nil {
		t.Fatalf("ParseGroupUpdate: %v", err)
	}
	if len(update.Participants) != 2 {
		t.Fatalf("participants = %+v", update.Participants)
	}
	if got := update.Participants[0]; !got.IsGuest() || got.Name != "Visitor" {
		t.Fatalf("guest = %+v", got)
	}
	if got := update.Participants[1]; got.IsGuest() || got.Name != "Pushed" {
		t.Fatalf("push_name must win over guest_name: %+v", got)
	}
}

func TestCallLinkJoinPushName(t *testing.T) {
	plain, err := BuildCallLinkJoin("TOKEN", CallLinkMediaAudio, "REQ")
	if err != nil {
		t.Fatalf("BuildCallLinkJoin: %v", err)
	}
	if _, ok := plain.GetChildren()[0].Attrs["push_name"]; ok {
		t.Fatalf("default join must not send push_name: %+v", plain.GetChildren()[0].Attrs)
	}
	named, err := BuildCallLinkJoinWithOptions(
		"TOKEN", CallLinkMediaVideo, "REQ", CallLinkJoinOptions{PushName: "  Visitor "},
	)
	if err != nil {
		t.Fatalf("BuildCallLinkJoinWithOptions: %v", err)
	}
	join := named.GetChildren()[0]
	if join.Tag != "link_join" || join.Attrs["push_name"] != "Visitor" ||
		join.Attrs["token"] != "TOKEN" || join.Attrs["media"] != "video" {
		t.Fatalf("link_join = %+v", join)
	}
	blank, err := BuildCallLinkJoinWithOptions("TOKEN", CallLinkMediaAudio, "REQ", CallLinkJoinOptions{PushName: "   "})
	if err != nil {
		t.Fatalf("BuildCallLinkJoinWithOptions blank: %v", err)
	}
	if _, ok := blank.GetChildren()[0].Attrs["push_name"]; ok {
		t.Fatal("a blank name must not be sent")
	}
}
