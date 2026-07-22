package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorWritesBatchesAsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{file: file}
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(`[ { "event": "one" }, { "event": "two" } ]`))
	res := httptest.NewRecorder()
	c.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /events returned %d: %s", res.Code, res.Body.String())
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	written, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer written.Close()
	scanner := bufio.NewScanner(written)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 2 || lines[0] != `{"event":"one"}` || lines[1] != `{"event":"two"}` {
		t.Fatalf("unexpected JSONL: %#v", lines)
	}
}
