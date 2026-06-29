package rtp

import "time"

// VideoRtpExtensionProfile is the RFC 5285 one-byte header-extension profile (0xBEDE)
// carried on every outbound H.264 packet, distinct from the audio WARP extension word
// (WhatsappRtpExtensionProfile, 0xdebe). The peer drops video from senders that omit it;
// audio is exempt.
const VideoRtpExtensionProfile uint16 = 0xbede

// Video header-extension element IDs (RFC 5285 one-byte form).
const (
	videoExtIDAbsSendTime  = 3 // http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time
	videoExtIDOrientation  = 5 // urn:3gpp:video-orientation (RFC 7742, CVO)
	videoExtIDTransportCC  = 6 // http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions
	videoExtAbsSendTimeLen = 3
	videoExtOrientationLen = 1
	videoExtTransportCCLen = 2
)

// BuildVideoRtpExtension builds the one-byte (RFC 5285) header-extension block carried on
// every outbound H.264 packet:
//
//	id=3 abs-send-time      (3 bytes) — 24-bit 6.18 fixed-point of the wall clock
//	id=5 video-orientation  (1 byte)  — urn:3gpp:video-orientation (CVO); 0 = upright
//	id=6 transport-wide-cc  (2 bytes) — per-packet feedback sequence number
//
// The elements are zero-padded to a 12-byte (3 × 32-bit word) block. Pair it with
// VideoRtpExtensionProfile on the RtpHeader and increment tccSeq once per packet sent.
func BuildVideoRtpExtension(tccSeq uint16, cvo byte) []byte {
	// abs-send-time: 24-bit unsigned, 6.18 fixed-point of the current wall-clock seconds.
	secs := float64(time.Now().UnixNano()) / 1e9
	abs := uint32(secs*262144.0) & 0xffffff

	ext := make([]byte, 12)
	ext[0] = byte(videoExtIDAbsSendTime<<4) | byte(videoExtAbsSendTimeLen-1) // 0x32
	ext[1] = byte(abs >> 16)
	ext[2] = byte(abs >> 8)
	ext[3] = byte(abs)
	ext[4] = byte(videoExtIDOrientation<<4) | byte(videoExtOrientationLen-1) // 0x50
	ext[5] = cvo
	ext[6] = byte(videoExtIDTransportCC<<4) | byte(videoExtTransportCCLen-1) // 0x61
	ext[7] = byte(tccSeq >> 8)
	ext[8] = byte(tccSeq)
	// ext[9..11] = padding (0x00)
	return ext
}
