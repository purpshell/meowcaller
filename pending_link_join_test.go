package meowcaller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
	waLog "github.com/polymorfa/hypermeow/util/log"
	"github.com/rs/zerolog"
)

func pendingLinkFixture(id string, creator types.JID) (waBinary.Node, groupCallUpdate) {
	return waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{"from": types.NewJID(id, "call")}}, groupCallUpdate{
		CallID: id, CallCreator: creator, TransactionID: 7,
		Relay: &groupCallRelay{Key: []byte{1, 2}, HBHKey: []byte{3}, Tokens: [][]byte{{4}}, AuthTokens: [][]byte{{5}}},
	}
}

func TestPendingLinkUpdateLifetimeAndOwnership(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	node, update := pendingLinkFixture("JOIN", creator)
	if eng.deferPendingLinkUpdate(&node, update) || len(eng.pendingLinkUpdates) != 0 {
		t.Fatal("unknown updates must not be retained without a pending join")
	}
	eng.beginPendingLinkJoin()
	if !eng.deferPendingLinkUpdate(&node, update) || len(eng.pendingLinkUpdates) != 1 {
		t.Fatal("pending snapshot was not retained")
	}
	retained := eng.pendingLinkUpdates[0].update.Relay
	update.Relay.Key[0] = 9
	if retained.Key[0] != 1 {
		t.Fatal("retained snapshot aliases input")
	}
	eng.endPendingLinkJoin()
	if len(eng.pendingLinkUpdates) != 0 || eng.pendingLinkUpdateBytes != 0 || eng.pendingLinkJoins != 0 {
		t.Fatal("completed join retained staging state")
	}
	if retained.Key[0] != 0 || retained.HBHKey[0] != 0 || retained.Tokens[0][0] != 0 || retained.AuthTokens[0][0] != 0 {
		t.Fatal("discarded relay credentials were not cleared")
	}
}

func TestPendingLinkUpdateBounds(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	for i := 0; i < maxPendingLinkUpdates+5; i++ {
		node, update := pendingLinkFixture(fmt.Sprint(i), creator)
		eng.deferPendingLinkUpdate(&node, update)
	}
	if len(eng.pendingLinkUpdates) != maxPendingLinkUpdates {
		t.Fatal("snapshot count is unbounded")
	}
	eng.endPendingLinkJoin()
	eng.beginPendingLinkJoin()
	for i := 0; i < 8; i++ {
		node, update := pendingLinkFixture(fmt.Sprint(i), creator)
		node.Content = make([]byte, maxPendingLinkUpdateSize-100)
		eng.deferPendingLinkUpdate(&node, update)
	}
	if eng.pendingLinkUpdateBytes > maxPendingLinkUpdateBytes || len(eng.pendingLinkUpdates) > 4 {
		t.Fatal("snapshot byte budget exceeded")
	}
	node, update := pendingLinkFixture("OVERSIZED", creator)
	node.Content = make([]byte, maxPendingLinkUpdateSize+1)
	before := len(eng.pendingLinkUpdates)
	eng.deferPendingLinkUpdate(&node, update)
	if len(eng.pendingLinkUpdates) != before {
		t.Fatal("oversized snapshot retained")
	}
}

func TestPendingLinkUpdateRequiresExactAckIdentity(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	node, update := pendingLinkFixture("JOIN", creator)
	eng.deferPendingLinkUpdate(&node, update)
	otherNode, other := pendingLinkFixture("OTHER", creator)
	eng.deferPendingLinkUpdate(&otherNode, other)
	eng.mu.Lock()
	matched, ended := eng.takePendingLinkUpdatesLocked("JOIN", types.NewJID("foreign", types.HiddenUserServer), eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if len(matched) != 0 || ended || len(eng.pendingLinkUpdates) != 1 || eng.pendingLinkUpdates[0].update.CallID != "OTHER" {
		t.Fatal("pending snapshot escaped the ACK call/creator binding")
	}
	node.Attrs["from"] = types.NewJID("OTHER", "call")
	eng.deferPendingLinkUpdate(&node, update)
	if len(eng.pendingLinkUpdates) != 1 {
		t.Fatal("wrong destination retained")
	}
}

func TestFailedLinkJoinClearsEarlyUpdates(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	node, update := pendingLinkFixture("JOIN", creator)
	eng.requestCallNode = func(context.Context, waBinary.Node, string) (*waBinary.Node, error) {
		eng.deferPendingLinkUpdate(&node, update)
		return nil, context.Canceled
	}
	if _, err := eng.joinPublicCallLink(context.Background(), "TOKEN", CallLinkOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("join failure lost")
	}
	if eng.lookup("JOIN") != nil || eng.pendingLinkJoins != 0 || len(eng.pendingLinkUpdates) != 0 {
		t.Fatal("failed join retained an unknown call")
	}
}

func TestTerminatedPendingLinkCannotBeRevived(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	node, update := pendingLinkFixture("JOIN", creator)
	eng.deferPendingLinkUpdate(&node, update)
	eng.finishCall("JOIN", "terminated")
	eng.deferPendingLinkUpdate(&node, update)
	eng.mu.Lock()
	matched, ended := eng.takePendingLinkUpdatesLocked("JOIN", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if !ended || len(matched) != 0 {
		t.Fatal("termination lost or credentials retained")
	}
	eng.mu.Lock()
	_, endedAgain := eng.takePendingLinkUpdatesLocked("JOIN", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if !endedAgain {
		t.Fatal("one ACK consumed the terminal record for another pending join")
	}
}

func TestTerminationBeforeSnapshotClosesPendingJoin(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	eng.finishCall("JOIN", "terminated")
	node, update := pendingLinkFixture("JOIN", creator)
	eng.deferPendingLinkUpdate(&node, update)
	eng.mu.Lock()
	matched, ended := eng.takePendingLinkUpdatesLocked("JOIN", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if !ended || len(matched) != 0 || eng.lookup("JOIN") != nil {
		t.Fatal("early termination allowed a snapshot or ACK to revive the call")
	}
}

func TestPendingTerminalOverflowFailsClosedAndClears(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.beginPendingLinkJoin()
	for i := 0; i <= maxPendingLinkUpdates; i++ {
		eng.finishCall(fmt.Sprint(i), "terminated")
	}
	eng.mu.Lock()
	_, ended := eng.takePendingLinkUpdatesLocked("JOIN", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if !ended || len(eng.pendingLinkEnded) > maxPendingLinkUpdates {
		t.Fatal("terminal budget did not fail closed")
	}
	eng.endPendingLinkJoin()
	if eng.pendingLinkClosed != 0 || len(eng.pendingLinkEnded) != 0 {
		t.Fatal("completed pending window retained terminal state")
	}
}

func TestNewerUpdatesCannotOvertakePendingRekeySnapshot(t *testing.T) {
	eng, _, creator := testGroupEngine("JOIN")
	eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	eng.calls["JOIN"].pendingLinkReplay = true
	node, older := pendingLinkFixture("JOIN", creator)
	older.RekeyRequested = true
	newer := older
	newer.TransactionID++
	newer.RekeyRequested = false
	if !eng.deferPendingLinkUpdate(&node, older) || !eng.deferPendingLinkUpdate(&node, newer) {
		t.Fatal("live update bypassed join replay")
	}
	eng.mu.Lock()
	queued, ended := eng.takePendingLinkUpdatesLocked("JOIN", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if ended || len(queued) != 2 || !queued[0].update.RekeyRequested || queued[0].update.TransactionID >= queued[1].update.TransactionID || eng.calls["JOIN"].groupUpdate != nil {
		t.Fatal("newer transaction consumed the pending rekey's place")
	}
	for i := range queued {
		eng.releasePendingLinkUpdate(&queued[i])
	}
}

func TestReplayPreservesRekeyBeforeNewerTransaction(t *testing.T) {
	eng, _, creator := testGroupEngine("JOIN")
	eng.c.wa = whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	var logs bytes.Buffer
	eng.c.log = zerolog.New(&logs)
	eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	m := eng.calls["JOIN"]
	m.pendingLinkReplay = true
	node, older := pendingLinkFixture("JOIN", creator)
	older.RekeyRequested, older.Relay = true, nil
	eng.deferPendingLinkUpdate(&node, older)
	eng.mu.Lock()
	queued, _ := eng.takePendingLinkUpdatesLocked("JOIN", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	newer := older
	newer.TransactionID++
	newer.RekeyRequested = false
	if !eng.deferPendingLinkUpdate(&node, newer) {
		eng.applyGroupUpdate(newer)
	}
	if err := eng.replayPendingLinkUpdates(context.Background(), m, m.call, eng.pendingLinkJoinSeq, queued); err != nil {
		t.Fatal(err)
	}
	// No peers means fanout returns this error before any key generation or I/O.
	if strings.Count(logs.String(), "group rekey has no remote connected devices") != 1 || m.groupUpdate.TransactionID != newer.TransactionID || m.pendingLinkReplay {
		t.Fatal("a newer update overtook or suppressed the earlier rekey work")
	}
}

func TestTwoPendingAcksCannotReviveTerminatedCall(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.c.wa = whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	eng.requestCallNode = func(ctx context.Context, _ waBinary.Node, _ string) (*waBinary.Node, error) {
		ready <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"class": "call", "type": "link_join"}, Content: []waBinary.Node{{Tag: "group_info", Attrs: waBinary.Attrs{"call-id": "JOIN", "call-creator": creator, "media": "audio", "transaction-id": "7", "connected-limit": "32"}}}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := eng.joinPublicCallLink(ctx, "TOKEN", CallLinkOptions{}); results <- err }()
	}
	for range 2 {
		<-ready
	}
	eng.finishCall("JOIN", "terminated")
	close(release)
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("an ACK revived the terminated call")
		}
	}
	if eng.lookup("JOIN") != nil || eng.pendingLinkJoins != 0 || len(eng.pendingLinkEnded) != 0 {
		t.Fatal("pending terminal window was not retired")
	}
	if call, err := eng.joinPublicCallLink(ctx, "TOKEN", CallLinkOptions{}); err != nil || call == nil {
		t.Fatal("the ACK fixture or fresh pending window is invalid")
	}
	eng.finishCall("JOIN", "test_complete")
}

func TestConcurrentLinkJoinCleanupKeepsOtherAckSnapshot(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	eng.beginPendingLinkJoin()
	eng.beginPendingLinkJoin()
	for _, id := range []string{"FIRST", "SECOND"} {
		node, update := pendingLinkFixture(id, creator)
		eng.deferPendingLinkUpdate(&node, update)
	}
	eng.mu.Lock()
	first, _ := eng.takePendingLinkUpdatesLocked("FIRST", creator, eng.pendingLinkJoinSeq)
	eng.mu.Unlock()
	if len(first) != 1 {
		t.Fatal("first ACK lost its snapshot")
	}
	eng.releasePendingLinkUpdate(&first[0])
	eng.endPendingLinkJoin()
	if len(eng.pendingLinkUpdates) != 1 || eng.pendingLinkUpdates[0].update.CallID != "SECOND" {
		t.Fatal("one completed join discarded another pending join")
	}
	eng.endPendingLinkJoin()
	if len(eng.pendingLinkUpdates) != 0 {
		t.Fatal("last join retained snapshots")
	}
}

func TestPendingBudgetIncludesInflightAndResumesAfterRelease(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	first, _ := eng.beginPendingLinkJoin()
	eng.beginPendingLinkJoin()
	for i := 0; i < maxPendingLinkUpdates; i++ {
		node, update := pendingLinkFixture(fmt.Sprint(i), creator)
		eng.deferPendingLinkUpdate(&node, update)
	}
	eng.mu.Lock()
	queued, _ := eng.takePendingLinkUpdatesLocked("0", creator, first)
	eng.mu.Unlock()
	node, update := pendingLinkFixture("NEXT", creator)
	eng.deferPendingLinkUpdate(&node, update)
	if len(queued) != 1 || len(eng.pendingLinkUpdates) != maxPendingLinkUpdates-1 || eng.pendingLinkUpdateCount != maxPendingLinkUpdates {
		t.Fatal("in-flight snapshot escaped the budget")
	}
	eng.releasePendingLinkUpdate(&queued[0])
	eng.deferPendingLinkUpdate(&node, update)
	if len(eng.pendingLinkUpdates) != maxPendingLinkUpdates || eng.pendingLinkUpdates[len(eng.pendingLinkUpdates)-1].update.CallID != "NEXT" {
		t.Fatal("a freed slot was unavailable to another active join")
	}
	eng.endPendingLinkJoin()
	eng.endPendingLinkJoin()
}

func TestTerminalRecordDoesNotRejectLaterJoinWhileAnotherWaits(t *testing.T) {
	eng, _, creator := testGroupEngine("EXISTING")
	old, _ := eng.beginPendingLinkJoin()
	eng.beginPendingLinkJoin()
	eng.finishCall("JOIN", "terminated")
	fresh, _ := eng.beginPendingLinkJoin()
	node, update := pendingLinkFixture("JOIN", creator)
	eng.deferPendingLinkUpdate(&node, update)
	eng.mu.Lock()
	oldUpdates, oldEnded := eng.takePendingLinkUpdatesLocked("JOIN", creator, old)
	freshUpdates, freshEnded := eng.takePendingLinkUpdatesLocked("JOIN", creator, fresh)
	eng.mu.Unlock()
	if !oldEnded || len(oldUpdates) != 0 || freshEnded || len(freshUpdates) != 1 {
		t.Fatal("terminal record escaped its pending request generation")
	}
	eng.releasePendingLinkUpdate(&freshUpdates[0])
	for range 3 {
		eng.endPendingLinkJoin()
	}
}

func TestInstalledRelayCredentialsSurvivePendingCleanup(t *testing.T) {
	eng, _, creator := testGroupEngine("JOIN")
	eng.c.wa = whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	id, _ := eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	m := eng.calls["JOIN"]
	m.pendingLinkReplay = true
	node, update := pendingLinkFixture("JOIN", creator)
	eng.deferPendingLinkUpdate(&node, update)
	eng.mu.Lock()
	queued, _ := eng.takePendingLinkUpdatesLocked("JOIN", creator, id)
	eng.mu.Unlock()
	retired := queued[0].update.Relay
	if err := eng.replayPendingLinkUpdates(context.Background(), m, m.call, id, queued); err != nil {
		t.Fatal(err)
	}
	installed := m.groupUpdate.Relay
	if installed.Key[0] != 1 || installed.HBHKey[0] != 3 || installed.Tokens[0][0] != 4 || installed.AuthTokens[0][0] != 5 {
		t.Fatal("pending cleanup wiped installed relay credentials")
	}
	if retired.Key[0] != 0 || retired.HBHKey[0] != 0 || retired.Tokens[0][0] != 0 || retired.AuthTokens[0][0] != 0 || eng.pendingLinkUpdateCount != 0 || eng.pendingLinkUpdateBytes != 0 {
		t.Fatal("pending credential ownership or accounting leaked")
	}
}

func TestInterruptedReplayDoesNotConsumeReplacementCall(t *testing.T) {
	eng, old, creator := testGroupEngine("JOIN")
	eng.c.wa = whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	oldID, _ := eng.beginPendingLinkJoin()
	m := eng.calls["JOIN"]
	m.pendingLinkReplay = true
	node, update := pendingLinkFixture("JOIN", creator)
	eng.deferPendingLinkUpdate(&node, update)
	eng.mu.Lock()
	oldQueued, _ := eng.takePendingLinkUpdatesLocked("JOIN", creator, oldID)
	eng.mu.Unlock()
	freshID, _ := eng.beginPendingLinkJoin()
	fresh := &Call{eng: eng, id: "JOIN", peer: creator, phase: CallPhaseConnecting}
	m.call = fresh
	update.TransactionID++
	eng.deferPendingLinkUpdate(&node, update)
	if err := eng.replayPendingLinkUpdates(context.Background(), m, old, oldID, oldQueued); err == nil {
		t.Fatal("replaced replay was accepted")
	}
	eng.finishCallFor("JOIN", "old_cleanup", old)
	if eng.calls["JOIN"] != m || m.call != fresh || !m.pendingLinkReplay || len(eng.pendingLinkUpdates) != 1 {
		t.Fatal("old replay changed the replacement's call or staged data")
	}
	eng.mu.Lock()
	queued, ended := eng.takePendingLinkUpdatesLocked("JOIN", creator, freshID)
	eng.mu.Unlock()
	if ended || len(queued) != 1 {
		t.Fatal("replacement snapshot was lost")
	}
	if err := eng.replayPendingLinkUpdates(context.Background(), m, fresh, freshID, queued); err != nil {
		t.Fatal(err)
	}
	eng.endPendingLinkJoin()
	eng.endPendingLinkJoin()
}

func TestPendingRekeyCapacityFailsJoinClosed(t *testing.T) {
	for _, rekey := range []bool{false, true} {
		t.Run(fmt.Sprint(rekey), func(t *testing.T) {
			eng, _, creator := testGroupEngine("EXISTING")
			id, _ := eng.beginPendingLinkJoin()
			defer eng.endPendingLinkJoin()
			for i := 0; i < maxPendingLinkUpdates; i++ {
				node, update := pendingLinkFixture(fmt.Sprint(i), creator)
				eng.deferPendingLinkUpdate(&node, update)
			}
			node, update := pendingLinkFixture("JOIN", creator)
			update.RekeyRequested = rekey
			eng.deferPendingLinkUpdate(&node, update)
			eng.mu.Lock()
			queued, ended := eng.takePendingLinkUpdatesLocked("JOIN", creator, id)
			eng.mu.Unlock()
			if ended != rekey || len(queued) != 0 || eng.lookup("JOIN") != nil {
				t.Fatal("lost required rekey was not distinguished from optional metadata overflow")
			}
		})
	}
}

func TestReplayReplacementDuringReceiverPreparation(t *testing.T) {
	eng, old, creator := testGroupEngine("JOIN")
	eng.c.wa = whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	oldID, _ := eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	m := eng.calls["JOIN"]
	m.pendingLinkReplay = true
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	entered, resume := make(chan struct{}), make(chan struct{})
	registry, err := newParticipantReceiveRegistry("JOIN", iota32(), self.String(), peer.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.clear()
	registry.decoderFactory = func() participantAudioDecoder {
		close(entered)
		<-resume
		return &recordingParticipantDecoder{}
	}
	m.groupReceivers = registry
	node, update := pendingLinkFixture("JOIN", creator)
	added := mediaTestJID("333333333333333", 43)
	update.Participants = []groupCallParticipant{{JID: added.ToNonAD(), State: "connected", Devices: []groupCallDevice{{JID: added, PID: 17, HasPID: true}}}}
	eng.deferPendingLinkUpdate(&node, update)
	eng.mu.Lock()
	queued, _ := eng.takePendingLinkUpdatesLocked("JOIN", creator, oldID)
	eng.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- eng.replayPendingLinkUpdates(context.Background(), m, old, oldID, queued) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not prepare receiver")
	}
	freshID, _ := eng.beginPendingLinkJoin()
	defer eng.endPendingLinkJoin()
	fresh := &Call{eng: eng, id: "JOIN", peer: creator, phase: CallPhaseConnecting}
	eng.mu.Lock()
	m.call = fresh
	eng.mu.Unlock()
	update.TransactionID++
	eng.deferPendingLinkUpdate(&node, update)
	close(resume)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("replaced replay succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replay deadlocked")
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if m.call != fresh || m.groupUpdate != nil || !m.pendingLinkReplay || len(eng.pendingLinkUpdates) != 1 || eng.pendingLinkUpdates[0].generation != freshID {
		t.Fatal("old replay mutated the replacement call or its staged update")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.hasGroupUpdate {
		t.Fatal("old replay committed receiver state after replacement")
	}
}

func TestStaleReplayCannotRekeyOrStartReplacement(t *testing.T) {
	eng, old, creator := testGroupEngine("JOIN")
	m := eng.calls["JOIN"]
	fresh := &Call{eng: eng, id: "JOIN", peer: creator, phase: CallPhaseConnecting}
	m.call = fresh
	rawKey := bytes.Repeat([]byte{0x42}, 32)
	if err := eng.installGroupEpochFor("JOIN", 7, rawKey, old); err == nil {
		t.Fatal("old replay installed a replacement epoch")
	}
	_, update := pendingLinkFixture("JOIN", creator)
	if err := eng.distributeRequestedGroupEpochFor(context.Background(), update, old); err == nil {
		t.Fatal("old replay started replacement rekey work")
	}
	eng.maybeStartMediaFor("JOIN", old)
	if m.hasGroupEpoch || len(m.groupRawEpoch) != 0 || m.started || m.call != fresh {
		t.Fatal("old replay mutated replacement media")
	}
}

func TestMediaReceiverPublicationRejectsReplacementCall(t *testing.T) {
	eng, old, creator := testGroupEngine("JOIN")
	_, update := pendingLinkFixture("JOIN", creator)
	m := eng.calls["JOIN"]
	m.groupUpdate = &update
	m.groupEpochTxID = update.TransactionID
	candidate, err := newParticipantReceiveRegistry("JOIN", iota32(), mediaTestJID("111111111111111", 14).String(), mediaTestJID("222222222222222", 0).String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.clear()
	fresh := &Call{eng: eng, id: "JOIN", peer: creator, phase: CallPhaseConnecting}
	m.call = fresh
	if _, _, err = eng.publishMediaReceivers("JOIN", old, candidate, &update, update.TransactionID); err == nil || m.groupReceivers != nil {
		t.Fatal("old media startup published receivers to the replacement")
	}
	if published, ready, err := eng.publishMediaReceivers("JOIN", fresh, candidate, &update, update.TransactionID); err != nil || !ready || published != candidate || m.groupReceivers != candidate {
		t.Fatal("current media startup could not publish its receivers")
	}
}

func TestDirectReceiverPublicationRejectsPromotion(t *testing.T) {
	eng, call, creator := testGroupEngine("JOIN")
	candidate, err := newParticipantReceiveRegistry("JOIN", iota32(), mediaTestJID("111111111111111", 14).String(), mediaTestJID("222222222222222", 0).String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.clear()
	_, update := pendingLinkFixture("JOIN", creator)
	m := eng.calls["JOIN"]
	m.group = true
	m.groupUpdate = &update
	m.groupEpochTxID = update.TransactionID
	m.groupRawEpoch = bytes.Repeat([]byte{0x42}, 32)
	m.hasGroupEpoch = true
	if _, ready, err := eng.publishMediaReceivers("JOIN", call, candidate, nil, 0); err != nil || ready || m.groupReceivers != nil {
		t.Fatal("direct-mode startup published receivers without the new group roster and epoch")
	}
	published, err := eng.initializeMediaReceivers(context.Background(), "JOIN", call, candidate)
	if err != nil || published != candidate {
		t.Fatalf("group catch-up failed: %v", err)
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if !candidate.hasGroupUpdate || candidate.transactionID != update.TransactionID || !candidate.hasEpoch || candidate.installedEpoch.transactionID != update.TransactionID || !bytes.Equal(candidate.installedEpoch.rawKey, m.groupRawEpoch) {
		t.Fatal("promoted registry lacks the current roster or shared epoch")
	}
}

func TestMediaRunReleasePreservesReplacementAndAllowsRetry(t *testing.T) {
	eng, old, creator := testGroupEngine("JOIN")
	owner := eng.calls["JOIN"]
	owner.started = true
	ctx, cancel := context.WithCancel(context.Background())
	owner.cancel = cancel
	eng.releaseMediaRun("JOIN", owner, old, cancel)
	if ctx.Err() == nil || owner.started || owner.cancel != nil {
		t.Fatal("failed media run retained its startup ownership")
	}
	fresh := &Call{eng: eng, id: "JOIN", peer: creator, phase: CallPhaseConnecting}
	owner.call = fresh
	owner.started = true
	replacementCtx, replacementCancel := context.WithCancel(context.Background())
	defer replacementCancel()
	owner.cancel = replacementCancel
	eng.releaseMediaRun("JOIN", owner, old, cancel)
	if replacementCtx.Err() != nil || !owner.started || owner.cancel == nil {
		t.Fatal("old media release reset the replacement run")
	}
}

func TestMediaStartupFailureCanStartAgain(t *testing.T) {
	eng, call, _ := testGroupEngine("JOIN")
	var logs bytes.Buffer
	eng.c.log = zerolog.New(&logs)
	owner := eng.calls["JOIN"]
	owner.group = false
	owner.callKey = iota32()
	owner.relay = &relayData{}
	owner.selfLID = mediaTestJID("111111111111111", 14).String()
	owner.peerLID = mediaTestJID("222222222222222", 0).String()
	for range 2 {
		eng.maybeStartMediaFor("JOIN", call)
		deadline := time.After(5 * time.Second)
		ticker := time.NewTicker(time.Millisecond)
		for {
			eng.mu.Lock()
			released := !owner.started && owner.cancel == nil
			eng.mu.Unlock()
			if released {
				break
			}
			select {
			case <-deadline:
				ticker.Stop()
				t.Fatal("media startup failure did not release ownership")
			case <-ticker.C:
			}
		}
		ticker.Stop()
	}
	if strings.Count(logs.String(), "starting media") != 2 || strings.Count(logs.String(), "media ended") != 2 {
		t.Fatal("later startup did not run after an endpoint-free failure")
	}
}
