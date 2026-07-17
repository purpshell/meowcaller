package meowcaller

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func reactionEvent(callID, emoji string, sender types.JID) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Sender: sender}},
		Message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key:  &waCommon.MessageKey{ID: proto.String(callID)},
			Text: proto.String(emoji),
		}},
	}
}

func TestCallSendsReactionToCallCreator(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var gotChat, gotSender types.JID
	var gotID types.MessageID
	var gotEmoji string
	eng.buildReaction = func(chat, sender types.JID, id types.MessageID, emoji string) *waE2E.Message {
		gotChat, gotSender, gotID, gotEmoji = chat, sender, id, emoji
		return &waE2E.Message{}
	}
	eng.sendMessage = func(context.Context, types.JID, *waE2E.Message) error { return nil }

	if err := call.SendReaction("👍"); err != nil {
		t.Fatalf("SendReaction: %v", err)
	}
	if gotChat != call.Peer() || gotSender != creatorJID() || gotID != types.MessageID(call.ID()) || gotEmoji != "👍" {
		t.Fatalf("reaction target = chat:%s sender:%s id:%s emoji:%s", gotChat, gotSender, gotID, gotEmoji)
	}
}

func TestIncomingCallReactionTargetsRemoteCreator(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	eng.calls[call.ID()].direction = CallDirectionIncoming
	eng.calls[call.ID()].creator = call.Peer()
	var gotSender types.JID
	eng.buildReaction = func(_ types.JID, sender types.JID, _ types.MessageID, _ string) *waE2E.Message {
		gotSender = sender
		return &waE2E.Message{}
	}
	eng.sendMessage = func(context.Context, types.JID, *waE2E.Message) error { return nil }

	if err := call.SendReaction("❤️"); err != nil {
		t.Fatalf("SendReaction: %v", err)
	}
	if gotSender != call.Peer() {
		t.Fatalf("incoming reaction target sender = %s, want %s", gotSender, call.Peer())
	}
}

func TestCallSendReactionPropagatesSendFailure(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	want := errors.New("send failed")
	eng.buildReaction = func(types.JID, types.JID, types.MessageID, string) *waE2E.Message {
		return &waE2E.Message{}
	}
	eng.sendMessage = func(context.Context, types.JID, *waE2E.Message) error { return want }

	if err := call.SendReaction(""); !errors.Is(err, want) {
		t.Fatalf("SendReaction error = %v, want %v", err, want)
	}
}

func TestCallReactionIsDispatchedByTargetCallID(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	sender := peerJID()
	var got CallReaction
	call.OnReaction(func(reaction CallReaction) { got = reaction })

	eng.onReactionMessage(reactionEvent(call.ID(), "👍", sender))

	if got.Emoji != "👍" || got.Sender != sender || got.Removed {
		t.Fatalf("reaction = %+v", got)
	}
}

func TestCallReactionRemovalIsDispatched(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var got CallReaction
	call.OnReaction(func(reaction CallReaction) { got = reaction })

	eng.onReactionMessage(reactionEvent(call.ID(), "", peerJID()))

	if !got.Removed || got.Emoji != "" {
		t.Fatalf("reaction removal = %+v", got)
	}
}

func TestCallReactionIgnoresUnrelatedMessage(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	called := false
	call.OnReaction(func(CallReaction) { called = true })

	eng.onReactionMessage(reactionEvent("OTHER", "👍", peerJID()))

	if called {
		t.Fatal("reaction for unrelated message reached call callback")
	}
}

func TestCallReactionFindsRecentlyEndedCall(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var got CallReaction
	call.OnReaction(func(reaction CallReaction) { got = reaction })
	eng.finishCall(call.ID(), "ended")

	eng.onReactionMessage(reactionEvent(call.ID(), "❤️", peerJID()))

	if got.Emoji != "❤️" {
		t.Fatalf("late reaction = %+v", got)
	}
}
