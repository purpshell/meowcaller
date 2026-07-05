package main

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePairArgs(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "session.db")
	var stderr strings.Builder
	cfg, err := parsePairArgs([]string{"--store", storePath}, &stderr)
	if err != nil {
		t.Fatalf("parsePairArgs: %v", err)
	}
	if cfg.storePath != storePath {
		t.Fatalf("store path = %q, want %q", cfg.storePath, storePath)
	}
}

func TestParsePairArgsRejectsPositionals(t *testing.T) {
	_, err := parsePairArgs([]string{"unexpected"}, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("error = %v, want usage error", err)
	}
}
