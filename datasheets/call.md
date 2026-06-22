# Datasheet: `meowcaller/call`

The call-control surface: the module that ties the media plane together and the
per-call registry that tracks active sessions and their media-task abort handles so
a connection teardown can stop every in-flight call. Signaling layer (call control
and lifecycle ownership).

**Validation vector:** (integration — no single vector). Behavior is pinned by the
inline `tests` module below: insert/duplicate/transition/remove bookkeeping plus
tests that `remove`, `abort_all`, a replacing `set_media_task`, and a
`set_media_task` on an already-removed call each actually cancel the media task.
There is no byte-level vector for orchestration.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust
`src/voip/registry.rs` + `src/voip/mod.rs` — main crate `src/`, not `wacore/src/`).

## Reference source (verbatim — authoritative)

### `mod.rs` (module composition — no logic to port)

```rust
//! VoIP calls media plane (Tokio runtime side): the DTLS/SCTP DataChannel transport
//! over the WhatsApp relay, Opus audio, the call state machine, and the media pipeline.
//! Pure protocol/crypto lives in `wacore::voip`.

pub mod audio;
pub mod registry;
pub mod session;
pub mod transport;
```

### `registry.rs` (per-call registry)

```rust
//! Per-call registry: tracks active [`CallSession`]s and their media-task abort handles so a
//! connection teardown can stop every in-flight call. [`CallRegistry::abort_all`] is the teardown
//! primitive, but it is NOT yet wired into `Client::cleanup_connection_state`; the integrator
//! owns a `CallRegistry` and must call `abort_all` from their own disconnect/reconnect path.

use std::collections::HashMap;
use std::sync::Mutex;

use tokio::task::AbortHandle;

use crate::voip::session::{CallPhase, CallSession};

struct CallEntry {
    session: CallSession,
    media_task: Option<AbortHandle>,
}

/// Thread-safe map of active calls keyed by call-id.
#[derive(Default)]
pub struct CallRegistry {
    inner: Mutex<HashMap<String, CallEntry>>,
}

impl CallRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    /// Register a new call. Returns false if a call with this id already exists.
    pub fn insert(&self, session: CallSession) -> bool {
        let mut map = self.inner.lock().expect("registry lock poisoned");
        if map.contains_key(&session.call_id) {
            return false;
        }
        map.insert(
            session.call_id.clone(),
            CallEntry {
                session,
                media_task: None,
            },
        );
        true
    }

    /// Attach (or replace) the media task's abort handle for a call. If the call was already
    /// removed, the handle is aborted immediately so its task can't outlive the call.
    pub fn set_media_task(&self, call_id: &str, handle: AbortHandle) {
        match self
            .inner
            .lock()
            .expect("registry lock poisoned")
            .get_mut(call_id)
        {
            Some(entry) => {
                if let Some(old) = entry.media_task.replace(handle) {
                    old.abort();
                }
            }
            None => handle.abort(),
        }
    }

    pub fn phase(&self, call_id: &str) -> Option<CallPhase> {
        self.inner
            .lock()
            .expect("registry lock poisoned")
            .get(call_id)
            .map(|e| e.session.phase())
    }

    /// Advance a call's phase; returns false if the call is unknown or the transition is illegal.
    pub fn transition(&self, call_id: &str, next: CallPhase) -> bool {
        self.inner
            .lock()
            .expect("registry lock poisoned")
            .get_mut(call_id)
            .is_some_and(|e| e.session.transition_to(next))
    }

    /// Read a clone of a call's session snapshot.
    pub fn snapshot(&self, call_id: &str) -> Option<CallSession> {
        self.inner
            .lock()
            .expect("registry lock poisoned")
            .get(call_id)
            .map(|e| e.session.clone())
    }

    pub fn active_count(&self) -> usize {
        self.inner.lock().expect("registry lock poisoned").len()
    }

    /// Remove a call, aborting its media task. Returns true if it existed.
    pub fn remove(&self, call_id: &str) -> bool {
        match self
            .inner
            .lock()
            .expect("registry lock poisoned")
            .remove(call_id)
        {
            Some(entry) => {
                if let Some(task) = entry.media_task {
                    task.abort();
                }
                true
            }
            None => false,
        }
    }

    /// Abort every call's media task and clear the registry. Returns the number cleared.
    /// Call this from your own disconnect/reconnect teardown; it is not wired into the Client.
    pub fn abort_all(&self) -> usize {
        let mut map = self.inner.lock().expect("registry lock poisoned");
        for entry in map.values() {
            if let Some(task) = &entry.media_task {
                task.abort();
            }
        }
        let n = map.len();
        map.clear();
        n
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use wacore_binary::{Jid, Server};

    fn session(id: &str) -> CallSession {
        CallSession::new_outgoing(
            id,
            Jid::new("222222222222222", Server::Lid),
            Jid::new("111111111111111", Server::Lid),
        )
    }

    #[test]
    fn insert_transition_remove() {
        let reg = CallRegistry::new();
        assert!(reg.insert(session("CID")));
        assert!(!reg.insert(session("CID"))); // duplicate
        assert_eq!(reg.phase("CID"), Some(CallPhase::Idle));
        assert!(reg.transition("CID", CallPhase::Calling));
        assert_eq!(reg.phase("CID"), Some(CallPhase::Calling));
        assert!(!reg.transition("UNKNOWN", CallPhase::Calling));
        assert!(reg.remove("CID"));
        assert!(!reg.remove("CID"));
        assert_eq!(reg.active_count(), 0);
    }

    #[tokio::test]
    async fn abort_all_stops_media_tasks() {
        let reg = CallRegistry::new();
        reg.insert(session("A"));
        reg.insert(session("B"));
        // Long-lived dummy media tasks.
        for id in ["A", "B"] {
            let handle = tokio::spawn(async {
                tokio::time::sleep(std::time::Duration::from_secs(3600)).await
            })
            .abort_handle();
            reg.set_media_task(id, handle);
        }
        assert_eq!(reg.active_count(), 2);
        assert_eq!(reg.abort_all(), 2);
        assert_eq!(reg.active_count(), 0);
    }

    /// Spawn a never-ending task; awaiting its `JoinHandle` blocks forever unless the stored
    /// `AbortHandle` actually cancels it. Awaiting then yields a cancelled `JoinError`, proving the
    /// handle is live (not a detached/orphaned copy).
    fn forever() -> tokio::task::JoinHandle<()> {
        tokio::spawn(async {
            std::future::pending::<()>().await;
        })
    }

    #[tokio::test]
    async fn remove_actually_cancels_media_task() {
        let reg = CallRegistry::new();
        reg.insert(session("A"));
        let handle = forever();
        reg.set_media_task("A", handle.abort_handle());
        assert!(reg.remove("A"));
        let err = handle.await.expect_err("removed task must be cancelled");
        assert!(err.is_cancelled());
    }

    #[tokio::test]
    async fn abort_all_actually_cancels_media_tasks() {
        let reg = CallRegistry::new();
        reg.insert(session("A"));
        let handle = forever();
        reg.set_media_task("A", handle.abort_handle());
        assert_eq!(reg.abort_all(), 1);
        let err = handle.await.expect_err("abort_all must cancel the task");
        assert!(err.is_cancelled());
    }

    #[tokio::test]
    async fn replace_cancels_the_old_media_task() {
        let reg = CallRegistry::new();
        reg.insert(session("A"));
        let old = forever();
        reg.set_media_task("A", old.abort_handle());
        // Replacing the handle for a live call must abort the old one.
        let new = forever();
        reg.set_media_task("A", new.abort_handle());
        let err = old.await.expect_err("replaced task must be cancelled");
        assert!(err.is_cancelled());
        assert!(!new.is_finished(), "the replacement task stays live");
        // Cleanup: removing the call cancels the replacement too.
        reg.remove("A");
        assert!(
            new.await
                .expect_err("replacement cancelled on remove")
                .is_cancelled()
        );
    }

    /// Attaching a media task to an already-removed call must abort the handle immediately so
    /// the task can't outlive the call.
    #[tokio::test]
    async fn set_media_task_on_unknown_call_aborts_immediately() {
        let reg = CallRegistry::new();
        let handle = forever();
        // No call with id "GONE" was ever inserted.
        reg.set_media_task("GONE", handle.abort_handle());
        let err = handle.await.expect_err("orphan task must be cancelled");
        assert!(err.is_cancelled());
    }
}
```

## Go envelope (signatures only)

```go
package meowcaller

// CallRegistry is a thread-safe map of active calls keyed by call-id, each
// optionally holding the cancel handle for its running media task.
type CallRegistry struct {
	// unexported: sync.Mutex + map[string]*callEntry
}

func NewCallRegistry() *CallRegistry

// Insert registers a new call; returns false if the id already exists.
func (r *CallRegistry) Insert(session *CallSession) bool

// SetMediaTask attaches (or replaces, cancelling the old) the media task's cancel
// handle for a call. If the call is unknown (e.g. already removed), the handle is
// cancelled immediately so its task can't outlive the call.
func (r *CallRegistry) SetMediaTask(callID string, cancel context.CancelFunc)

// Phase returns the call's current phase, and whether the call is known.
func (r *CallRegistry) Phase(callID string) (CallPhase, bool)

// Transition advances a call's phase; false if unknown or the move is illegal.
func (r *CallRegistry) Transition(callID string, next CallPhase) bool

// Snapshot returns a copy of the call's session, and whether it is known.
func (r *CallRegistry) Snapshot(callID string) (CallSession, bool)

func (r *CallRegistry) ActiveCount() int

// Remove deletes a call, cancelling its media task; true if it existed.
func (r *CallRegistry) Remove(callID string) bool

// AbortAll cancels every call's media task and clears the registry,
// returning the number cleared. Call on disconnect/reconnect.
func (r *CallRegistry) AbortAll() int
```

## Implementation suggestions (guidance, not authoritative)

- The registry bookkeeping is fully pinned by `insert_transition_remove`: insert
  returns false on a duplicate id, `phase`/`transition` no-op on an unknown id,
  `remove` returns whether it existed, and `active_count` reflects the map size.
  Port these exactly.
- Rust `Option<T>` returns (`phase`, `snapshot`) → Go `(T, bool)`. `bool` returns
  (`insert`, `transition`, `remove`) stay `bool`. `usize` counts → Go `int`.
- `TODO(human):` the abort-handle model does not map one-to-one. Rust uses
  `tokio::task::AbortHandle`; the idiomatic Go equivalent is a `context.CancelFunc`
  (or a stop channel) captured when the media goroutine is spawned. The envelope
  assumes `context.CancelFunc` — confirm against however the media loop is actually
  launched, and remember the Go `context` must be `import`ed. A `CancelFunc` is a
  no-op-safe "request stop"; it does not *wait* for the goroutine, so the Go tests
  must observe cancellation via a done-channel/`ctx.Done()`, not by "joining".
- `TODO(human):` `set_media_task` has TWO cancel behaviors to preserve: (1) replacing
  a live call's handle cancels the *previous* one (`replace` then `old.abort()`); and
  (2) attaching to an *unknown/removed* call cancels the incoming handle immediately
  (`None => handle.abort()`) so an orphan task can't outlive the call. Both are pinned
  (`replace_cancels_the_old_media_task`, `set_media_task_on_unknown_call_aborts_immediately`).
- Concurrency: the Rust wraps the whole map in a single `Mutex`; every method takes
  the lock for its critical section. Mirror with a `sync.Mutex` guarding the map;
  do not hold the lock across the cancel call if cancellation can block (the Rust
  `abort` is non-blocking — a Go `context.CancelFunc` is also non-blocking, so calling
  it under the lock is acceptable, but releasing first is cleaner).
- `abort_all` is the teardown hook: it must cancel every media task and empty the map
  so a reconnect cannot leave a media loop outliving the connection. The reference
  notes it is NOT auto-wired into the client — the integrator calls it from their own
  disconnect/reconnect path.
- `TODO(human):` `mod.rs` only declares the sibling modules; there is no logic to
  port. The real call-control flow (dispatching server signaling into `transition`,
  spawning the media goroutine and registering its cancel, calling `abort_all` from
  connection cleanup) is not in these files and is the human's to wire — this
  datasheet pins only the registry contract those wires must use.
- This is an orchestration module: apart from the registry's pinned bookkeeping, it
  is decision-heavy glue with no byte-level vector. Treat the surrounding wiring as
  unproven until validated end-to-end against a live call.
```
