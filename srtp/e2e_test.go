package srtp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type kat struct {
	Inputs struct {
		CallKey string `json:"callKey"`
		SelfLid string `json:"selfLid"`
		PeerLid string `json:"peerLid"`
		SSRC    uint32 `json:"ssrc"`
		Seq     uint16 `json:"seq"`
		Roc     uint32 `json:"roc"`
		Payload string `json:"payload"`
	} `json:"inputs"`
	E2eSrtp struct {
		PeerCipherKey string `json:"peer_cipherKey"`
		PeerSalt      string `json:"peer_salt"`
		PeerAuthKey   string `json:"peer_authKey"`
		SelfCipherKey string `json:"self_cipherKey"`
		SelfSalt      string `json:"self_salt"`
		SelfAuthKey   string `json:"self_authKey"`
		RtpIv         string `json:"rtpIv"`
		CipherOut     string `json:"cipher_out"`
	} `json:"e2e_srtp"`
}

func loadKat(t *testing.T) kat {
	t.Helper()
	raw, err := os.ReadFile("testdata/kats.json")
	if err != nil {
		t.Fatalf("read kats.json: %v", err)
	}
	var k kat
	if err := json.Unmarshal(raw, &k); err != nil {
		t.Fatalf("parse kats.json: %v", err)
	}
	return k
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestDeriveE2eKeysMatchesKAT derives the peer and self session keys from the call
// key and asserts cipher/salt/auth match the kats.json e2e_srtp expectations.
func TestDeriveE2eKeysMatchesKAT(t *testing.T) {
	k := loadKat(t)
	callKey := mustHex(t, k.Inputs.CallKey)

	peer, err := DeriveE2eKeys(callKey, k.Inputs.PeerLid)
	if err != nil {
		t.Fatalf("peer derive: %v", err)
	}
	if !bytes.Equal(peer.CipherKey[:], mustHex(t, k.E2eSrtp.PeerCipherKey)) {
		t.Errorf("peer cipher_key = %x, want %s", peer.CipherKey, k.E2eSrtp.PeerCipherKey)
	}
	if !bytes.Equal(peer.Salt[:], mustHex(t, k.E2eSrtp.PeerSalt)) {
		t.Errorf("peer salt = %x, want %s", peer.Salt, k.E2eSrtp.PeerSalt)
	}
	if !bytes.Equal(peer.AuthKey[:], mustHex(t, k.E2eSrtp.PeerAuthKey)) {
		t.Errorf("peer auth_key = %x, want %s", peer.AuthKey, k.E2eSrtp.PeerAuthKey)
	}

	self, err := DeriveE2eKeys(callKey, k.Inputs.SelfLid)
	if err != nil {
		t.Fatalf("self derive: %v", err)
	}
	if !bytes.Equal(self.CipherKey[:], mustHex(t, k.E2eSrtp.SelfCipherKey)) {
		t.Errorf("self cipher_key = %x, want %s", self.CipherKey, k.E2eSrtp.SelfCipherKey)
	}
	if !bytes.Equal(self.AuthKey[:], mustHex(t, k.E2eSrtp.SelfAuthKey)) {
		t.Errorf("self auth_key = %x, want %s", self.AuthKey, k.E2eSrtp.SelfAuthKey)
	}
}

// TestRtpIVMatchesKAT builds the per-packet IV from the peer salt and asserts it
// matches the kats.json rtpIv.
func TestRtpIVMatchesKAT(t *testing.T) {
	k := loadKat(t)
	var salt [14]byte
	copy(salt[:], mustHex(t, k.E2eSrtp.PeerSalt))
	iv := BuildE2eRtpIV(salt[:], k.Inputs.SSRC, k.Inputs.Roc, k.Inputs.Seq)
	if got := hex.EncodeToString(iv[:]); got != k.E2eSrtp.RtpIv {
		t.Errorf("rtpIv = %s, want %s", got, k.E2eSrtp.RtpIv)
	}
}

// TestCryptPayloadMatchesKAT encrypts the payload and asserts the ciphertext matches
// cipher_out, then decrypts to confirm the cipher round-trips.
func TestCryptPayloadMatchesKAT(t *testing.T) {
	k := loadKat(t)
	var keys E2eSrtpKeys
	copy(keys.CipherKey[:], mustHex(t, k.E2eSrtp.PeerCipherKey))
	copy(keys.Salt[:], mustHex(t, k.E2eSrtp.PeerSalt))
	payload := mustHex(t, k.Inputs.Payload)

	ct, err := CryptPayload(&keys, k.Inputs.SSRC, k.Inputs.Seq, k.Inputs.Roc, payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if got := hex.EncodeToString(ct); got != k.E2eSrtp.CipherOut {
		t.Errorf("cipher_out = %s, want %s", got, k.E2eSrtp.CipherOut)
	}
	pt, err := CryptPayload(&keys, k.Inputs.SSRC, k.Inputs.Seq, k.Inputs.Roc, ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(pt, payload) {
		t.Errorf("decrypt round-trip = %x, want %x", pt, payload)
	}
}

// TestRocTrackerWraps exercises both the send-side monotonic tracker and the
// recv-side guess estimator across wraps, reorder dips, and late packets.
func TestRocTrackerWraps(t *testing.T) {
	var tx RocTracker
	if got := tx.Advance(0xFFFE); got != 0 {
		t.Fatalf("seed: roc=%d, want 0", got)
	}
	tx.Advance(0xFFFF)
	if got := tx.Advance(0x0000); got != 1 {
		t.Errorf("0xFFFF->0x0000: roc=%d, want 1", got)
	}
	tx.Advance(0x0001)
	if got := tx.Advance(0x0000); got != 1 {
		t.Errorf("backward dip must not bump: roc=%d, want 1", got)
	}
	tx.Advance(0x0001)
	for _, s := range []uint16{0x7000, 0xE000, 0xFFFF} {
		tx.Advance(s)
	}
	if got := tx.Advance(0x0000); got != 2 {
		t.Errorf("second wrap: roc=%d, want 2", got)
	}

	var rx RecvRocTracker
	if got := rx.GuessRoc(0xFFFE); got != 0 {
		t.Fatalf("recv seed: roc=%d, want 0", got)
	}
	rx.GuessRoc(0xFFFF)
	if got := rx.GuessRoc(0x0000); got != 1 {
		t.Errorf("recv 0xFFFF->0x0000: roc=%d, want 1", got)
	}
	rx.GuessRoc(0x0001)
	if got := rx.GuessRoc(0x0000); got != 1 {
		t.Errorf("reordered dip stays in roc: roc=%d, want 1", got)
	}
	if got := rx.GuessRoc(0x0002); got != 1 {
		t.Errorf("state intact after dip: roc=%d, want 1", got)
	}
	for _, s := range []uint16{0x7000, 0xE000, 0xFFFF} {
		if got := rx.GuessRoc(s); got != 1 {
			t.Errorf("walk forward seq=%#x: roc=%d, want 1", s, got)
		}
	}
	if got := rx.GuessRoc(0x0000); got != 2 {
		t.Errorf("recv second wrap: roc=%d, want 2", got)
	}
	if got := rx.GuessRoc(0xFFF0); got != 1 {
		t.Errorf("late packet returns lower roc: roc=%d, want 1", got)
	}
	if got := rx.GuessRoc(0x0001); got != 2 {
		t.Errorf("state not corrupted by late packet: roc=%d, want 2", got)
	}
}
