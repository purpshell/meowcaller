package rtp

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type ssrcKat struct {
	Inputs struct {
		CallID  string `json:"callId"`
		PeerLid string `json:"peerLid"`
	} `json:"inputs"`
	VoipCrypto struct {
		SsrcSlot0 uint32 `json:"ssrc_slot0"`
		SsrcSlot1 uint32 `json:"ssrc_slot1"`
	} `json:"voip_crypto"`
}

func loadSsrcKat(t *testing.T) ssrcKat {
	t.Helper()
	raw, err := os.ReadFile("testdata/kats.json")
	if err != nil {
		t.Fatalf("read kats.json: %v", err)
	}
	var k ssrcKat
	if err := json.Unmarshal(raw, &k); err != nil {
		t.Fatalf("parse kats.json: %v", err)
	}
	return k
}

// TestSsrcMatchesKAT checks the per-slot SSRC derivation against kats.json.
func TestSsrcMatchesKAT(t *testing.T) {
	k := loadSsrcKat(t)
	got0, err := DeriveWasmParticipantSsrc(k.Inputs.CallID, k.Inputs.PeerLid, 0)
	if err != nil {
		t.Fatalf("slot 0: %v", err)
	}
	if got0 != k.VoipCrypto.SsrcSlot0 {
		t.Errorf("ssrc_slot0 = %d, want %d", got0, k.VoipCrypto.SsrcSlot0)
	}
	got1, err := DeriveWasmParticipantSsrc(k.Inputs.CallID, k.Inputs.PeerLid, 1)
	if err != nil {
		t.Fatalf("slot 1: %v", err)
	}
	if got1 != k.VoipCrypto.SsrcSlot1 {
		t.Errorf("ssrc_slot1 = %d, want %d", got1, k.VoipCrypto.SsrcSlot1)
	}
}

// TestFormatParticipantIDRules checks the device-suffix rules.
func TestFormatParticipantIDRules(t *testing.T) {
	cases := map[string]string{
		"12345@lid":            "12345:0@lid",
		"12345:6@lid":          "12345:6@lid",
		"12345@s.whatsapp.net": "12345@s.whatsapp.net",
	}
	for in, want := range cases {
		if got := FormatE2ESrtpParticipantID(in); got != want {
			t.Errorf("FormatE2ESrtpParticipantID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestE2EParticipantIDVariants checks the deduplicated recv-path LID variants.
func TestE2EParticipantIDVariants(t *testing.T) {
	got := E2EParticipantIDVariants("12345:6@lid")
	want := []string{"12345:6@lid", "12345:0@lid", "12345@lid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("variants = %q, want %q", got, want)
	}
}

func TestCapturedAppDataSsrcUsesSlotSix(t *testing.T) {
	got, err := DeriveWasmParticipantSsrc(
		"A8FE932D75AEE1DEDFC3A877E7238B88",
		"242653052539031:0@lid",
		AppDataSlotWord,
	)
	if err != nil {
		t.Fatalf("derive app-data SSRC: %v", err)
	}
	if got != 2135790987 {
		t.Fatalf("app-data SSRC = %d, want captured 2135790987", got)
	}
}
