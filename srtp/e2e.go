package srtp

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"

	"github.com/purpshell/meowcaller/util"
)

// errShortKey is returned when the supplied key material is shorter than the
// 32-byte minimum the E2E derivation requires.
var errShortKey = errors.New("srtp: key material shorter than 32 bytes")

// E2eSrtpKeys holds the per-participant session keys for the end-to-end 1:1 SRTP
// cipher: the AES-128 cipher key, the 14-byte master salt, and the auth key.
type E2eSrtpKeys struct {
	CipherKey [16]byte
	Salt      [14]byte
	AuthKey   [20]byte
}

// aesCmKdf is the AES-CM PRF (libsrtp KDF): IV = master salt with label XORed into
// byte 7, zero-padded to 16, then AES-128-CTR keystream over len zero bytes.
func aesCmKdf(masterKey, masterSalt []byte, label byte, length int) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L24-L32
	var iv [16]byte
	copy(iv[:14], masterSalt[:14])
	iv[7] ^= label
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, length)
	cipher.NewCTR(block, iv[:]).XORKeyStream(out, out)
	return out, nil
}

// deriveSessionKeysFromMaster splits the 46-byte master into key (16) + salt (14)
// and runs the AES-CM PRF three times (labels 0x00/0x01/0x02) for cipher/auth/salt.
func deriveSessionKeysFromMaster(master []byte) (E2eSrtpKeys, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L34-L49
	masterKey := master[0:16]
	masterSalt := master[16:30]
	var keys E2eSrtpKeys
	cipherKey, err := aesCmKdf(masterKey, masterSalt, 0x00, 16)
	if err != nil {
		return E2eSrtpKeys{}, err
	}
	copy(keys.CipherKey[:], cipherKey)
	authKey, err := aesCmKdf(masterKey, masterSalt, 0x01, 20)
	if err != nil {
		return E2eSrtpKeys{}, err
	}
	copy(keys.AuthKey[:], authKey)
	salt, err := aesCmKdf(masterKey, masterSalt, 0x02, 14)
	if err != nil {
		return E2eSrtpKeys{}, err
	}
	copy(keys.Salt[:], salt)
	return keys, nil
}

// DeriveE2eKeys derives the E2E 1:1 keys from callKey (>=32B) using participantLid
// as the HKDF info. It errors when callKey is shorter than 32 bytes.
func DeriveE2eKeys(callKey []byte, participantLid string) (E2eSrtpKeys, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L55-L61
	if len(callKey) < 32 {
		return E2eSrtpKeys{}, errShortKey
	}
	master, err := util.HKDFSHA256(make([]byte, 32), callKey[:32], []byte(participantLid), 46)
	if err != nil {
		return E2eSrtpKeys{}, err
	}
	return deriveSessionKeysFromMaster(master)
}

// DeriveE2eKeysFromRaw derives the E2E 1:1 keys from a keygen-v2 <raw_e2e> blob
// (>=32B) in place of callKey. It errors when rawE2e is shorter than 32 bytes.
func DeriveE2eKeysFromRaw(rawE2e []byte, participantLid string) (E2eSrtpKeys, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L64-L70
	if len(rawE2e) < 32 {
		return E2eSrtpKeys{}, errShortKey
	}
	master, err := util.HKDFSHA256(make([]byte, 32), rawE2e[:32], []byte(participantLid), 46)
	if err != nil {
		return E2eSrtpKeys{}, err
	}
	return deriveSessionKeysFromMaster(master)
}

// BuildE2eRtpIV builds the E2E RTP IV: salt right-aligned into 16 bytes, SSRC XORed
// at bytes 4-7, and the 48-bit packet index (ROC<<16 | seq) XORed at bytes 8-13.
func BuildE2eRtpIV(salt []byte, ssrc uint32, roc uint32, seq uint16) [16]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L74-L92
	var iv [16]byte
	off := 14 - len(salt)
	copy(iv[off:off+len(salt)], salt)
	iv[4] ^= byte(ssrc >> 24)
	iv[5] ^= byte(ssrc >> 16)
	iv[6] ^= byte(ssrc >> 8)
	iv[7] ^= byte(ssrc)
	packetIndex := uint64(roc)*0x1_0000 + uint64(seq)
	hi16 := uint16((packetIndex >> 32) & 0xffff)
	lo32 := uint32(packetIndex & 0xffff_ffff)
	iv[8] ^= byte(hi16 >> 8)
	iv[9] ^= byte(hi16)
	iv[10] ^= byte(lo32 >> 24)
	iv[11] ^= byte(lo32 >> 16)
	iv[12] ^= byte(lo32 >> 8)
	iv[13] ^= byte(lo32)
	return iv
}

// CryptPayload AES-128-CTR encrypts/decrypts an RTP payload (the cipher is symmetric).
func CryptPayload(keys *E2eSrtpKeys, ssrc uint32, seq uint16, roc uint32, payload []byte) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L95-L101
	iv := BuildE2eRtpIV(keys.Salt[:], ssrc, roc, seq)
	block, err := aes.NewCipher(keys.CipherKey[:])
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), payload...)
	cipher.NewCTR(block, iv[:]).XORKeyStream(out, out)
	return out, nil
}

// RocTracker is the send-side ROC tracker for monotonic 16-bit sequence numbers.
type RocTracker struct {
	roc         uint32
	lastSeq     uint16
	initialized bool
}

// Advance folds seq into the tracker and returns the current ROC, bumping it on the
// 0xFFFF->0x0000 wrap (a signed 16-bit gap below -32768).
func (t *RocTracker) Advance(seq uint16) uint32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L112-L124
	if !t.initialized {
		t.lastSeq = seq
		t.initialized = true
		return t.roc
	}
	if int32(seq)-int32(t.lastSeq) < -32768 {
		t.roc++
	}
	t.lastSeq = seq
	return t.roc
}

// RecvRocTracker is the recv-side ROC estimator (RFC 3711 guess-index): it tolerates
// reorder/loss by guessing each packet's ROC from the highest seq seen.
type RecvRocTracker struct {
	roc         uint32
	sL          uint16
	initialized bool
}

// GuessRoc guesses the ROC for seq and folds it into the state, seeding from the
// first packet (roc=0). A reordered late packet returns the lower ROC untouched.
func (t *RecvRocTracker) GuessRoc(seq uint16) uint32 {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L139-L168
	if !t.initialized {
		t.sL = seq
		t.initialized = true
		return t.roc
	}
	var v uint32
	if t.sL < 0x8000 {
		if int32(seq)-int32(t.sL) > 0x8000 {
			v = t.roc - 1
		} else {
			v = t.roc
		}
	} else if int32(t.sL)-int32(seq) > 0x8000 {
		v = t.roc + 1
	} else {
		v = t.roc
	}
	switch v {
	case t.roc:
		if seq > t.sL {
			t.sL = seq
		}
	case t.roc + 1:
		t.roc = v
		t.sL = seq
	}
	return v
}
