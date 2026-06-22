package meowcaller

import (
	"context"
	"sync"
)

// Per-call registry: tracks active CallSessions and their media-task cancel handles
// so a connection teardown can stop every in-flight call. AbortAll is the teardown
// primitive; the integrator owns a CallRegistry and calls AbortAll from their own
// disconnect/reconnect path (it is not auto-wired).

// callEntry is one registered call: its session plus the optional cancel handle for
// the running media goroutine.
type callEntry struct {
	session   *CallSession
	mediaTask context.CancelFunc // nil until a media goroutine is registered
}

// CallRegistry is a thread-safe map of active calls keyed by call-id, each
// optionally holding the cancel handle for its running media task.
type CallRegistry struct {
	mu    sync.Mutex
	calls map[string]*callEntry
}

// NewCallRegistry returns an empty registry.
func NewCallRegistry() *CallRegistry {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L25-L27
	// TODO
	// agent suggestion: return &CallRegistry{calls: make(map[string]*callEntry)}.
	// human input:
	return nil
}

// Insert registers a new call; returns false if the id already exists.
func (r *CallRegistry) Insert(session *CallSession) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L30-L43
	// TODO
	// agent suggestion: lock; if calls[session.CallID] exists return false; else store
	// &callEntry{session: session}; return true.
	// human input:
	return false
}

// SetMediaTask attaches (or replaces, cancelling the old) the media task's cancel
// handle for a call. If the call is unknown (e.g. already removed), the handle is
// cancelled immediately so its task can't outlive the call.
func (r *CallRegistry) SetMediaTask(callID string, cancel context.CancelFunc) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L47-L61
	// TODO
	// agent suggestion: lock; if entry, found := calls[callID]; found { old := entry.mediaTask;
	// entry.mediaTask = cancel; if old != nil old() } else { cancel() }. Two pinned behaviors:
	// replace-and-cancel the old, and cancel an orphan handle when the call is unknown.
	// human input:
	_ = cancel
}

// Phase returns the call's current phase, and whether the call is known.
func (r *CallRegistry) Phase(callID string) (CallPhase, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L63-L69
	// TODO
	// agent suggestion: lock; if entry, ok := calls[callID]; ok return entry.session.Phase(), true; else 0, false.
	// human input:
	return CallPhaseIdle, false
}

// Transition advances a call's phase; false if unknown or the move is illegal.
func (r *CallRegistry) Transition(callID string, next CallPhase) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L72-L78
	// TODO
	// agent suggestion: lock; entry, ok := calls[callID]; return ok && entry.session.TransitionTo(next).
	// human input:
	return false
}

// Snapshot returns a copy of the call's session, and whether it is known.
func (r *CallRegistry) Snapshot(callID string) (CallSession, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L81-L87
	// TODO
	// agent suggestion: lock; if entry, ok := calls[callID]; ok return *entry.session, true; else zero, false.
	// human input:
	return CallSession{}, false
}

// ActiveCount returns the number of registered calls.
func (r *CallRegistry) ActiveCount() int {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L89-L91
	// TODO
	// agent suggestion: lock; return len(r.calls).
	// human input:
	return 0
}

// Remove deletes a call, cancelling its media task; true if it existed.
func (r *CallRegistry) Remove(callID string) bool {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L94-L109
	// TODO
	// agent suggestion: lock; entry, ok := calls[callID]; if !ok return false; delete; if
	// entry.mediaTask != nil entry.mediaTask(); return true.
	// human input:
	return false
}

// AbortAll cancels every call's media task and clears the registry, returning the
// number cleared. Call on disconnect/reconnect.
func (r *CallRegistry) AbortAll() int {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/src/voip/registry.rs#L113-L123
	// TODO
	// agent suggestion: lock; for each entry if mediaTask != nil call it; n := len(calls);
	// reset calls to a fresh map; return n.
	// human input:
	return 0
}
