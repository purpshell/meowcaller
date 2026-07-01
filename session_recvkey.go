package meowcaller

import (
	"crypto/hmac"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
)

// The E2E-SRTP receive key must be derived from the peer's ACTUAL sending device
// LID ("<user>:<device>@lid"). NewMediaPipeline derives it from
// FormatE2ESrtpParticipantID(peerJID), and FormatParticipantID forces a bare @lid
// to device :0 — so whenever the peer streams from a non-zero device the recv
// key is wrong. Because the payload cipher is AES-CTR (length-preserving) and the
// WARP MI tag is not verified, a wrong key yields correct-length, high-entropy
// garbage with no decrypt error, which the (correct) MLow decoder renders as
// robotic audio. Sending is unaffected: the send key uses our own already
// device-qualified LID.
//
// This picks the right device without new signaling: the WARP MI tag is
// HMAC-SHA1 over the packet keyed by the sender's per-participant E2E auth key
// (the same key ProtectAudio signs with), so the peer's real device is the one
// whose derived recv key reproduces the tag. We derive a candidate key per device
// index and lock onto the MI-tag match on the first inbound packet.
//
// Source of truth (recv key is keyed off the peer's real device, not :0):
// https://github.com/JotaDev66/WaCalls — re-keys the receive direction from the
// peer's device-qualified JID (firstPeerDevice / reinitSrtp), yielding clean
// inbound audio with this same MLow decoder.

// maxRecvDeviceIndex bounds the peer device-index search for the E2E recv key.
const maxRecvDeviceIndex = 255

// maxRecvSelectPackets caps how many packets we probe before giving up and
// keeping the default recv key, so a non-matching stream costs bounded work.
const maxRecvSelectPackets = 250

type recvKeyCandidate struct {
	id   string
	keys srtp.E2eSrtpKeys
}

// buildRecvKeyCandidates derives candidate E2E recv keys across device-qualified
// peer participant ids, the library default (bare -> :0) first so a correct :0
// locks immediately.
func buildRecvKeyCandidates(callKey []byte, peerJID string, log zerolog.Logger) []recvKeyCandidate {
	var out []recvKeyCandidate
	seen := make(map[string]bool)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		keys, err := srtp.DeriveE2eKeys(callKey, id)
		if err != nil {
			log.Debug().Err(err).Str("participant", id).Msg("recv key candidate: derive failed")
			return
		}
		out = append(out, recvKeyCandidate{id: id, keys: keys})
	}
	add(rtp.FormatE2ESrtpParticipantID(peerJID))
	bare, _, _ := strings.Cut(peerJID, "/")
	bare = strings.TrimSpace(bare)
	if at := strings.LastIndexByte(bare, '@'); at > 0 {
		user := bare[:at]
		domain := bare[at+1:]
		base := user
		if i := strings.IndexByte(user, ':'); i >= 0 {
			base = user[:i]
		}
		if domain == "lid" {
			for d := 0; d <= maxRecvDeviceIndex; d++ {
				add(base + ":" + strconv.Itoa(d) + "@" + domain)
			}
		}
	}
	log.Debug().Int("candidate_count", len(out)).Msg("built e2e recv key candidates")
	return out
}

// selectRecvKey locks p.recvKeys onto the candidate whose WARP MI tag matches the
// packet. No-op once locked; after maxRecvSelectPackets probes it gives up and
// keeps the default key.
func (p *MediaPipeline) selectRecvKey(withoutTag, recvTag []byte, roc uint32) {
	if p.recvLocked {
		return
	}
	for i := range p.recvCandidates {
		c := &p.recvCandidates[i]
		if hmac.Equal(srtp.ComputeWarpMITag(c.keys.AuthKey[:], withoutTag, roc, p.warpMITagLen), recvTag) {
			p.recvKeys = c.keys
			p.recvLocked = true
			p.recvCandidates = nil
			p.log.Info().Str("recv_participant", c.id).Msg("locked e2e recv key via warp mi tag")
			return
		}
	}
	p.recvTries++
	if p.recvTries >= maxRecvSelectPackets {
		p.recvLocked = true
		p.recvCandidates = nil
		p.log.Warn().Int("tries", p.recvTries).Msg("no recv key matched warp mi tag; keeping default key")
	}
}
