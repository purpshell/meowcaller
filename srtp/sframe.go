package srtp

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"

	"github.com/purpshell/meowcaller/util"
)

// errBadCallKeyLen is returned when the call key is not exactly 32 bytes, the only
// length the SFrame key derivation accepts.
var errBadCallKeyLen = errors.New("srtp: sframe call key must be exactly 32 bytes")

// SFrame KDF labels and lengths.
const (
	KDFLabelE2ESframe = "e2e sframe key"
	KDFLabelWarpAuth  = "warp auth key"
	gcmTagLen         = 16
	aesKeyLen         = 16
)

// FormatSframeParticipantID formats the participant id used as the SFrame HKDF info.
func FormatSframeParticipantID(jid string) string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L33-L35
	return util.FormatParticipantID(jid)
}

// SframeInfoLabel builds the HKDF info label "e2e sframe key<participantID>".
func SframeInfoLabel(participantID string) string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L37-L39
	return KDFLabelE2ESframe + participantID
}

// splitCallKey splits a 32-byte call key into (salt, ikm).
func splitCallKey(callKey []byte) (salt, ikm []byte, err error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L22-L27
	if len(callKey) != 32 {
		return nil, nil, errBadCallKeyLen
	}
	return callKey[0:16], callKey[16:32], nil
}

// DeriveE2eSframeKeyForParticipant derives the 32-byte per-participant SFrame key
// from callKey (exactly 32B), salt = callKey[0:16], ikm = callKey[16:32].
func DeriveE2eSframeKeyForParticipant(callKey []byte, participantID string) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L42-L54
	salt, ikm, err := splitCallKey(callKey)
	if err != nil {
		return nil, err
	}
	return util.HKDFSHA256(salt, ikm, []byte(SframeInfoLabel(participantID)), 32)
}

// DeriveWarpAuthKey derives the 32-byte WARP auth key from callKey (32B), empty
// salt, label "warp auth key".
func DeriveWarpAuthKey(callKey []byte) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L56-L66
	// TODO
	// agent suggestion: if len(callKey)!=32 return errBadCallKeyLen; util.HKDFSHA256(nil, callKey,
	// []byte(KDFLabelWarpAuth), 32). Left a stub — no KAT here; validate under #24 warp.
	// human input:
	return nil, nil
}

// counterToIV builds the 16-byte GCM nonce: 8 zero bytes then counter as LE uint64.
func counterToIV(counter uint64) [16]byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L88-L92
	var iv [16]byte
	binary.LittleEndian.PutUint64(iv[8:16], counter)
	return iv
}

// buildSframeHeader encodes [varint counter || varint keyID || total-length byte].
// binary.AppendUvarint is the same unsigned LEB128 as the reference encode_varint.
func buildSframeHeader(counter, keyID uint64) []byte {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L94-L101
	h := binary.AppendUvarint(nil, counter)
	h = binary.AppendUvarint(h, keyID)
	return append(h, byte(len(h)+1))
}

// parseSframeHeader decodes the trailing header, validating the total-length byte.
func parseSframeHeader(header []byte) (counter, keyID uint64, ok bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L103-L115
	if len(header) < 2 {
		return 0, 0, false
	}
	if int(header[len(header)-1]) != len(header) {
		return 0, 0, false
	}
	body := header[:len(header)-1]
	counter, n := binary.Uvarint(body)
	if n <= 0 {
		return 0, 0, false
	}
	keyID, n2 := binary.Uvarint(body[n:])
	if n2 <= 0 {
		return 0, 0, false
	}
	return counter, keyID, true
}

// gcmEncrypt seals plaintext with AES-128-GCM under the non-standard 16-byte nonce.
func gcmEncrypt(key []byte, nonce16 [16]byte, plaintext []byte) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L117-L123
	gcm, err := newSframeGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce16[:], plaintext, nil), nil
}

// gcmDecrypt opens ciphertext+tag with AES-128-GCM; ok=false on any auth failure.
func gcmDecrypt(key []byte, nonce16 [16]byte, ciphertextWithTag []byte) ([]byte, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L125-L129
	gcm, err := newSframeGCM(key)
	if err != nil {
		return nil, false
	}
	plain, err := gcm.Open(nil, nonce16[:], ciphertextWithTag, nil)
	if err != nil {
		return nil, false
	}
	return plain, true
}

// newSframeGCM builds AES-128-GCM with the non-standard 16-byte nonce size so the
// GHASH-derived J0 matches the reference.
func newSframeGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:aesKeyLen])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, 16)
}

// SframeSession holds the per-direction SFrame keys (encrypt for peer, decrypt for
// self) and the send-side counter.
type SframeSession struct {
	SelfParticipantID string
	PeerParticipantID string
	encryptKey        [16]byte
	decryptKey        [16]byte
	txCounter         uint64
}

// NewSframeSession builds a session from callKey and the self/peer JIDs.
func NewSframeSession(callKey []byte, selfJID, peerJID string) (*SframeSession, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L154-L172
	selfID := FormatSframeParticipantID(selfJID)
	peerID := FormatSframeParticipantID(peerJID)
	sendKey, err := DeriveE2eSframeKeyForParticipant(callKey, peerID)
	if err != nil {
		return nil, err
	}
	recvKey, err := DeriveE2eSframeKeyForParticipant(callKey, selfID)
	if err != nil {
		return nil, err
	}
	s := &SframeSession{SelfParticipantID: selfID, PeerParticipantID: peerID}
	copy(s.encryptKey[:], sendKey[:aesKeyLen])
	copy(s.decryptKey[:], recvKey[:aesKeyLen])
	return s, nil
}

// Encrypt seals one frame as [ciphertext || 16-byte tag || varint-header].
func (s *SframeSession) Encrypt(plaintext []byte) ([]byte, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L173-L187
	counter := s.txCounter
	s.txCounter++
	header := buildSframeHeader(counter, 0)
	iv := counterToIV(counter)
	encrypted, err := gcmEncrypt(s.encryptKey[:], iv, plaintext)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(encrypted)+len(header))
	out = append(out, encrypted...)
	out = append(out, header...)
	return out, nil
}

// Decrypt classifies one frame. It returns (plaintext, true) when the trailing
// SFrame header parses and the GCM tag authenticates; otherwise (nil, false),
// meaning the frame is plain Opus the caller must use verbatim.
func (s *SframeSession) Decrypt(frame []byte) ([]byte, bool) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/sframe.rs#L188-L213
	if len(frame) < gcmTagLen+3 {
		return nil, false
	}
	headerLen := int(frame[len(frame)-1])
	if headerLen < 3 || headerLen > len(frame) {
		return nil, false
	}
	headerStart := len(frame) - headerLen
	header := frame[headerStart:]
	ciphertext := frame[:headerStart]
	if len(ciphertext) < gcmTagLen+1 {
		return nil, false
	}
	counter, _, ok := parseSframeHeader(header)
	if !ok {
		return nil, false
	}
	iv := counterToIV(counter)
	return gcmDecrypt(s.decryptKey[:], iv, ciphertext)
}
