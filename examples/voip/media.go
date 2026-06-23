package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/relay"
	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/stun"
	waBinary "go.mau.fi/whatsmeow/binary"
)

// ---- relay signaling parse (port of wacore/src/voip/relay_parse.rs essentials) ----

type relayAddress struct {
	ipv4 string
	port uint16
}

type relayEndpoint struct {
	relayID     uint32
	relayName   string
	tokenID     uint32
	authTokenID uint32
	isFNA       bool
	addresses   []relayAddress
}

type relayData struct {
	relayKeyASCII []byte   // raw <key> content — the STUN MESSAGE-INTEGRITY key
	relayTokens   [][]byte // indexed <token id=…>
	endpoints     []relayEndpoint
}

func nodeBytes(n *waBinary.Node) []byte {
	switch c := n.Content.(type) {
	case []byte:
		return c
	case string:
		return []byte(c)
	}
	return nil
}

func childByTag(n *waBinary.Node, tag string) *waBinary.Node {
	kids := n.GetChildren()
	for i := range kids {
		if kids[i].Tag == tag {
			return &kids[i]
		}
	}
	return nil
}

// findRelay recursively locates the <relay> node anywhere under n (it can sit under
// <offer> or a sibling <relaylatency>/<transport>).
func findRelay(n *waBinary.Node) *waBinary.Node {
	if n == nil {
		return nil
	}
	if n.Tag == "relay" {
		return n
	}
	kids := n.GetChildren()
	for i := range kids {
		if r := findRelay(&kids[i]); r != nil {
			return r
		}
	}
	return nil
}

func attrUint(n *waBinary.Node, key string) uint32 {
	v, _ := strconv.ParseUint(n.AttrGetter().String(key), 10, 32)
	return uint32(v)
}

const maxRelayTokens = 64

func parseIndexedTokens(node *waBinary.Node, tag string) [][]byte {
	var tokens [][]byte
	kids := node.GetChildren()
	for i := range kids {
		c := &kids[i]
		if c.Tag != tag {
			continue
		}
		b := nodeBytes(c)
		if b == nil {
			continue
		}
		id := len(tokens)
		if s := c.AttrGetter().String("id"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				id = n
			}
		}
		if id >= maxRelayTokens {
			continue
		}
		for len(tokens) <= id {
			tokens = append(tokens, nil)
		}
		tokens[id] = b
	}
	return tokens
}

// parseRelayData ports parse_relay_data: <key>, indexed <token>, and te2 endpoints.
func parseRelayData(node *waBinary.Node) *relayData {
	rd := &relayData{}
	if key := childByTag(node, "key"); key != nil {
		rd.relayKeyASCII = nodeBytes(key)
	}
	rd.relayTokens = parseIndexedTokens(node, "token")

	kids := node.GetChildren()
	for i := range kids {
		te2 := &kids[i]
		if te2.Tag != "te2" {
			continue
		}
		ab := nodeBytes(te2)
		if len(ab) != 6 { // IPv4:port only (IPv6 endpoints skipped for this demo)
			continue
		}
		ep := relayEndpoint{
			relayID:     attrUint(te2, "relay_id"),
			relayName:   te2.AttrGetter().String("relay_name"),
			tokenID:     attrUint(te2, "token_id"),
			authTokenID: attrUint(te2, "auth_token_id"),
			isFNA:       te2.AttrGetter().String("is_fna") == "1",
			addresses: []relayAddress{{
				ipv4: fmt.Sprintf("%d.%d.%d.%d", ab[0], ab[1], ab[2], ab[3]),
				port: binary.BigEndian.Uint16(ab[4:6]),
			}},
		}
		rd.endpoints = append(rd.endpoints, ep)
	}
	return rd
}

// getMediaRelayEndpoint mirrors the reference: prefer an outbound (non-FNA,
// auth_token_id≠0) endpoint, else any non-FNA, else the first.
func getMediaRelayEndpoint(rd *relayData) *relayEndpoint {
	for i := range rd.endpoints {
		if e := &rd.endpoints[i]; !e.isFNA && e.authTokenID != 0 {
			return e
		}
	}
	for i := range rd.endpoints {
		if e := &rd.endpoints[i]; !e.isFNA {
			return e
		}
	}
	if len(rd.endpoints) > 0 {
		return &rd.endpoints[0]
	}
	return nil
}

// ---- relay connect + media loop (port of voip.rs connect_and_allocate + run_media) ----

func connectAndAllocate(rd *relayData) (*relay.RelayMediaChannel, []byte, error) {
	ep := getMediaRelayEndpoint(rd)
	if ep == nil || len(ep.addresses) == 0 {
		return nil, nil, fmt.Errorf("relay has no usable endpoint")
	}
	addr := &net.UDPAddr{IP: net.ParseIP(ep.addresses[0].ipv4), Port: int(ep.addresses[0].port)}
	log.Printf("🔌 connecting media transport to relay %s %s…", ep.relayName, addr)

	type result struct {
		ch  *relay.RelayMediaChannel
		err error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := relay.ConnectRelayMedia(addr)
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
	}
	log.Printf("✅ relay DataChannel OPEN to %s", ep.relayName)

	if int(ep.tokenID) >= len(rd.relayTokens) || rd.relayTokens[ep.tokenID] == nil {
		return nil, nil, fmt.Errorf("no relay token #%d", ep.tokenID)
	}
	if len(rd.relayKeyASCII) == 0 {
		return nil, nil, fmt.Errorf("relay has no <key>")
	}
	endpointXor, ok := stun.EncodeXorRelayEndpoint(ep.addresses[0].ipv4, ep.addresses[0].port)
	if !ok {
		return nil, nil, fmt.Errorf("bad endpoint XOR")
	}
	var tx [12]byte
	_, _ = rand.Read(tx[:])
	allocate := stun.BuildWasmStunAllocateRequest(tx, rd.relayTokens[ep.tokenID], endpointXor, rd.relayKeyASCII)
	if _, err := ch.Send(allocate); err != nil {
		return nil, nil, fmt.Errorf("allocate send: %w", err)
	}
	log.Printf("📡 sent STUN Allocate (%d bytes)", len(allocate))
	return ch, allocate, nil
}

// runMedia pipes mic↔speaker over the relay DataChannel: mic → MLow → E2E-SRTP
// protect → DataChannel, and DataChannel → unprotect → MLow → speaker, with a 1 Hz
// allocate+ping keepalive (the relay drops us without consent-freshness traffic).
func runMedia(ctx context.Context, callID string, callKey []byte, selfLID, peerLID string, rd *relayData) error {
	ch, allocate, err := connectAndAllocate(rd)
	if err != nil {
		return err
	}
	defer ch.Close()

	ssrc, err := rtp.DeriveWasmParticipantSsrc(callID, rtp.FormatE2ESrtpParticipantID(selfLID), 0)
	if err != nil {
		return err
	}
	log.Printf("media: self=%s peer=%s ssrc=%#08x", selfLID, peerLID, ssrc)

	a, err := newAudio()
	if err != nil {
		return err
	}
	defer a.close()
	mic, stopMic, err := a.openMic()
	if err != nil {
		return err
	}
	defer stopMic()
	speaker, stopSpeaker, err := a.openSpeaker()
	if err != nil {
		return err
	}
	defer stopSpeaker()

	enc := mlow.NewMlowEncoder()
	dec := mlow.NewMlowDecoder()
	txPipe, err := meowcaller.NewMediaPipeline(callKey, selfLID, peerLID, ssrc, frameSamps)
	if err != nil {
		return err
	}
	rxPipe, err := meowcaller.NewMediaPipeline(callKey, selfLID, peerLID, ssrc, frameSamps)
	if err != nil {
		return err
	}

	// Keepalive: re-send the allocate + a WhatsApp ping ~1 Hz.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			var tx [12]byte
			_, _ = rand.Read(tx[:])
			ping := stun.BuildWhatsappPing(tx)
			if _, err := ch.Send(allocate); err != nil {
				return
			}
			_, _ = ch.Send(ping[:])
		}
	}()

	// Send: mic → encode → protect → DataChannel.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case pcm, ok := <-mic:
				if !ok {
					return
				}
				payload, err := enc.Encode(pcmToFloat(pcm))
				if err != nil {
					continue
				}
				packet, err := txPipe.ProtectAudio(payload)
				if err != nil {
					continue
				}
				if _, err := ch.Send(packet); err != nil {
					return
				}
			}
		}
	}()

	// Receive: DataChannel → classify. RTP → unprotect → decode → speaker. A non-RTP
	// STUN binding request gets a binding-success reply (ICE consent freshness, RFC
	// 7675); without it the relay drops the binding and the peer's call fails.
	buf := make([]byte, 1500)
	var rtpIn, consent uint64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := ch.Recv(buf)
		if err != nil {
			return fmt.Errorf("relay recv: %w", err)
		}
		pkt := buf[:n]
		if relay.ClassifyRelayPacket(pkt) != relay.RelayPacketRtp {
			if mt, ok := stun.StunMessageType(pkt); ok && mt == stun.MsgBindingRequest {
				if txid, ok := stun.StunTransactionID(pkt); ok && len(txid) == 12 {
					var tx [12]byte
					copy(tx[:], txid)
					resp := stun.EncodeStunRequest(stun.MsgBindingSuccess, tx, nil, rd.relayKeyASCII, true)
					if _, err := ch.Send(resp); err != nil {
						return fmt.Errorf("relay send binding-success: %w", err)
					}
					if consent++; consent == 1 {
						log.Printf("🤝 relay consent: answering STUN binding requests (keeps the media path alive)")
					}
				}
			}
			continue
		}
		_, payload, ok := rxPipe.UnprotectAudio(pkt)
		if !ok {
			continue
		}
		speaker <- floatToPCM(dec.Decode(payload))
		if rtpIn++; rtpIn == 1 {
			log.Printf("🔊 first RTP from relay — inbound media flowing")
		}
	}
}
