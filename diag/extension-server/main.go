package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type collector struct {
	mu    sync.Mutex
	file  *os.File
	count uint64
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "content-type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		c.mu.Lock()
		count := c.count
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": count})
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/events" {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": err.Error()})
		return
	}
	var events []json.RawMessage
	if len(bytes.TrimSpace(body)) > 0 && bytes.TrimSpace(body)[0] == '[' {
		err = json.Unmarshal(body, &events)
	} else {
		events = []json.RawMessage{body}
	}
	if err != nil || len(events) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid event batch"})
		return
	}

	lines := make([][]byte, 0, len(events))
	for _, event := range events {
		var compact bytes.Buffer
		if !json.Valid(event) || json.Compact(&compact, event) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid event"})
			return
		}
		lines = append(lines, append(compact.Bytes(), '\n'))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range lines {
		if _, err = c.file.Write(line); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		c.count++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": c.count})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:3219", "HTTP listen address")
	output := flag.String("output", "", "JSONL output file")
	flag.Parse()
	if *output == "" {
		*output = filepath.Join("diag", "captures", "whatsapp-"+time.Now().Format("20060102-150405")+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		log.Fatal(err)
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	fmt.Printf("collecting on http://%s -> %s\n", *listen, *output)
	log.Fatal(http.ListenAndServe(*listen, &collector{file: file}))
}
