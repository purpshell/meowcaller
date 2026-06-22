package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// meowStore is meowcaller's OWN persistence — call/session state — kept in a
// separate SQLite file with its own *sql.DB from whatsmeow's auth store. The two
// never share a connection or a file lock, so meowcaller writes can't lock up or
// corrupt the whatsmeow device store. busy_timeout absorbs brief contention and a
// single open connection serializes writers (SQLite has one writer).
type meowStore struct{ db *sql.DB }

// callRecord is one call's meowcaller-side state: the per-call secret and the
// addressing/relay context the media path derives from.
type callRecord struct {
	CallID    string
	Direction string
	SelfLID   string
	PeerLID   string
	CallKey   []byte
	RelayIP   string
	RelayPort uint16
	Phase     string
}

// openMeowStore opens (creating if absent) the meowcaller call store at path.
func openMeowStore(ctx context.Context, path string) (*meowStore, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path))
	if err != nil {
		return nil, fmt.Errorf("open meowcaller store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS calls (
		call_id    TEXT PRIMARY KEY,
		direction  TEXT NOT NULL,
		self_lid   TEXT NOT NULL DEFAULT '',
		peer_lid   TEXT NOT NULL DEFAULT '',
		call_key   BLOB,
		relay_ip   TEXT NOT NULL DEFAULT '',
		relay_port INTEGER NOT NULL DEFAULT 0,
		phase      TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init meowcaller schema: %w", err)
	}
	return &meowStore{db: db}, nil
}

// SaveCall upserts one call record, stamping created/updated times.
func (s *meowStore) SaveCall(ctx context.Context, r callRecord) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO calls
		(call_id, direction, self_lid, peer_lid, call_key, relay_ip, relay_port, phase, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(call_id) DO UPDATE SET
			direction = excluded.direction,
			self_lid  = excluded.self_lid,
			peer_lid  = excluded.peer_lid,
			call_key  = excluded.call_key,
			relay_ip  = excluded.relay_ip,
			relay_port= excluded.relay_port,
			phase     = excluded.phase,
			updated_at= excluded.updated_at`,
		r.CallID, r.Direction, r.SelfLID, r.PeerLID, r.CallKey, r.RelayIP, r.RelayPort, r.Phase, now, now)
	return err
}

// SetPhase updates just the lifecycle phase of an existing call (no-op if absent).
func (s *meowStore) SetPhase(ctx context.Context, callID, phase string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE calls SET phase = ?, updated_at = ? WHERE call_id = ?`,
		phase, time.Now().Unix(), callID)
	return err
}

// CountCalls returns how many call records are stored (used to confirm the store
// is live and separate on startup).
func (s *meowStore) CountCalls(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calls`).Scan(&n)
	return n, err
}

// Close closes the underlying database handle.
func (s *meowStore) Close() error { return s.db.Close() }
