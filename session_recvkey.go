package meowcaller

import (
	"crypto/hmac"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
)

const maxRecvDeviceIndex = 255
const maxRecvSelectPackets = 250

type recvKeyCandidate struct {
	id   string
	keys srtp.E2eSrtpKeys
}

// buildRecvKeyCandidates derives candidate E2E recv keys across device-qualified
// peer participant IDs. The default (bare -> :0) is first so a correct :0 locks
// immediately. All device indices 0..255 are tried to handle inbound RTP from an
// unknown companion device.
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
