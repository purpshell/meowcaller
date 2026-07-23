package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVideoBridgeControlDispatchesJSONCommand(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"dial_video","target":"15551234567"}`))
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.Action != "dial_video" || got.Target != "15551234567" {
		t.Fatalf("control = %+v", got)
	}
}

func TestVideoBridgeControlReportsCommandFailure(t *testing.T) {
	vb := &videoBridge{}
	vb.OnControl(func(vbControl) error { return errors.New("no active call") })
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"hangup"}`))
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestVideoBridgeControlRejectsUnknownAction(t *testing.T) {
	vb := &videoBridge{}
	vb.OnControl(func(vbControl) error { return nil })
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"explode"}`))
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestVideoBridgeControlDispatchesReaction(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"reaction","emoji":"👍"}`))
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent || got.Action != "reaction" || got.Emoji != "👍" {
		t.Fatalf("reaction control = (%d, %+v)", rec.Code, got)
	}
}

func TestVideoBridgeServesCurrentPairingQR(t *testing.T) {
	vb := &videoBridge{}
	vb.setQRCodePNG([]byte("png-bytes"))
	req := httptest.NewRequest(http.MethodGet, "/qr.png", nil)
	rec := httptest.NewRecorder()

	vb.handleQRCode(rec, req)

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("QR response = (%d, %q)", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("QR body = %q", rec.Body.String())
	}
}
