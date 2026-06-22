package main

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMeowStore exercises the separate meowcaller call store: create, upsert, phase
// update, and that it persists across reopen (its own file, no whatsmeow involved).
func TestMeowStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meowcaller.db")

	st, err := openMeowStore(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if n, err := st.CountCalls(ctx); err != nil || n != 0 {
		t.Fatalf("fresh count = %d, %v; want 0, nil", n, err)
	}

	rec := callRecord{
		CallID: "abc123", Direction: "outgoing",
		SelfLID: "1@lid", PeerLID: "2@lid",
		CallKey: []byte{0xde, 0xad, 0xbe, 0xef}, Phase: "calling",
	}
	if err := st.SaveCall(ctx, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Upsert the same call_id with relay info — count stays 1.
	rec.Phase = "media"
	rec.RelayIP, rec.RelayPort = "1.2.3.4", 3478
	if err := st.SaveCall(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n, _ := st.CountCalls(ctx); n != 1 {
		t.Fatalf("after upsert count = %d; want 1", n)
	}
	if err := st.SetPhase(ctx, "abc123", "terminated"); err != nil {
		t.Fatalf("setphase: %v", err)
	}
	st.Close()

	// Reopen the same file — the record is still there (real on-disk persistence).
	st2, err := openMeowStore(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	var phase string
	var key []byte
	if err := st2.db.QueryRowContext(ctx,
		`SELECT phase, call_key FROM calls WHERE call_id = ?`, "abc123").Scan(&phase, &key); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if phase != "terminated" {
		t.Errorf("phase = %q; want terminated", phase)
	}
	if string(key) != string([]byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Errorf("call_key = %x; want deadbeef", key)
	}
}
