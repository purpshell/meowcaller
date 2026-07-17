package meowcaller

import (
	"bytes"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
)

func peerJID() types.JID { return types.JID{User: "222222222222222", Server: types.HiddenUserServer} }
func creatorJID() types.JID {
	return types.JID{User: "111111111111111", Server: types.HiddenUserServer, Device: 1}
}

func iota32() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// TestOutgoingLifecycle pins the outgoing transition table.
func TestOutgoingLifecycle(t *testing.T) {
	s := NewOutgoingSession("CID", peerJID(), creatorJID())
	if s.Phase() != CallPhaseIdle {
		t.Fatalf("phase = %d, want Idle", s.Phase())
	}
	for _, ph := range []CallPhase{CallPhaseCalling, CallPhaseRinging, CallPhaseConnecting, CallPhaseActive} {
		if !s.TransitionTo(ph) {
			t.Fatalf("transition to %d rejected", ph)
		}
	}
	if !s.IsActive() {
		t.Error("not active after reaching Active")
	}
	if s.TransitionTo(CallPhaseCalling) {
		t.Error("illegal Active→Calling accepted")
	}
	if !s.TransitionTo(CallPhaseEnded) {
		t.Error("→Ended rejected")
	}
	if !s.IsEnded() {
		t.Error("not ended")
	}
	if s.TransitionTo(CallPhaseActive) {
		t.Error("Ended is not a sink")
	}
}

// TestIncomingStartsRinging pins the incoming start phase and the no-Calling rule.
func TestIncomingStartsRinging(t *testing.T) {
	s := NewIncomingSession("CID", peerJID(), creatorJID())
	if s.Phase() != CallPhaseRinging {
		t.Fatalf("phase = %d, want Ringing", s.Phase())
	}
	if s.TransitionTo(CallPhaseCalling) {
		t.Error("incoming must not go to Calling")
	}
	if !s.TransitionTo(CallPhaseConnecting) || !s.TransitionTo(CallPhaseActive) {
		t.Error("Ringing→Connecting→Active rejected")
	}
}

// TestMediaPipelineRoundTrips checks the protect→unprotect composition loopback.
func TestMediaPipelineRoundTrips(t *testing.T) {
	callKey := iota32()
	lid := "222222222222222:0@lid"
	tx, err := NewMediaPipeline(callKey, lid, lid, 0x12345678, 960)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	rx, err := NewMediaPipeline(callKey, lid, lid, 0x12345678, 960)
	if err != nil {
		t.Fatalf("rx: %v", err)
	}
	opus := []byte{0x48, 0x11, 0x22, 0x33, 0x44, 0x55}
	packet, err := tx.ProtectAudio(opus)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	header, payload, ok := rx.UnprotectAudio(packet)
	if !ok {
		t.Fatal("unprotect failed")
	}
	if header.SequenceNumber != 1 || header.Ssrc != 0x12345678 || header.PayloadType != 120 {
		t.Errorf("header = seq %d ssrc %#x pt %d", header.SequenceNumber, header.Ssrc, header.PayloadType)
	}
	if !bytes.Equal(payload, opus) {
		t.Errorf("payload = %x, want %x", payload, opus)
	}
}

func TestMediaPipelineRekeysReceivePathForAnsweringDevice(t *testing.T) {
	callKey := iota32()
	self := "111111111111111:0@lid"
	initialPeer := "222222222222222:0@lid"
	answeringPeer := "222222222222222:7@lid"
	const ssrc = 0x55667788
	receiver, err := NewMediaPipeline(callKey, self, initialPeer, ssrc, FrameSamples)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	answeringDevice, err := NewMediaPipeline(callKey, answeringPeer, self, ssrc, FrameSamples)
	if err != nil {
		t.Fatalf("answering device: %v", err)
	}
	payload := []byte{1, 2, 3, 4, 5}
	packet, err := answeringDevice.ProtectAudio(payload)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}

	if err = receiver.RekeyRecv(callKey, answeringPeer); err != nil {
		t.Fatalf("RekeyRecv: %v", err)
	}
	_, got, ok := receiver.UnprotectAudio(packet)
	if !ok {
		t.Fatal("rekeyed receiver rejected answering-device packet")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("rekeyed payload = %x, want %x", got, payload)
	}
}

// TestProtectUsesSelfLidForSend pins the send keystream to the self LID.
func TestProtectUsesSelfLidForSend(t *testing.T) {
	callKey := iota32()
	selfLid, peerLid := "111111111111111:0@lid", "222222222222222:0@lid"
	ssrc := uint32(0x12345678)
	pipe, err := NewMediaPipeline(callKey, selfLid, peerLid, ssrc, 960)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	opus := []byte{0x10, 0x21, 0x32, 0x43}
	packet, err := pipe.ProtectAudio(opus)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	withoutTag := packet[:len(packet)-srtp.WarpMITagLen]
	headerLen, ok := rtp.RtpHeaderByteLength(withoutTag)
	if !ok {
		t.Fatal("header length")
	}
	body := withoutTag[headerLen:]

	selfKeys, _ := srtp.DeriveE2eKeys(callKey, selfLid)
	expect, _ := srtp.CryptPayload(&selfKeys, ssrc, 1, 0, opus)
	if !bytes.Equal(body, expect) {
		t.Error("send must encrypt under the self LID")
	}
	peerKeys, _ := srtp.DeriveE2eKeys(callKey, peerLid)
	inverted, _ := srtp.CryptPayload(&peerKeys, ssrc, 1, 0, opus)
	if bytes.Equal(body, inverted) {
		t.Error("send must NOT encrypt under the peer LID")
	}
}

// TestRecvUsesPeerLidForRecv pins the recv keystream to the peer LID.
func TestRecvUsesPeerLidForRecv(t *testing.T) {
	callKey := iota32()
	selfLid, peerLid := "111111111111111:0@lid", "222222222222222:0@lid"
	ssrc := uint32(0x12345678)
	us, _ := NewMediaPipeline(callKey, selfLid, peerLid, ssrc, 960)
	peerTx, _ := NewMediaPipeline(callKey, peerLid, selfLid, ssrc, 960)

	opus := []byte{0x48, 0x01, 0x02, 0x03, 0x04, 0x05}
	fromPeer, err := peerTx.ProtectAudio(opus)
	if err != nil {
		t.Fatalf("peer protect: %v", err)
	}
	_, recovered, ok := us.UnprotectAudio(fromPeer)
	if !ok || !bytes.Equal(recovered, opus) {
		t.Error("recv must use the peer-LID keystream")
	}

	selfKeyedTx, _ := NewMediaPipeline(callKey, selfLid, peerLid, ssrc, 960)
	wrong, _ := selfKeyedTx.ProtectAudio(opus)
	us2, _ := NewMediaPipeline(callKey, selfLid, peerLid, ssrc, 960)
	_, mis, _ := us2.UnprotectAudio(wrong)
	if bytes.Equal(mis, opus) {
		t.Error("recv must not recover a self-LID-keyed packet")
	}
}

func TestMediaPipelineTracksSenderStats(t *testing.T) {
	pipe, err := NewMediaPipeline(iota32(), "111111111111111:0@lid", "222222222222222:0@lid", 0x12345678, 960)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	header := rtp.RtpHeader{
		Marker:         true,
		PayloadType:    rtp.RtpPayloadTypeH264,
		SequenceNumber: 0,
		Timestamp:      90000,
		Ssrc:           0x12345678,
		VideoExtension: &rtp.VideoRtpExtension{MediaFrameInfo: rtp.VideoMediaFrameInfoIDR},
	}
	if _, err := pipe.ProtectRTP(&header, []byte{0x65, 1, 2, 3}); err != nil {
		t.Fatalf("protect: %v", err)
	}
	stats := pipe.SenderStats()
	if stats.PacketsSent != 1 || stats.OctetsSent != 4 || stats.RtpTimestamp != 90000 {
		t.Errorf("stats = %+v, want packets=1 octets=4 timestamp=90000", stats)
	}
}
