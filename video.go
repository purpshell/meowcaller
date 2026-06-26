package meowcaller

import (
	"bufio"
	"os"
	"sync"
)

// Video in meowcaller is encoded H.264, carried as Annex-B access units (one frame's worth
// of start-code-prefixed NAL units). meowcaller does not encode or decode pixels — an
// external codec (the browser's WebCodecs, a hardware encoder, ffmpeg, …) produces and
// consumes the H.264; meowcaller carries it over the WhatsApp relay. The send/receive model
// mirrors audio (see audio.go): attach a VideoSink with Call.ReceiveVideo for the peer's
// video, and push your encoded frames with Call.SendVideo.

// VideoSink consumes the encoded H.264 access units received from the peer. Attach one with
// Call.ReceiveVideo; the built-in AnnexBRecorder records to a raw .h264 file, or use
// VideoSinkFunc to forward to a callback. Without a sink the peer's video is discarded.
type VideoSink interface {
	// WriteVideo consumes one Annex-B H.264 access unit from the peer.
	WriteVideo(accessUnit []byte) error
	// Close flushes and releases the sink. Safe to call more than once.
	Close() error
}

// VideoSinkFunc adapts a plain function to a VideoSink (Close is a no-op).
type VideoSinkFunc func(accessUnit []byte)

// WriteVideo calls f.
func (f VideoSinkFunc) WriteVideo(accessUnit []byte) error { f(accessUnit); return nil }

// Close is a no-op for VideoSinkFunc.
func (f VideoSinkFunc) Close() error { return nil }

// VideoState is the peer's video state from a mid-call <video> stanza, delivered to
// Call.OnVideoState.
type VideoState struct {
	// Active reports the peer's camera is on (state == 1).
	Active bool
	// Upgrade reports a mid-call audio→video upgrade (state == 11).
	Upgrade bool
	// Orientation is the peer's device orientation (0..3); rotate the rendered video by
	// Orientation × 90° to display upright.
	Orientation int
	// Raw is the unmapped "state" attribute value.
	Raw int
}

// annexBRecorder records the peer's H.264 to a raw Annex-B .h264 file.
type annexBRecorder struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	closed bool
}

// AnnexBRecorder creates a VideoSink that records the peer's H.264 to a raw Annex-B .h264
// file at path (playable directly by ffmpeg/VLC). Close finalizes it — the video analog of
// WAVRecorder.
func AnnexBRecorder(path string) (VideoSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &annexBRecorder{f: f, w: bufio.NewWriter(f)}, nil
}

// WriteVideo appends one Annex-B access unit.
func (r *annexBRecorder) WriteVideo(accessUnit []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	_, err := r.w.Write(accessUnit)
	return err
}

// Close flushes and closes the file. Safe to call more than once.
func (r *annexBRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.w.Flush(); err != nil {
		r.f.Close()
		return err
	}
	return r.f.Close()
}
