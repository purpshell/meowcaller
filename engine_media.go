package meowcaller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/relay"
	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/stun"
)

// The live-relay media loop: connect+allocate to the elected relay, then run the
// per-frame send/recv loop. Outbound pulls frames from the Call's Player (silence when
// idle), encodes via MLow + ProtectAudio, and sends to the relay; inbound classifies
// relay packets, unprotects+decodes RTP, and writes to the Call's sink.

// maybeStartMedia launches the media loop for callID once both the callKey and the relay
// endpoint are known. It is idempotent — the loop starts exactly once per call.
func (e *engine) maybeStartMedia(callID string) {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.started || m.callKey == nil || m.relay == nil {
		e.mu.Unlock()
		return
	}
	m.started = true
	mctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	call := m.call
	callKey, selfLID, peerLID, rd := m.callKey, m.selfLID, m.peerLID, m.relay
	e.mu.Unlock()

	if call != nil {
		call.setPhase(CallPhaseConnecting)
	}
	e.c.log.Info().Str("call_id", callID).Msg("starting media")
	go func() {
		if err := e.runMedia(mctx, callID, call, callKey, selfLID, peerLID, rd); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("media ended")
		}
	}()
}

// connectAndAllocate opens the relay DataChannel and sends the STUN allocate, returning
// the channel and the allocate bytes (re-sent by the keepalive).
//
// NOT VALIDATED: live-relay only.
func (e *engine) connectAndAllocate(ctx context.Context, rd *relayData) (*relay.RelayMediaChannel, []byte, error) {
	log := e.c.log
	ep := getMediaRelayEndpoint(rd)
	if ep == nil || len(ep.addresses) == 0 {
		return nil, nil, fmt.Errorf("relay has no usable endpoint")
	}
	addr := &net.UDPAddr{IP: net.ParseIP(ep.addresses[0].ipv4), Port: int(ep.addresses[0].port)}
	log.Info().Str("relay_name", ep.relayName).Str("addr", addr.String()).Msg("connecting media transport to relay")
	e.c.diag.Emit("relay", map[string]any{
		"event": "endpoint", "relay_name": ep.relayName,
		"ipv4": ep.addresses[0].ipv4, "port": ep.addresses[0].port, "token_id": ep.tokenID,
	})

	type result struct {
		ch  *relay.RelayMediaChannel
		err error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := relay.ConnectRelayMedia(addr, relay.WithLogger(log))
		done <- result{ch, err}
	}()
	var ch *relay.RelayMediaChannel
	select {
	case r := <-done:
		if r.err != nil {
			return nil, nil, fmt.Errorf("relay connect: %w", r.err)
		}
		ch = r.ch
	case <-time.After(12 * time.Second):
		return nil, nil, fmt.Errorf("relay connect timed out (DTLS didn't complete)")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	log.Info().Str("relay_name", ep.relayName).Msg("relay DataChannel open")

	if int(ep.tokenID) >= len(rd.relayTokens) || rd.relayTokens[ep.tokenID] == nil {
		ch.Close()
		return nil, nil, fmt.Errorf("no relay token #%d", ep.tokenID)
	}
	if len(rd.relayKeyASCII) == 0 {
		ch.Close()
		return nil, nil, fmt.Errorf("relay has no <key>")
	}
	e.c.diag.Emit("relay", map[string]any{
		"event": "keying", "token_id": ep.tokenID, "token_count": len(rd.relayTokens),
		"relay_key_hex": hex.EncodeToString(rd.relayKeyASCII),
		"token_hex":     hex.EncodeToString(rd.relayTokens[ep.tokenID]),
	})
	endpointXor, ok := stun.EncodeXorRelayEndpoint(ep.addresses[0].ipv4, ep.addresses[0].port, log)
	if !ok {
		ch.Close()
		return nil, nil, fmt.Errorf("bad endpoint XOR")
	}
	var tx [12]byte
	_, _ = rand.Read(tx[:])
	allocate := stun.BuildWasmStunAllocateRequest(tx, rd.relayTokens[ep.tokenID], endpointXor, rd.relayKeyASCII, log)
	if _, err := ch.Send(allocate); err != nil {
		ch.Close()
		return nil, nil, fmt.Errorf("allocate send: %w", err)
	}
	log.Info().Int("bytes", len(allocate)).Msg("sent STUN allocate")
	e.c.diag.Emit("stun", map[string]any{
		"event": "allocate_sent", "bytes": len(allocate),
		"tx_id_hex": hex.EncodeToString(tx[:]), "allocate_hex": hex.EncodeToString(allocate),
	})
	return ch, allocate, nil
}

// runMedia runs the per-frame media loop over the relay DataChannel: the Player's frames
// (or silence) → MLow → E2E-SRTP protect → DataChannel, and DataChannel → classify →
// unprotect → MLow decode → the Call's sink. A 1 Hz allocate+ping keepalive holds the
// relay's consent freshness; the relay's binding-requests are answered with
// binding-success. The working recipe is preserved exactly: a consent ping (0x0801) goes
// out with the allocate at t+0, BEFORE any RTP; no STUN binding-requests are ever sent.
//
// NOT VALIDATED: live-relay only.
func (e *engine) runMedia(ctx context.Context, callID string, call *Call, callKey []byte, selfLID, peerLID string, rd *relayData) error {
	log := e.c.log
	ch, allocate, err := e.connectAndAllocate(ctx, rd)
	if err != nil {
		return err
	}
	defer ch.Close()

	// Send a consent ping (0x0801) immediately, together with the allocate and BEFORE any
	// RTP. The relay won't forward the peer's media until consent (ping → pong) is
	// established; RTP sent before the first ping is dropped and the relay never bridges.
	{
		var ptx [12]byte
		_, _ = rand.Read(ptx[:])
		initPing := stun.BuildWhatsappPing(ptx, log)
		_, _ = ch.Send(initPing[:])
		e.c.diag.Emit("stun", map[string]any{
			"event": "consent_ping_sent", "tx_id_hex": hex.EncodeToString(ptx[:]),
			"ping_hex": hex.EncodeToString(initPing[:]),
		})
	}

	ssrc, err := rtp.DeriveWasmParticipantSsrc(callID, rtp.FormatE2ESrtpParticipantID(selfLID), 0, log)
	if err != nil {
		return err
	}
	log.Info().
		Str("self_lid", selfLID).
		Str("peer_lid", peerLID).
		Str("ssrc", fmt.Sprintf("0x%08x", ssrc)).
		Msg("media session")
	e.c.diag.Emit("ssrc", map[string]any{
		"call_id": callID, "ssrc": ssrc, "self_lid": selfLID,
		"participant_id": rtp.FormatE2ESrtpParticipantID(selfLID),
	})

	enc := mlow.NewMlowEncoder(mlow.WithLogger(log))
	dec := mlow.NewMlowDecoder(mlow.WithLogger(log))
	txPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, ssrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	rxPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, ssrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	// The derived E2E-SRTP keys live inside MediaPipeline; record the derivation INPUTS
	// (callKey + participant-ID info strings) so a reference can re-derive and compare.
	e.c.diag.Emit("srtp", map[string]any{
		"event": "media_keys_input", "call_id": callID, "ssrc": ssrc,
		"self_participant_id": rtp.FormatE2ESrtpParticipantID(selfLID),
		"peer_participant_id": rtp.FormatE2ESrtpParticipantID(peerLID),
		"call_key_hex":        hex.EncodeToString(callKey),
	})
	e.c.diag.Emit("meta", map[string]any{
		"event": "media_start", "call_id": callID, "self_lid": selfLID,
		"peer_lid": peerLID, "ssrc": ssrc,
	})

	// relayRx counts packets received from the relay, so the silence watchdog can warn if
	// the relay never answers our allocate.
	var relayRx atomic.Uint64

	// Inbound calls are torn down by the caller within ~400ms if the relay bind never
	// comes alive; check at 400ms and 900ms and say so explicitly.
	go func() {
		for _, d := range []time.Duration{400 * time.Millisecond, 900 * time.Millisecond} {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			if relayRx.Load() == 0 {
				log.Warn().Dur("after", d).Msg("relay silent after allocate, no bytes back yet (allocate undelivered or rejected)")
			}
		}
	}()

	// Keepalive: re-send the Allocate AND a WhatsApp ping (0x0801) ~1 Hz. This matches the
	// working capture exactly — allocate+ping every second, NO STUN binding-requests at
	// all; the relay answers allocate-success + pong and bridges the peer's media.
	// Binding-requests instead flip the relay into ICE-consent mode and the bridge never
	// forms.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var tickCount uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			var tx [12]byte
			_, _ = rand.Read(tx[:])
			ping := stun.BuildWhatsappPing(tx, log)
			if _, err := ch.Send(allocate); err != nil {
				return
			}
			_, _ = ch.Send(ping[:])
			tickCount++
			e.c.diag.Emit("stun", map[string]any{
				"event": "keepalive", "tick": tickCount,
				"tx_id_hex": hex.EncodeToString(tx[:]), "ping_hex": hex.EncodeToString(ping[:]),
			})
		}
	}()

	// Send loop: frame-paced from connect, NOT gated on the Player. WhatsApp starts media
	// on relay connection and the relay learns our SSRC from our FIRST RTP — it won't
	// bridge the peer's media until it sees our stream. So we send silence frames until the
	// Player has real audio (nextFrame() == nil means send silence).
	frameInterval := time.Duration(FrameSamples) * time.Second / SampleRate
	go func() {
		silence := make([]float32, FrameSamples)
		ticker := time.NewTicker(frameInterval)
		defer ticker.Stop()
		var txCount uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			frame := silence
			if player, _ := callPlayerSink(call); player != nil {
				if f := player.nextFrame(); f != nil {
					frame = f
				}
			}
			payload, err := enc.Encode(frame)
			if err != nil {
				continue
			}
			packet, err := txPipe.ProtectAudio(payload)
			if err != nil {
				continue
			}
			e.c.diag.Emit("media_out", map[string]any{
				"frame": txCount, "frame_samples": len(frame), "pcm_rms": rmsFloat32(frame),
				"payload_len": len(payload), "payload_hex": hex.EncodeToString(payload),
				"packet_len": len(packet), "packet_hex": hex.EncodeToString(packet),
			})
			if _, err := ch.Send(packet); err != nil {
				return
			}
			if txCount++; txCount == 1 {
				log.Info().Int("bytes", len(packet)).Msg("first RTP sent to relay, outbound media flowing")
				e.c.diag.Emit("meta", map[string]any{"event": "first_rtp_sent", "call_id": callID, "bytes": len(packet)})
			}
		}
	}()

	// Receive: DataChannel → classify. RTP → unprotect → decode → sink. A non-RTP STUN
	// binding request gets a binding-success reply (ICE consent freshness, RFC 7675);
	// without it the relay drops the binding and the peer's call fails.
	// Video receive (meowcaller-native): a second WARP pipeline keyed on the video SSRC
	// (participant slot 2), demuxed off the relay by H.264 payload type 97. NALUs are
	// reassembled into Annex-B access units and emitted on the RTP marker bit, per WaCalls.
	//
	// NOT VALIDATED: no live video-RTP vector; assumes video shares the audio E2E keys and
	// WARP framing, and that the relay bridges the video SSRC.
	videoSelfSsrc, err := rtp.DeriveWasmParticipantSsrc(callID, rtp.FormatE2ESrtpParticipantID(selfLID), rtp.VideoSlotWord, log)
	if err != nil {
		return err
	}
	rxVideoPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, videoSelfSsrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	var videoDepack rtp.H264Depacketizer
	var videoAU []byte

	// Video send: a second WARP pipeline on our video SSRC, registered on the call so
	// Call.SendVideoFrame can push encoded H.264 to the relay. Cleared when the loop exits.
	txVideoPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, videoSelfSsrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	vsender := &videoSender{pipe: txVideoPipe, ch: ch, ssrc: videoSelfSsrc}
	e.mu.Lock()
	if m := e.calls[callID]; m != nil {
		m.videoTx = vsender
	}
	e.mu.Unlock()
	defer func() {
		vsender.mu.Lock()
		vsender.ch = nil
		vsender.mu.Unlock()
		e.mu.Lock()
		if m := e.calls[callID]; m != nil {
			m.videoTx = nil
		}
		e.mu.Unlock()
	}()

	buf := make([]byte, 1500)
	var rtpIn, rtpSeen, unprotectFail, vidIn uint64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := ch.Recv(buf)
		if err != nil {
			return fmt.Errorf("relay recv: %w", err)
		}
		relayRx.Add(1)
		pkt := buf[:n]
		isRTP := relay.ClassifyRelayPacket(pkt) == relay.RelayPacketRtp
		e.c.diag.Emit("relay", map[string]any{
			"event": "packet_in", "bytes": n, "is_rtp": isRTP,
			"packet_hex": hex.EncodeToString(pkt),
		})
		if !isRTP {
			mt, isStun := stun.StunMessageType(pkt)
			if isStun && mt == stun.MsgBindingRequest {
				if txid, ok := stun.StunTransactionID(pkt); ok && len(txid) == 12 {
					var tx [12]byte
					copy(tx[:], txid)
					resp := stun.EncodeStunRequest(stun.MsgBindingSuccess, tx, nil, rd.relayKeyASCII, true, log)
					if _, err := ch.Send(resp); err != nil {
						return fmt.Errorf("relay send binding-success: %w", err)
					}
					e.c.diag.Emit("stun", map[string]any{
						"event": "binding_request_answered",
						"tx_id_hex": hex.EncodeToString(tx[:]), "resp_hex": hex.EncodeToString(resp),
					})
				}
			}
			continue
		}
		if rtpSeen++; rtpSeen == 1 {
			log.Info().Int("bytes", n).Msg("first RTP-classified packet from relay, relay is bridging the peer's media")
		}
		// Demux: H.264 (PT 97) is the peer's video; route it to the video pipeline and
		// reassemble Annex-B access units, emitting each on the RTP marker bit. Anything else
		// is audio. Ported from WaCalls callmanager_video.handleVideoRelayData.
		// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/callmanager_video.go#L86-L126
		if vh, vok := rtp.ParseRtpHeader(pkt); vok && vh.PayloadType == rtp.RtpPayloadTypeH264 {
			_, vpayload, vunok := rxVideoPipe.UnprotectAudio(pkt)
			if !vunok {
				e.c.diag.Emit("video", map[string]any{"event": "unprotect_failed", "ssrc": vh.Ssrc, "seq": vh.SequenceNumber})
				continue
			}
			for _, nalu := range videoDepack.Depacketize(vpayload) {
				videoAU = append(videoAU, 0x00, 0x00, 0x00, 0x01)
				videoAU = append(videoAU, nalu...)
			}
			if vh.Marker && len(videoAU) > 0 {
				frame := videoAU
				videoAU = nil
				e.c.diag.Emit("video", map[string]any{"event": "frame", "ssrc": vh.Ssrc, "bytes": len(frame)})
				if sink := callVideoSink(call); sink != nil {
					_ = sink.WriteVideo(frame)
				}
			}
			if vidIn++; vidIn == 1 {
				log.Info().Uint32("ssrc", vh.Ssrc).Msg("first video RTP demuxed from relay (NOT VALIDATED)")
				e.c.diag.Emit("meta", map[string]any{"event": "first_video_rtp_in", "call_id": callID, "ssrc": vh.Ssrc})
			}
			continue
		}
		hdr, payload, ok := rxPipe.UnprotectAudio(pkt)
		if !ok {
			if unprotectFail++; unprotectFail == 1 {
				log.Warn().Int("bytes", n).Msg("RTP arrived but failed to unprotect, keying/SSRC mismatch on the recv path")
			}
			e.c.diag.Emit("srtp", map[string]any{"event": "unprotect_failed", "bytes": n})
			continue
		}
		e.c.diag.Emit("rtp", map[string]any{
			"event": "in", "ssrc": hdr.Ssrc, "seq": hdr.SequenceNumber,
			"ts": hdr.Timestamp, "pt": hdr.PayloadType, "marker": hdr.Marker,
		})
		e.c.diag.Emit("srtp", map[string]any{
			"event": "frame_unprotected", "ssrc": hdr.Ssrc, "seq": hdr.SequenceNumber,
			"payload_len": len(payload), "payload_hex": hex.EncodeToString(payload),
		})
		frame := dec.Decode(payload)
		e.c.diag.Emit("media_in", map[string]any{
			"seq": hdr.SequenceNumber, "samples": len(frame),
			"pcm_rms": rmsFloat32(frame), "payload_len": len(payload),
		})
		if _, sink := callPlayerSink(call); sink != nil {
			_ = sink.WriteFrame(frame)
		}
		if rtpIn++; rtpIn == 1 {
			log.Info().Msg("first RTP decoded from relay, inbound audio flowing")
			e.c.diag.Emit("meta", map[string]any{"event": "first_rtp_in", "call_id": callID})
			if call != nil {
				call.setPhase(CallPhaseActive)
				if fn := call.onReadyFn(); fn != nil {
					fn()
				}
			}
		}
	}
}

// callPlayerSink returns a Call's current Player and sink, tolerating a nil Call (an
// outbound call may never have had one attached).
func callPlayerSink(call *Call) (*Player, AudioSink) {
	if call == nil {
		return nil, nil
	}
	return call.playerAndSink()
}

// callVideoSink returns a Call's inbound-video sink, tolerating a nil Call.
func callVideoSink(call *Call) VideoSink {
	if call == nil {
		return nil
	}
	return call.videoSinkRef()
}

// videoRtpStepSamples advances the 90 kHz video RTP timestamp one frame at 15 fps.
const videoRtpStepSamples = 90000 / 15

// videoSender packetizes encoded H.264 access units (Annex-B) into PT-97 RTP, E2E-SRTP
// protects them with the video pipeline, and sends them to the relay. The send path is
// fed encoded H.264 (e.g. from the VideoBridge / WebCodecs), not raw frames.
//
// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/callmanager_video.go#L49-L84
//
// NOT VALIDATED: the video send media path is unproven.
type videoSender struct {
	mu      sync.Mutex
	pipe    *MediaPipeline
	ch      *relay.RelayMediaChannel
	ssrc    uint32
	seq     uint16
	ts      uint32
	started bool
}

// send fragments one Annex-B access unit into PT-97 RTP packets (marker on the last) and
// sends them to the relay.
func (vs *videoSender) send(au []byte) {
	if vs == nil || len(au) == 0 {
		return
	}
	nalus := rtp.SplitAnnexB(au)
	if len(nalus) == 0 {
		return
	}
	var payloads [][]byte
	for _, n := range nalus {
		payloads = append(payloads, rtp.PackageH264NALU(n)...)
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.ch == nil {
		return
	}
	if vs.started {
		vs.ts += videoRtpStepSamples
	}
	vs.started = true
	for i, p := range payloads {
		hdr := rtp.RtpHeader{
			PayloadType:    rtp.RtpPayloadTypeH264,
			SequenceNumber: vs.seq,
			Timestamp:      vs.ts,
			Ssrc:           vs.ssrc,
			Marker:         i == len(payloads)-1,
		}
		vs.seq++
		pkt, err := vs.pipe.ProtectRTP(&hdr, p)
		if err != nil {
			continue
		}
		if _, err := vs.ch.Send(pkt); err != nil {
			return
		}
	}
}

// rmsFloat32 returns the root-mean-square level of a PCM frame, a cheap loudness
// metric for the media diagnostic streams (avoids inlining raw float32 PCM).
func rmsFloat32(f []float32) float64 {
	if len(f) == 0 {
		return 0
	}
	var sum float64
	for _, s := range f {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(f)))
}
