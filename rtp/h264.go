package rtp

// H.264 RTP packetization/depacketization (RFC 6184) for WhatsApp video calls.
//
// Ported from WaCalls (jotadev66, MIT) feat/video-calls — the upstream sibling
// pure-Go WhatsApp VoIP stack whose video support this is based on. See CREDITS in
// README. Each function carries a // Source of truth: permalink to the WaCalls origin.

const (
	h264StapAType = 24 // STAP-A aggregation packet NAL type (RFC 6184 §5.7.1)
	h264FuaType   = 28 // FU-A fragmentation unit NAL type (RFC 6184 §5.8)
	mtuPayloadMax = 800

	maxFuReassemblyBytes = 4 << 20
)

// PackageH264NALU splits one NAL unit into RTP payloads: a single payload when it fits
// the MTU budget, else FU-A fragments.
func PackageH264NALU(nalu []byte) [][]byte {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/transport/h264_packet.go#L11-L55
	if len(nalu) == 0 {
		return nil
	}

	if len(nalu) <= mtuPayloadMax {
		out := make([]byte, len(nalu))
		copy(out, nalu)
		return [][]byte{out}
	}
	naluHeader := nalu[0]
	fbitAndNri := naluHeader & 0xE0
	originalType := naluHeader & 0x1F
	fuIndicator := fbitAndNri | h264FuaType

	body := nalu[1:]
	fragSize := mtuPayloadMax - 2

	var out [][]byte
	offset := 0
	for offset < len(body) {
		end := offset + fragSize
		if end > len(body) {
			end = len(body)
		}
		chunk := body[offset:end]

		fuHeader := originalType
		if offset == 0 {
			fuHeader |= 0x80
		}
		if end == len(body) {
			fuHeader |= 0x40
		}

		pkt := make([]byte, 2+len(chunk))
		pkt[0] = fuIndicator
		pkt[1] = fuHeader
		copy(pkt[2:], chunk)
		out = append(out, pkt)

		offset = end
	}
	return out
}

// PackageH264STAPA aggregates several small NAL units into one STAP-A payload, or nil
// when there are fewer than two or the aggregate would exceed the MTU budget.
func PackageH264STAPA(nalus [][]byte) []byte {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/transport/h264_packet.go#L57-L88
	if len(nalus) < 2 {
		return nil
	}
	total := 1
	var maxNri byte
	for _, n := range nalus {
		if len(n) == 0 || len(n) > 0xFFFF {
			return nil
		}
		total += 2 + len(n)
		if total > mtuPayloadMax {
			return nil
		}
		nri := n[0] & 0x60
		if nri > maxNri {
			maxNri = nri
		}
	}

	out := make([]byte, total)
	out[0] = maxNri | h264StapAType
	offset := 1
	for _, n := range nalus {
		out[offset] = byte(len(n) >> 8)
		out[offset+1] = byte(len(n))
		offset += 2
		copy(out[offset:], n)
		offset += len(n)
	}
	return out
}

// SplitAnnexB splits an Annex-B byte stream (00 00 01 / 00 00 00 01 start codes) into
// its constituent NAL units, trimming trailing zero bytes before each start code.
func SplitAnnexB(data []byte) [][]byte {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/transport/h264_packet.go#L90-L116
	var nalus [][]byte
	start := -1
	i := 0
	for i < len(data) {
		sc := annexBStartCodeLen(data, i)
		if sc > 0 {
			if start >= 0 {
				end := i
				for end > start && data[end-1] == 0 {
					end--
				}
				if end > start {
					nalus = append(nalus, data[start:end])
				}
			}
			i += sc
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(data) {
		nalus = append(nalus, data[start:])
	}
	return nalus
}

// annexBStartCodeLen returns the length (3 or 4) of an Annex-B start code at offset, or 0.
func annexBStartCodeLen(data []byte, offset int) int {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/transport/h264_packet.go#L118-L129
	if offset+3 < len(data) &&
		data[offset] == 0 && data[offset+1] == 0 &&
		data[offset+2] == 0 && data[offset+3] == 1 {
		return 4
	}
	if offset+2 < len(data) &&
		data[offset] == 0 && data[offset+1] == 0 && data[offset+2] == 1 {
		return 3
	}
	return 0
}

// H264Depacketizer reassembles H.264 NAL units from RFC 6184 RTP payloads (single NAL,
// STAP-A, FU-A), carrying FU-A fragment state across packets.
type H264Depacketizer struct {
	fuBuf    []byte
	fuType   byte
	fuNriF   byte
	fuActive bool
}

// Depacketize consumes one RTP payload and returns the NAL units it produced (zero for a
// mid-fragment, one for single/FU-A completion, many for STAP-A).
func (d *H264Depacketizer) Depacketize(payload []byte) [][]byte {
	// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/transport/h264_packet.go#L138-L210
	if len(payload) < 1 {
		return nil
	}
	naluType := payload[0] & 0x1F
	fbitAndNri := payload[0] & 0xE0

	switch {
	case naluType >= 1 && naluType <= 23:
		d.fuActive = false
		out := make([]byte, len(payload))
		copy(out, payload)
		return [][]byte{out}

	case naluType == h264StapAType:
		d.fuActive = false
		body := payload[1:]
		var out [][]byte
		for len(body) >= 2 {
			size := int(body[0])<<8 | int(body[1])
			body = body[2:]
			if size <= 0 || size > len(body) {
				return out
			}
			nalu := make([]byte, size)
			copy(nalu, body[:size])
			out = append(out, nalu)
			body = body[size:]
		}
		return out

	case naluType == h264FuaType:
		if len(payload) < 2 {
			d.fuActive = false
			return nil
		}
		fuHeader := payload[1]
		startBit := fuHeader & 0x80
		endBit := fuHeader & 0x40
		origType := fuHeader & 0x1F
		body := payload[2:]

		if startBit != 0 {
			d.fuActive = true
			d.fuType = origType
			d.fuNriF = fbitAndNri
			d.fuBuf = append(d.fuBuf[:0], fbitAndNri|origType)
			d.fuBuf = append(d.fuBuf, body...)
		} else if d.fuActive {
			if len(d.fuBuf)+len(body) > maxFuReassemblyBytes {
				d.fuActive = false
				d.fuBuf = d.fuBuf[:0]
				return nil
			}
			d.fuBuf = append(d.fuBuf, body...)
		} else {
			return nil
		}

		if endBit != 0 && d.fuActive {
			d.fuActive = false
			out := make([]byte, len(d.fuBuf))
			copy(out, d.fuBuf)
			d.fuBuf = d.fuBuf[:0]
			return [][]byte{out}
		}
		return nil

	default:
		d.fuActive = false
		return nil
	}
}
