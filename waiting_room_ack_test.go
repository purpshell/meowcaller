package meowcaller

import (
	"context"
	"errors"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"testing"
	"time"
)

func TestWaitingRoomControlsRejectProviderRefusal(t *testing.T) {
	eng, call, _ := testGroupEngine("GROUP")
	eng.sendCallNode = func(context.Context, waBinary.Node) error { return nil }
	eng.requestCallNode = func(_ context.Context, node waBinary.Node, id string) (*waBinary.Node, error) {
		return &waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": id, "from": node.Attrs["to"], "class": "call", "type": node.GetChildren()[0].Tag, "error": "403"}}, nil
	}
	if err := call.AdmitParticipant(context.Background(), "200@lid"); err == nil {
		t.Fatal("provider refusal was reported as successful admission")
	}
}

func TestWaitingRoomControlsWaitForMatchingAck(t *testing.T) {
	for _, action := range []string{"toggle", "admit", "deny"} {
		t.Run(action, func(t *testing.T) {
			eng, call, _ := testGroupEngine("GROUP")
			released := make(chan struct{})
			requested := make(chan struct{})
			eng.requestCallNode = func(ctx context.Context, node waBinary.Node, id string) (*waBinary.Node, error) {
				close(requested)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-released:
				}
				return &waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": id, "from": node.Attrs["to"], "class": "call", "type": node.GetChildren()[0].Tag}}, nil
			}
			done := make(chan error, 1)
			go func() {
				if action == "toggle" {
					done <- call.SetApprovalRequired(context.Background(), true)
				} else if action == "admit" {
					done <- call.AdmitParticipant(context.Background(), "200@lid")
				} else {
					done <- call.DenyParticipant(context.Background(), "200@lid")
				}
			}()
			<-requested
			select {
			case err := <-done:
				t.Fatalf("returned before provider ack: %v", err)
			default:
			}
			close(released)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestWaitingRoomControlTimeoutIsUnknownAndNotRetried(t *testing.T) {
	eng, call, _ := testGroupEngine("GROUP")
	attempts := 0
	eng.requestCallNode = func(ctx context.Context, _ waBinary.Node, _ string) (*waBinary.Node, error) {
		attempts++
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := call.SetApprovalRequired(ctx, true)
	if !errors.Is(err, ErrWaitingRoomControlUnknown) || !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
		t.Fatalf("unknown/retry contract: %v attempts=%d", err, attempts)
	}
}
func TestWaitingRoomControlRejectsUnrelatedAck(t *testing.T) {
	for _, field := range []string{"id", "from", "type", "class"} {
		t.Run(field, func(t *testing.T) {
			eng, call, _ := testGroupEngine("GROUP")
			eng.requestCallNode = func(_ context.Context, node waBinary.Node, id string) (*waBinary.Node, error) {
				ack := &waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": id, "from": node.Attrs["to"], "class": "call", "type": node.GetChildren()[0].Tag}}
				ack.Attrs[field] = "foreign"
				return ack, nil
			}
			if err := call.SetApprovalRequired(context.Background(), true); !errors.Is(err, ErrWaitingRoomControlUnknown) {
				t.Fatalf("accepted unrelated %s: %v", field, err)
			}
		})
	}
}
