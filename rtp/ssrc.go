package rtp

import (
	"encoding/binary"
	"strings"

	"github.com/purpshell/meowcaller/util"
	"github.com/rs/zerolog"
)

// SSRC derivation and participant-LID helpers for E2E HKDF info.

// WasmRelayStreamSlotWords are the slot words for the 9-stream relay allocate plan.
var WasmRelayStreamSlotWords = [9]uint32{0, 1, 4, 2, 3, 5, 7, 8, 6}

// VideoSlotWord is the relay stream slot for a participant's video SSRC (audio is slot 0).
// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/callmanager_video.go#L13
const VideoSlotWord uint32 = 2

// DeriveWasmParticipantSsrc derives a participant/stream SSRC:
// HKDF-SHA256(salt=slotWord LE32, ikm=callID, info=lid, 4), read back as LE u32.
func DeriveWasmParticipantSsrc(callID, lid string, slotWord uint32, log ...zerolog.Logger) (uint32, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/ssrc.rs#L11-L17
	lg := pickLog(log)
	salt := binary.LittleEndian.AppendUint32(nil, slotWord)
	okm, err := util.HKDFSHA256(salt, []byte(callID), []byte(lid), 4)
	if err != nil {
		lg.Debug().Err(err).Str("call_id", callID).Str("lid", lid).Uint32("slot_word", slotWord).Msg("derive participant ssrc: HKDF failed")
		return 0, err
	}
	ssrc := binary.LittleEndian.Uint32(okm)
	lg.Trace().Str("call_id", callID).Str("lid", lid).Uint32("slot_word", slotWord).Uint32("ssrc", ssrc).Msg("derived participant ssrc")
	return ssrc, nil
}

// DeriveWasmRelayStreamSsrcs derives all 9 relay-stream SSRCs in slot order.
func DeriveWasmRelayStreamSsrcs(callID, lid string, log ...zerolog.Logger) ([9]uint32, error) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/ssrc.rs#L20-L22
	lg := pickLog(log)
	var out [9]uint32
	for i, slot := range WasmRelayStreamSlotWords {
		ssrc, err := DeriveWasmParticipantSsrc(callID, lid, slot, lg)
		if err != nil {
			lg.Debug().Err(err).Str("call_id", callID).Str("lid", lid).Int("stream_index", i).Msg("derive relay stream ssrcs: failed")
			return [9]uint32{}, err
		}
		out[i] = ssrc
	}
	lg.Debug().Str("call_id", callID).Str("lid", lid).Int("stream_count", len(out)).Msg("derived relay stream ssrcs")
	return out, nil
}

// FormatE2ESrtpParticipantID formats the device-qualified LID for E2E-SRTP HKDF info.
func FormatE2ESrtpParticipantID(jid string) string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/ssrc.rs#L28-L30
	return util.FormatParticipantID(jid)
}

// E2EParticipantIDVariants lists the device-qualified LID variants the recv path
// tries as HKDF info (peer sender LIDs), deduplicated in insertion order.
func E2EParticipantIDVariants(jid string, log ...zerolog.Logger) []string {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/ssrc.rs#L33-L56
	lg := pickLog(log)
	var out []string
	seen := make(map[string]bool)
	push := func(s string) {
		t := strings.TrimSpace(s)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	bare, _, _ := strings.Cut(jid, "/")
	bare = strings.TrimSpace(bare)
	push(bare)
	push(FormatE2ESrtpParticipantID(jid))
	if at := strings.LastIndexByte(bare, '@'); at > 0 {
		user := bare[:at]
		domain := bare[at+1:]
		if domain == "lid" && strings.Contains(user, ":") {
			base, _, _ := strings.Cut(user, ":")
			push(base + ":0@" + domain)
			push(base + "@" + domain)
		}
	}
	lg.Trace().Str("jid", jid).Int("variant_count", len(out)).Msg("computed e2e participant id variants")
	return out
}
