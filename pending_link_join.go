package meowcaller

import (
	"context"
	"errors"
	"strings"

	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/types"
)

// These are client retention bounds, not provider wire limits.
const (
	maxPendingLinkUpdates     = 16
	maxPendingLinkUpdateBytes = 256 << 10
	maxPendingLinkUpdateSize  = 64 << 10
	maxPendingLinkCallIDSize  = 512
)

type pendingLinkUpdate struct {
	update     groupCallUpdate
	size       int
	generation uint64
}

func (e *engine) beginPendingLinkJoin() (uint64, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingLinkJoinSeq == ^uint64(0) {
		return 0, errors.New("meowcaller: pending link join sequence exhausted")
	}
	e.pendingLinkJoins++
	e.pendingLinkJoinSeq++
	return e.pendingLinkJoinSeq, nil
}

func (e *engine) endPendingLinkJoin() {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingLinkJoins--
	if e.pendingLinkJoins == 0 {
		for i := range e.pendingLinkUpdates {
			e.releasePendingLinkUpdateLocked(&e.pendingLinkUpdates[i])
		}
		e.pendingLinkUpdates = nil
		e.pendingLinkUpdateBytes = 0
		e.pendingLinkUpdateCount = 0
		e.pendingLinkEnded = nil
		e.pendingLinkClosed = 0
		e.pendingLinkJoinSeq = 0
	}
}

func (e *engine) deferPendingLinkUpdate(node *waBinary.Node, update groupCallUpdate) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[update.CallID]
	if (m != nil && !m.pendingLinkReplay) || e.pendingLinkJoins == 0 {
		return false
	}
	if update.CallID == "" || len(update.CallID) > maxPendingLinkCallIDSize || update.CallCreator.IsEmpty() || node.AttrGetter().JID("from") != types.NewJID(update.CallID, "call") {
		return true
	}
	if e.pendingLinkEnded[update.CallID] >= e.pendingLinkJoinSeq || e.pendingLinkClosed >= e.pendingLinkJoinSeq {
		return true
	}
	if e.pendingLinkUpdateCount >= maxPendingLinkUpdates {
		if update.RekeyRequested {
			e.markPendingLinkEndedLocked(update.CallID)
		}
		return true
	}
	encoded, err := waBinary.Marshal(*node)
	size := len(encoded)
	clear(encoded)
	if err != nil || size > maxPendingLinkUpdateSize || size > maxPendingLinkUpdateBytes-e.pendingLinkUpdateBytes {
		if update.RekeyRequested {
			e.markPendingLinkEndedLocked(update.CallID)
		}
		return true
	}
	e.pendingLinkUpdates = append(e.pendingLinkUpdates, pendingLinkUpdate{update: cloneGroupCallUpdate(update), size: size, generation: e.pendingLinkJoinSeq})
	e.pendingLinkUpdateBytes += size
	e.pendingLinkUpdateCount++
	return true
}

// The caller holds e.mu while binding the public Call to the ACK identity.
func (e *engine) takePendingLinkUpdatesLocked(callID string, creator types.JID, joinID uint64) ([]pendingLinkUpdate, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	var matched []pendingLinkUpdate
	cutoff := max(e.pendingLinkEnded[callID], e.pendingLinkClosed)
	ended := cutoff >= joinID
	remaining := e.pendingLinkUpdates[:0]
	for _, pending := range e.pendingLinkUpdates {
		if pending.update.CallID != callID || pending.generation < joinID || (ended && pending.generation > cutoff) {
			remaining = append(remaining, pending)
			continue
		}
		if !ended && pending.update.CallCreator == creator {
			matched = append(matched, pending)
		} else {
			e.releasePendingLinkUpdateLocked(&pending)
		}
	}
	clear(e.pendingLinkUpdates[len(remaining):])
	e.pendingLinkUpdates = remaining
	return matched, ended
}

func (e *engine) markPendingLinkEndedLocked(callID string) {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	if e.pendingLinkJoins == 0 {
		return
	}
	_, exists := e.pendingLinkEnded[callID]
	if len(callID) > maxPendingLinkCallIDSize || (!exists && len(e.pendingLinkEnded) >= maxPendingLinkUpdates) {
		e.pendingLinkClosed = e.pendingLinkJoinSeq
	} else {
		if e.pendingLinkEnded == nil {
			e.pendingLinkEnded = make(map[string]uint64)
		}
		e.pendingLinkEnded[strings.Clone(callID)] = e.pendingLinkJoinSeq
	}
	remaining := e.pendingLinkUpdates[:0]
	for _, pending := range e.pendingLinkUpdates {
		if pending.update.CallID == callID || pending.generation <= e.pendingLinkClosed {
			e.releasePendingLinkUpdateLocked(&pending)
		} else {
			remaining = append(remaining, pending)
		}
	}
	clear(e.pendingLinkUpdates[len(remaining):])
	e.pendingLinkUpdates = remaining
}

func (e *engine) replayPendingLinkUpdates(ctx context.Context, m *engineCall, call *Call, joinID uint64, queued []pendingLinkUpdate) error {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	for {
		for i := range queued {
			update := queued[i].update
			e.mu.Lock()
			active := e.calls[call.id] == m && m.call == call && max(e.pendingLinkEnded[call.id], e.pendingLinkClosed) < joinID
			e.mu.Unlock()
			if active && ctx.Err() == nil && e.applyGroupUpdateFor(update, call) && update.RekeyRequested {
				if err := e.distributeRequestedGroupEpochFor(ctx, update, call); err != nil {
					e.c.log.Warn().Err(err).Str("call_id", update.CallID).Msg("group rekey fanout failed")
				}
			}
			e.releasePendingLinkUpdate(&queued[i])
		}
		e.mu.Lock()
		var ended bool
		active := e.calls[call.id] == m && m.call == call
		if active {
			queued, ended = e.takePendingLinkUpdatesLocked(call.id, call.peer, joinID)
		} else {
			queued = nil
		}
		if ended || !active || ctx.Err() != nil {
			if active {
				m.pendingLinkReplay = false
			}
			e.mu.Unlock()
			for i := range queued {
				e.releasePendingLinkUpdate(&queued[i])
			}
			if active {
				e.finishCallFor(call.id, "join_interrupted", call)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return errors.New("meowcaller: call ended before link join completed")
		}
		if len(queued) == 0 {
			m.pendingLinkReplay = false
			e.mu.Unlock()
			return nil
		}
		e.mu.Unlock()
	}
}

func (e *engine) releasePendingLinkUpdate(pending *pendingLinkUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	e.mu.Lock()
	e.releasePendingLinkUpdateLocked(pending)
	e.mu.Unlock()
}

func (e *engine) releasePendingLinkUpdateLocked(pending *pendingLinkUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	if pending.size > 0 {
		e.pendingLinkUpdateBytes -= pending.size
		e.pendingLinkUpdateCount--
		pending.size = 0
	}
	clearPendingLinkUpdate(pending)
}

func clearPendingLinkUpdate(pending *pendingLinkUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/pull/51
	if relay := pending.update.Relay; relay != nil {
		clear(relay.Key)
		clear(relay.HBHKey)
		for _, token := range relay.Tokens {
			clear(token)
		}
		for _, token := range relay.AuthTokens {
			clear(token)
		}
	}
	pending.update.Relay = nil
	pending.update.Participants = nil
}
