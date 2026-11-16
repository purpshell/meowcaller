# Call-link join ordering

Status: implemented. Synthetic race and cleanup checks cover the request window;
native provider ordering remains unverified.

**Reference pinned at:** a3a062ffdd46aed0129f335429d724dd1abb353c

## Reference source

The local ordering defect is in the existing Go orchestration, not a claim about
WhatsApp server ordering. The join installs its public handle after the request
returns. At the pinned source, `engine_group_api.go:439-441` reads:

```go
e.mu.Lock()
m := e.entry(result.CallID)
m.call = call
```

An earlier update reaches `engine_group_control.go:162-170` before that entry
exists and is dropped. The synthetic regression invokes the normal incoming
group-update handler from the request fixture before returning the join ACK.
It fails with no installed group update on the pinned source.

## Go envelope

`pending_link_join.go` adds private begin/end, defer and exact-identity take
helpers. Public Call and Client signatures stay unchanged.

The pending window starts only for `link_join`, before sending the request.
Unknown updates outside that window still follow the existing drop path.
Only a qualified `call-id@call` source can be staged. Once the ACK is parsed,
replay requires its exact call ID and creator. Snapshots retain arrival order;
the existing transaction filter still rejects stale or duplicate updates.
Rekey requests follow the same applied-update path as live snapshots. The ACK's
partial roster does not consume a group-update transaction.

## Client retention policy

The engine retains at most 16 queued or in-flight snapshots, with at most
256 KiB of encoded stanza data in total
and 64 KiB per snapshot across its active joins. These are local resource bounds,
not WhatsApp protocol limits or measured heap bounds. Over-limit unknown updates
are dropped without allocating call entries. If the dropped snapshot requests
rekey work, affected pending joins fail rather than reporting a usable call
without that epoch. The temporary encoded copy is
cleared after sizing.

Each snapshot owns its relay-key and token copies. Replay, creator mismatch,
termination and final join cleanup clear those copies. The last pending join
clears unrelated snapshots too. Terminal records survive every ACK until that
window closes, including termination before the first snapshot. A terminal record
applies only to joins that had started when the termination arrived, so a later
join can reuse the ID while an unrelated request still waits. Up to 16 terminal
IDs of at most 512 bytes are retained. Overflow fails current pending joins closed.
Updates for a newly bound call remain queued until its replay drains, preserving
an earlier rekey request before a newer transaction. Other active calls continue
through the ordinary update path. Count and byte budgets are released after each
snapshot's replay or discard, including its in-flight lifetime. Callers retain
control of the overall request deadline through their context.
Interrupted replay checks the exact public Call identity before cleanup, so an
older request cannot end or drain a replacement entry with the same call ID.
Roster and receiver commits recheck that identity together, after receiver
preparation. Rekey installation uses the same registry-then-engine lock order;
rekey sends and media startup also reject a replaced public Call. Media receiver publication, sender attachment and cleanup also require the original
public Call. If a direct call becomes a group call during receiver construction,
the existing bounded startup loop reloads and installs the current group roster
and epoch before publishing the registry. A send already in progress cannot be withdrawn.

## Verification

- `TestCallLinkJoinRetainsGroupUpdateBeforeACK`: RED before, GREEN after.
- `TestCallLinkJoinInstallsFollowingGroupUpdate`: existing equal-transaction path.
- Pending-update tests cover exact ACK identity, wrong source, owned copies,
  count and byte bounds, failed joins and termination cleanup.

These checks establish client behavior under synthetic ordering. They do not
establish hosted, real-provider, guest-chat or managed-native acceptance.
