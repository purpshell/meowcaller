package meowcaller

import (
	"context"
	"fmt"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"strconv"
	"time"
)

// ErrWaitingRoomControlUnknown means no matching provider acknowledgement was
// received. The command may have taken effect; callers must not replay it automatically.
var ErrWaitingRoomControlUnknown error = waitingRoomControlUnknown{}

type waitingRoomControlUnknown struct{}

func (waitingRoomControlUnknown) Error() string {
	return "meowcaller: waiting-room control outcome unknown"
}

// OutcomeUnknown lets a provider boundary retain uncertainty without matching text.
func (waitingRoomControlUnknown) OutcomeUnknown() bool { return true }

// WaitingRoomControlError is a matching provider refusal, without private stanza data.
type WaitingRoomControlError struct{ Code int }

func (e *WaitingRoomControlError) Error() string {
	return fmt.Sprintf("meowcaller: waiting-room control refused (%d)", e.Code)
}

func (e *engine) requestWaitingRoomControl(parent context.Context, node waBinary.Node) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	id := node.AttrGetter().String("id")
	response, err := e.requestCall(ctx, node, id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWaitingRoomControlUnknown, err)
	}
	if response == nil {
		return ErrWaitingRoomControlUnknown
	}
	attrs := response.AttrGetter()
	children := node.GetChildren()
	if len(children) != 1 || response.Tag != "ack" || attrs.String("class") != "call" || attrs.String("type") != children[0].Tag || attrs.String("id") != id || attrs.JID("from") != node.AttrGetter().JID("to") {
		return ErrWaitingRoomControlUnknown
	}
	if raw := attrs.String("error"); raw != "" {
		code, err := strconv.Atoi(raw)
		if err != nil || code < 400 || code > 599 {
			return ErrWaitingRoomControlUnknown
		}
		return &WaitingRoomControlError{Code: code}
	}
	if child, ok := response.GetOptionalChildByTag("error"); ok {
		code := child.AttrGetter().Int("code")
		if code < 400 || code > 599 {
			return ErrWaitingRoomControlUnknown
		}
		return &WaitingRoomControlError{Code: code}
	}
	return nil
}
