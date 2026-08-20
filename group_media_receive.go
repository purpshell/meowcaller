package meowcaller

import (
	"bytes"
	"fmt"
	"slices"
	"sync"

	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
)

type participantAudioDecoder interface {
	Decode([]byte) []float32
}

type participantAudioDecoderFactory func() (participantAudioDecoder, error)

type decodedParticipantAudio struct {
	ParticipantID string
	UserJID       types.JID
	DeviceJID     types.JID
	PID           uint32
	HasPID        bool
	SSRC          uint32
	Timestamp     uint32
	PCM           []float32
}

type unprotectedParticipantMedia struct {
	ParticipantID string
	UserJID       types.JID
	DeviceJID     types.JID
	PID           uint32
	HasPID        bool
	Header        rtp.RtpHeader
	Payload       []byte
	receiver      *participantAudioReceiver
}

type participantAudioReceiver struct {
	mu            sync.Mutex
	userJID       types.JID
	deviceJID     types.JID
	participantID string
	pid           uint32
	hasPID        bool
	ssrc          uint32
	streamSSRCs   [9]uint32
	videoSSRC     uint32
	appDataSSRC   uint32
	pipe          *MediaPipeline
	videoPipe     *MediaPipeline
	appDataPipe   *MediaPipeline
	srtcp         *mediaSrtcpReceiver
	decoder       participantAudioDecoder
}

type installedGroupRawEpoch struct {
	transactionID uint32
	rawKey        []byte
}

type participantReceiverMetadata struct {
	receiver  *participantAudioReceiver
	userJID   types.JID
	deviceJID types.JID
	pid       uint32
}

type preparedGroupRawEpoch struct {
	sendKeys        srtp.E2eSrtpKeys
	sendRTCPKeys    srtp.E2eSrtpKeys
	receiveKeys     map[*participantAudioReceiver]srtp.E2eSrtpKeys
	receiveRTCPKeys map[*participantAudioReceiver]srtp.E2eSrtpKeys
}

type preparedParticipantReceiveUpdate struct {
	transactionID               uint32
	byDeviceID                  map[string]*participantAudioReceiver
	byPID                       map[uint32]*participantAudioReceiver
	bySSRC                      map[uint32]*participantAudioReceiver
	byVideoSSRC                 map[uint32]*participantAudioReceiver
	byAppDataSSRC               map[uint32]*participantAudioReceiver
	bySRTCPSSRC                 map[uint32]*participantAudioReceiver
	metadata                    []participantReceiverMetadata
	newReceivers                []*participantAudioReceiver
	consumedPendingTransactions []uint32
	epoch                       *preparedGroupRawEpoch
	epochTransactionID          uint32
	epochRawKey                 []byte
	replaceInstalledEpoch       bool
}

type participantReceiveRegistry struct {
	mu               sync.RWMutex
	callID           string
	callKey          []byte
	selfLID          string
	selfID           string
	fallbackID       string
	transactionID    uint32
	rosterGeneration uint64
	hasGroupUpdate   bool
	decoderFactory   participantAudioDecoderFactory
	byDeviceID       map[string]*participantAudioReceiver
	byPID            map[uint32]*participantAudioReceiver
	bySSRC           map[uint32]*participantAudioReceiver
	byVideoSSRC      map[uint32]*participantAudioReceiver
	byAppDataSSRC    map[uint32]*participantAudioReceiver
	bySRTCPSSRC      map[uint32]*participantAudioReceiver
	sendPipes        []*MediaPipeline
	srtcpSenders     []*mediaSrtcpSender
	pendingEpochs    map[uint32][]byte
	installedEpoch   installedGroupRawEpoch
	hasEpoch         bool
	log              zerolog.Logger
}

func newParticipantReceiveRegistry(
	callID string,
	callKey []byte,
	selfLID string,
	peerLID string,
	decoderFactory func() participantAudioDecoder,
	opts ...Option,
) (*participantReceiveRegistry, error) {
	var factory participantAudioDecoderFactory
	if decoderFactory != nil {
		factory = func() (participantAudioDecoder, error) {
			return decoderFactory(), nil
		}
	}
	return newParticipantReceiveRegistryWithDecoderFactory(callID, callKey, selfLID, peerLID, factory, opts...)
}

func newParticipantReceiveRegistryWithDecoderFactory(
	callID string,
	callKey []byte,
	selfLID string,
	peerLID string,
	decoderFactory participantAudioDecoderFactory,
	opts ...Option,
) (*participantReceiveRegistry, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L24-L44
	if decoderFactory == nil {
		decoderFactory = func() (participantAudioDecoder, error) {
			return mlow.NewMlowDecoder(), nil
		}
	}
	r := &participantReceiveRegistry{
		callID:         callID,
		callKey:        append([]byte(nil), callKey...),
		selfLID:        selfLID,
		selfID:         rtp.FormatE2ESrtpParticipantID(selfLID),
		decoderFactory: decoderFactory,
		byDeviceID:     make(map[string]*participantAudioReceiver),
		byPID:          make(map[uint32]*participantAudioReceiver),
		bySSRC:         make(map[uint32]*participantAudioReceiver),
		byVideoSSRC:    make(map[uint32]*participantAudioReceiver),
		byAppDataSSRC:  make(map[uint32]*participantAudioReceiver),
		bySRTCPSSRC:    make(map[uint32]*participantAudioReceiver),
		pendingEpochs:  make(map[uint32][]byte),
		log:            resolveConfig(opts).log,
	}
	peerJID, err := types.ParseJID(peerLID)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: parse initial media peer: %w", err)
	}
	receiver, err := r.newReceiver(peerJID.ToNonAD(), peerJID, 0, false)
	if err != nil {
		return nil, err
	}
	r.fallbackID = receiver.participantID
	r.byDeviceID[receiver.participantID] = receiver
	r.bySSRC[receiver.ssrc] = receiver
	r.byVideoSSRC[receiver.videoSSRC] = receiver
	r.byAppDataSSRC[receiver.appDataSSRC] = receiver
	for _, streamSSRC := range receiver.streamSSRCs {
		r.bySRTCPSSRC[streamSSRC] = receiver
	}
	return r, nil
}

func (r *participantReceiveRegistry) newReceiver(userJID, deviceJID types.JID, pid uint32, hasPID bool) (*participantAudioReceiver, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L37-L44
	participantID := rtp.FormatE2ESrtpParticipantID(deviceJID.String())
	streamSSRCs, err := rtp.DeriveWasmRelayStreamSsrcs(r.callID, participantID, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant stream SSRCs: %w", err)
	}
	ssrc := streamSSRCs[0]
	videoSSRC, err := rtp.DeriveWasmParticipantSsrc(r.callID, participantID, rtp.VideoSlotWord, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant video SSRC: %w", err)
	}
	appDataSSRC, err := rtp.DeriveWasmParticipantSsrc(r.callID, participantID, rtp.AppDataSlotWord, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant app-data SSRC: %w", err)
	}
	pipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), ssrc, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant receive pipeline: %w", err)
	}
	videoPipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), videoSSRC, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant video receive pipeline: %w", err)
	}
	appDataPipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), appDataSSRC, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant app-data receive pipeline: %w", err)
	}
	srtcpReceiver, err := newMediaSrtcpReceiver(r.callKey, deviceJID.String())
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant SRTCP receiver: %w", err)
	}
	decoder, err := r.decoderFactory()
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant audio decoder: %w", err)
	}
	if decoder == nil {
		return nil, fmt.Errorf("meowcaller: participant audio decoder factory returned nil")
	}
	return &participantAudioReceiver{
		userJID: userJID, deviceJID: deviceJID, participantID: participantID,
		pid: pid, hasPID: hasPID, ssrc: ssrc, streamSSRCs: streamSSRCs,
		videoSSRC: videoSSRC, appDataSSRC: appDataSSRC,
		pipe: pipe, videoPipe: videoPipe, appDataPipe: appDataPipe,
		srtcp: srtcpReceiver, decoder: decoder,
	}, nil
}

func (r *participantReceiveRegistry) ApplyGroupUpdateTransaction(
	update groupCallUpdate,
	apply func(commit func()) error,
) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L100-L141
	if apply == nil {
		return fmt.Errorf("meowcaller: group update apply callback is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasGroupUpdate && update.TransactionID <= r.transactionID {
		return nil
	}
	prepared, err := r.prepareGroupUpdateLocked(update)
	if err != nil {
		return err
	}
	committed := false
	commitCalls := 0
	defer func() {
		prepared.clear(!committed)
	}()
	commit := func() {
		commitCalls++
		if committed {
			return
		}
		r.commitGroupUpdateLocked(prepared)
		committed = true
	}
	if err = apply(commit); err != nil {
		if committed {
			r.log.Error().
				Err(err).
				Str("call_id", r.callID).
				Uint32("transaction_id", update.TransactionID).
				Msg("group update apply failed after commit")
			return nil
		}
		return err
	}
	if commitCalls > 1 {
		r.log.Error().
			Str("call_id", r.callID).
			Uint32("transaction_id", update.TransactionID).
			Int("commit_calls", commitCalls).
			Msg("group update apply invoked commit more than once")
	}
	if !committed {
		return fmt.Errorf("meowcaller: group update apply completed without commit")
	}
	return nil
}

func (r *participantReceiveRegistry) ApplyGroupUpdate(update groupCallUpdate) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L83-L100
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L139-L141
	return r.ApplyGroupUpdateTransaction(update, func(commit func()) error {
		commit()
		return nil
	})
}

func (r *participantReceiveRegistry) prepareGroupUpdateLocked(
	update groupCallUpdate,
) (*preparedParticipantReceiveUpdate, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L110-L120
	prepared := &preparedParticipantReceiveUpdate{
		transactionID: update.TransactionID,
		byDeviceID:    make(map[string]*participantAudioReceiver),
		byPID:         make(map[uint32]*participantAudioReceiver),
		bySSRC:        make(map[uint32]*participantAudioReceiver),
		byVideoSSRC:   make(map[uint32]*participantAudioReceiver),
		byAppDataSSRC: make(map[uint32]*participantAudioReceiver),
		bySRTCPSSRC:   make(map[uint32]*participantAudioReceiver),
	}
	hasConnectedRemotePID := false
	for _, participant := range update.Participants {
		if participant.State != "connected" {
			continue
		}
		for _, device := range participant.Devices {
			if !device.HasPID {
				continue
			}
			// Source of truth: https://github.com/purpshell/meowcaller/blob/e34f414e423e6e45863d19dcfdd7e658f5f1314a/datasheets/group-media-receive.md#L24-L42
			participantID := rtp.FormatE2ESrtpParticipantID(device.JID.String())
			if participantID == r.selfID {
				continue
			}
			hasConnectedRemotePID = true
			if _, exists := prepared.byDeviceID[participantID]; exists {
				prepared.clear(true)
				return nil, fmt.Errorf("meowcaller: duplicate group participant device %s", participantID)
			}
			receiver := r.byDeviceID[participantID]
			if receiver == nil {
				var err error
				receiver, err = r.newReceiver(participant.JID, device.JID, device.PID, true)
				if err != nil {
					prepared.clear(true)
					return nil, err
				}
				prepared.newReceivers = append(prepared.newReceivers, receiver)
			}
			if _, exists := prepared.byPID[device.PID]; exists {
				prepared.clear(true)
				return nil, fmt.Errorf("meowcaller: duplicate group participant PID %d", device.PID)
			}
			if _, exists := prepared.bySSRC[receiver.ssrc]; exists {
				prepared.clear(true)
				return nil, fmt.Errorf("meowcaller: duplicate group participant SSRC %d", receiver.ssrc)
			}
			if _, exists := prepared.byVideoSSRC[receiver.videoSSRC]; exists {
				prepared.clear(true)
				return nil, fmt.Errorf("meowcaller: duplicate group participant video SSRC %d", receiver.videoSSRC)
			}
			if _, exists := prepared.byAppDataSSRC[receiver.appDataSSRC]; exists {
				prepared.clear(true)
				return nil, fmt.Errorf("meowcaller: duplicate group participant app-data SSRC %d", receiver.appDataSSRC)
			}
			for _, streamSSRC := range receiver.streamSSRCs {
				if _, exists := prepared.bySRTCPSSRC[streamSSRC]; exists {
					prepared.clear(true)
					return nil, fmt.Errorf("meowcaller: duplicate group participant stream SSRC %d", streamSSRC)
				}
				prepared.bySRTCPSSRC[streamSSRC] = receiver
			}
			prepared.byDeviceID[participantID] = receiver
			prepared.byPID[device.PID] = receiver
			prepared.bySSRC[receiver.ssrc] = receiver
			prepared.byVideoSSRC[receiver.videoSSRC] = receiver
			prepared.byAppDataSSRC[receiver.appDataSSRC] = receiver
			prepared.metadata = append(prepared.metadata, participantReceiverMetadata{
				receiver: receiver, userJID: participant.JID,
				deviceJID: device.JID, pid: device.PID,
			})
		}
	}
	if !hasConnectedRemotePID {
		fallback := r.byDeviceID[r.fallbackID]
		if fallback != nil {
			prepared.byDeviceID[r.fallbackID] = fallback
			prepared.bySSRC[fallback.ssrc] = fallback
			prepared.byVideoSSRC[fallback.videoSSRC] = fallback
			prepared.byAppDataSSRC[fallback.appDataSSRC] = fallback
			for _, streamSSRC := range fallback.streamSSRCs {
				prepared.bySRTCPSSRC[streamSSRC] = fallback
			}
		}
	}

	var newestPending installedGroupRawEpoch
	defer func() {
		clear(newestPending.rawKey)
	}()
	hasNewestPending := false
	for transactionID, rawKey := range r.pendingEpochs {
		if transactionID > update.TransactionID {
			continue
		}
		prepared.consumedPendingTransactions = append(prepared.consumedPendingTransactions, transactionID)
		if !hasNewestPending || transactionID > newestPending.transactionID {
			clear(newestPending.rawKey)
			newestPending = installedGroupRawEpoch{
				transactionID: transactionID,
				rawKey:        bytes.Clone(rawKey),
			}
			hasNewestPending = true
		}
	}
	selectedEpoch := r.installedEpoch
	hasSelectedEpoch := r.hasEpoch
	if hasNewestPending {
		switch {
		case !hasSelectedEpoch || newestPending.transactionID > selectedEpoch.transactionID:
			selectedEpoch = newestPending
			hasSelectedEpoch = true
			prepared.replaceInstalledEpoch = true
		case newestPending.transactionID == selectedEpoch.transactionID &&
			!bytes.Equal(newestPending.rawKey, selectedEpoch.rawKey):
			prepared.clear(true)
			return nil, fmt.Errorf(
				"meowcaller: conflicting group raw epoch for transaction %d",
				newestPending.transactionID,
			)
		}
	}
	if hasSelectedEpoch {
		epoch, err := r.prepareGroupRawEpochLocked(selectedEpoch.rawKey, prepared.byDeviceID)
		if err != nil {
			prepared.clear(true)
			return nil, err
		}
		prepared.epoch = epoch
		prepared.epochTransactionID = selectedEpoch.transactionID
		prepared.epochRawKey = bytes.Clone(selectedEpoch.rawKey)
	}
	return prepared, nil
}

func (r *participantReceiveRegistry) commitGroupUpdateLocked(prepared *preparedParticipantReceiveUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L122-L137
	for participantID, receiver := range r.byDeviceID {
		if prepared.byDeviceID[participantID] != receiver {
			clearParticipantReceiverKeys(receiver)
		}
	}
	for _, next := range prepared.metadata {
		next.receiver.mu.Lock()
		next.receiver.userJID = next.userJID
		next.receiver.deviceJID = next.deviceJID
		next.receiver.pid = next.pid
		next.receiver.hasPID = true
		next.receiver.mu.Unlock()
	}
	r.byDeviceID = prepared.byDeviceID
	r.byPID = prepared.byPID
	r.bySSRC = prepared.bySSRC
	r.byVideoSSRC = prepared.byVideoSSRC
	r.byAppDataSSRC = prepared.byAppDataSSRC
	r.bySRTCPSSRC = prepared.bySRTCPSSRC
	r.transactionID = prepared.transactionID
	r.rosterGeneration++
	r.hasGroupUpdate = true
	if prepared.epoch != nil {
		r.installPreparedGroupRawEpochLocked(prepared.epoch)
		if prepared.replaceInstalledEpoch {
			clear(r.installedEpoch.rawKey)
			r.installedEpoch = installedGroupRawEpoch{
				transactionID: prepared.epochTransactionID,
				rawKey:        bytes.Clone(prepared.epochRawKey),
			}
			r.hasEpoch = true
		}
	}
	for _, transactionID := range prepared.consumedPendingTransactions {
		clear(r.pendingEpochs[transactionID])
		delete(r.pendingEpochs, transactionID)
	}
	r.log.Info().
		Str("call_id", r.callID).
		Uint32("transaction_id", prepared.transactionID).
		Int("remote_participants", len(prepared.byPID)).
		Msg("applied group media receive roster")
}

func (p *preparedParticipantReceiveUpdate) clear(rollback bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L117-L128
	if p == nil {
		return
	}
	if p.epoch != nil {
		p.epoch.clear()
	}
	clear(p.epochRawKey)
	if rollback {
		for _, receiver := range p.newReceivers {
			clearParticipantReceiverKeys(receiver)
		}
	}
}

func clearParticipantReceiverKeys(receiver *participantAudioReceiver) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L117-L120
	if receiver == nil {
		return
	}
	receiver.pipe.installSendKeys(srtp.E2eSrtpKeys{})
	receiver.pipe.installRecvKeys(srtp.E2eSrtpKeys{})
	receiver.videoPipe.installSendKeys(srtp.E2eSrtpKeys{})
	receiver.videoPipe.installRecvKeys(srtp.E2eSrtpKeys{})
	receiver.appDataPipe.installSendKeys(srtp.E2eSrtpKeys{})
	receiver.appDataPipe.installRecvKeys(srtp.E2eSrtpKeys{})
	receiver.srtcp.installKeys(srtp.E2eSrtpKeys{})
	if closer, ok := receiver.decoder.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	receiver.decoder = nil
}

func (r *participantReceiveRegistry) clear() {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L117-L128
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.callKey)
	clear(r.installedEpoch.rawKey)
	for transactionID, rawKey := range r.pendingEpochs {
		clear(rawKey)
		delete(r.pendingEpochs, transactionID)
	}
	for _, receiver := range r.byDeviceID {
		clearParticipantReceiverKeys(receiver)
	}
	for _, sendPipe := range r.sendPipes {
		sendPipe.installSendKeys(srtp.E2eSrtpKeys{})
		sendPipe.installRecvKeys(srtp.E2eSrtpKeys{})
	}
	for _, sender := range r.srtcpSenders {
		sender.installKeys(srtp.E2eSrtpKeys{})
	}
	r.callKey = nil
	r.installedEpoch = installedGroupRawEpoch{}
	r.hasEpoch = false
	r.byDeviceID = nil
	r.byPID = nil
	r.bySSRC = nil
	r.byVideoSSRC = nil
	r.byAppDataSSRC = nil
	r.bySRTCPSSRC = nil
	r.sendPipes = nil
	r.srtcpSenders = nil
}

// HasCommittedGroupUpdate reports whether one authoritative group roster has
// crossed the media transaction commit boundary.
func (r *participantReceiveRegistry) HasCommittedGroupUpdate() bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L117-L140
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hasGroupUpdate
}

func (r *participantReceiveRegistry) attachSendPipeline(sendPipe *MediaPipeline) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L78-L86
	if sendPipe == nil {
		return fmt.Errorf("meowcaller: nil media send pipeline")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasEpoch {
		keys, err := srtp.DeriveE2eKeysFromRaw(r.installedEpoch.rawKey, r.selfID, r.log)
		if err != nil {
			return fmt.Errorf("meowcaller: derive late group send epoch: %w", err)
		}
		sendPipe.installSendKeys(keys)
	}
	r.sendPipes = append(r.sendPipes, sendPipe)
	return nil
}

func (r *participantReceiveRegistry) attachSRTCPSender(sender *mediaSrtcpSender) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L78-L120
	if sender == nil {
		return fmt.Errorf("meowcaller: nil SRTCP sender")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasEpoch {
		keys, err := srtp.DeriveE2eSRTCPKeysFromRaw(r.installedEpoch.rawKey, r.selfID, r.log)
		if err != nil {
			return fmt.Errorf("meowcaller: derive late group SRTCP send epoch: %w", err)
		}
		sender.installKeys(keys)
	}
	r.srtcpSenders = append(r.srtcpSenders, sender)
	return nil
}

// ApplyGroupRawEpoch installs or buffers one shared keygen-v2 media epoch
// according to the authoritative group roster transaction.
func (r *participantReceiveRegistry) ApplyGroupRawEpoch(transactionID uint32, rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L88-L120
	if len(rawKey) != 32 {
		return fmt.Errorf("meowcaller: group raw epoch has %d bytes, want 32", len(rawKey))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hasGroupUpdate || transactionID > r.transactionID {
		if existing, ok := r.pendingEpochs[transactionID]; ok {
			if bytes.Equal(existing, rawKey) {
				return nil
			}
			return fmt.Errorf("meowcaller: conflicting group raw epoch for transaction %d", transactionID)
		}
		r.pendingEpochs[transactionID] = bytes.Clone(rawKey)
		return nil
	}
	return r.applyGroupRawEpochLocked(transactionID, rawKey)
}

func (r *participantReceiveRegistry) applyGroupRawEpochLocked(transactionID uint32, rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L101-L120
	if r.hasEpoch {
		if transactionID < r.installedEpoch.transactionID {
			return nil
		}
		if transactionID == r.installedEpoch.transactionID {
			if bytes.Equal(r.installedEpoch.rawKey, rawKey) {
				return nil
			}
			return fmt.Errorf("meowcaller: conflicting group raw epoch for transaction %d", transactionID)
		}
	}
	if err := r.installGroupRawEpochLocked(rawKey); err != nil {
		return err
	}
	clear(r.installedEpoch.rawKey)
	r.installedEpoch = installedGroupRawEpoch{
		transactionID: transactionID,
		rawKey:        bytes.Clone(rawKey),
	}
	r.hasEpoch = true
	return nil
}

func (r *participantReceiveRegistry) installGroupRawEpochLocked(rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	prepared, err := r.prepareGroupRawEpochLocked(rawKey, r.byDeviceID)
	if err != nil {
		return err
	}
	defer prepared.clear()
	r.installPreparedGroupRawEpochLocked(prepared)
	return nil
}

func (r *participantReceiveRegistry) prepareGroupRawEpochLocked(
	rawKey []byte,
	receivers map[string]*participantAudioReceiver,
) (*preparedGroupRawEpoch, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L110-L126
	sendKeys, err := srtp.DeriveE2eKeysFromRaw(rawKey, r.selfID)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive group send epoch: %w", err)
	}
	sendRTCPKeys, err := srtp.DeriveE2eSRTCPKeysFromRaw(rawKey, r.selfID, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive group SRTCP send epoch: %w", err)
	}
	prepared := &preparedGroupRawEpoch{
		sendKeys:        sendKeys,
		sendRTCPKeys:    sendRTCPKeys,
		receiveKeys:     make(map[*participantAudioReceiver]srtp.E2eSrtpKeys, len(receivers)),
		receiveRTCPKeys: make(map[*participantAudioReceiver]srtp.E2eSrtpKeys, len(receivers)),
	}
	for _, receiver := range receivers {
		keys, deriveErr := srtp.DeriveE2eKeysFromRaw(rawKey, receiver.participantID)
		if deriveErr != nil {
			prepared.clear()
			return nil, fmt.Errorf("meowcaller: derive group receive epoch: %w", deriveErr)
		}
		prepared.receiveKeys[receiver] = keys
		srtcpKeys, deriveErr := srtp.DeriveE2eSRTCPKeysFromRaw(rawKey, receiver.participantID, r.log)
		if deriveErr != nil {
			prepared.clear()
			return nil, fmt.Errorf("meowcaller: derive group SRTCP receive epoch: %w", deriveErr)
		}
		prepared.receiveRTCPKeys[receiver] = srtcpKeys
	}
	return prepared, nil
}

func (r *participantReceiveRegistry) installPreparedGroupRawEpochLocked(prepared *preparedGroupRawEpoch) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L122-L137
	for _, sendPipe := range r.sendPipes {
		sendPipe.installSendKeys(prepared.sendKeys)
	}
	for _, sender := range r.srtcpSenders {
		sender.installKeys(prepared.sendRTCPKeys)
	}
	for receiver, keys := range prepared.receiveKeys {
		receiver.pipe.installRecvKeysPreservingROC(keys)
		receiver.videoPipe.installRecvKeysPreservingROC(keys)
		receiver.appDataPipe.installRecvKeysPreservingROC(keys)
		receiver.srtcp.installKeys(prepared.receiveRTCPKeys[receiver])
	}
	r.log.Info().
		Str("call_id", r.callID).
		Int("remote_participants", len(prepared.receiveKeys)).
		Msg("installed shared group media key epoch")
}

func (p *preparedGroupRawEpoch) clear() {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L117-L128
	if p == nil {
		return
	}
	p.sendKeys = srtp.E2eSrtpKeys{}
	p.sendRTCPKeys = srtp.E2eSrtpKeys{}
	for receiver := range p.receiveKeys {
		p.receiveKeys[receiver] = srtp.E2eSrtpKeys{}
	}
	for receiver := range p.receiveRTCPKeys {
		p.receiveRTCPKeys[receiver] = srtp.E2eSrtpKeys{}
	}
}

func (r *participantReceiveRegistry) RekeyFallback(peerLID string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L89-L100
	peerJID, err := types.ParseJID(peerLID)
	if err != nil {
		return fmt.Errorf("meowcaller: parse answering media peer: %w", err)
	}
	participantID := rtp.FormatE2ESrtpParticipantID(peerJID.String())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasGroupUpdate {
		if _, ok := r.byDeviceID[participantID]; ok {
			r.fallbackID = participantID
			return nil
		}
		return fmt.Errorf("meowcaller: answering media peer is not active in group roster")
	}
	receiver, err := r.newReceiver(peerJID.ToNonAD(), peerJID, 0, false)
	if err != nil {
		return err
	}
	for _, previous := range r.byDeviceID {
		if previous != receiver {
			clearParticipantReceiverKeys(previous)
		}
	}
	r.byDeviceID = map[string]*participantAudioReceiver{participantID: receiver}
	r.bySSRC = map[uint32]*participantAudioReceiver{receiver.ssrc: receiver}
	r.byVideoSSRC = map[uint32]*participantAudioReceiver{receiver.videoSSRC: receiver}
	r.byAppDataSSRC = map[uint32]*participantAudioReceiver{receiver.appDataSSRC: receiver}
	r.bySRTCPSSRC = make(map[uint32]*participantAudioReceiver, len(receiver.streamSSRCs))
	for _, streamSSRC := range receiver.streamSSRCs {
		r.bySRTCPSSRC[streamSSRC] = receiver
	}
	r.byPID = make(map[uint32]*participantAudioReceiver)
	r.fallbackID = participantID
	r.rosterGeneration++
	return nil
}

func (r *participantReceiveRegistry) UnprotectVideo(packet []byte) (unprotectedParticipantMedia, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	header, ok := rtp.ParseRtpHeader(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.byVideoSSRC[header.Ssrc]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", header.Ssrc).Msg("dropping video from inactive participant SSRC")
		return unprotectedParticipantMedia{}, false
	}
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	authenticatedHeader, payload, ok := receiver.videoPipe.UnprotectAudio(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	return receiver.unprotectedMedia(authenticatedHeader, payload), true
}

func (r *participantReceiveRegistry) UnprotectAppData(packet []byte) (unprotectedParticipantMedia, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	header, ok := rtp.ParseRtpHeader(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.byAppDataSSRC[header.Ssrc]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", header.Ssrc).Msg("dropping app-data from inactive participant SSRC")
		return unprotectedParticipantMedia{}, false
	}
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	authenticatedHeader, payload, ok := receiver.appDataPipe.UnprotectAudio(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	return receiver.unprotectedMedia(authenticatedHeader, payload), true
}

func (r *participantAudioReceiver) unprotectedMedia(header rtp.RtpHeader, payload []byte) unprotectedParticipantMedia {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	return unprotectedParticipantMedia{
		ParticipantID: r.participantID,
		UserJID:       r.userJID,
		DeviceJID:     r.deviceJID,
		PID:           r.pid,
		HasPID:        r.hasPID,
		Header:        header,
		Payload:       payload,
		receiver:      r,
	}
}

func (r *participantReceiveRegistry) UnprotectSRTCP(senderSSRC uint32, packet []byte) ([]byte, uint32, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.bySRTCPSSRC[senderSSRC]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", senderSSRC).Msg("dropping SRTCP from inactive participant SSRC")
		return nil, 0, false
	}
	return receiver.srtcp.unprotect(senderSSRC, packet)
}

func (r *participantReceiveRegistry) ActiveParticipantIDs() []string {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L89-L100
	r.mu.RLock()
	defer r.mu.RUnlock()
	participantIDs := make([]string, 0, len(r.byDeviceID))
	for participantID := range r.byDeviceID {
		participantIDs = append(participantIDs, participantID)
	}
	slices.Sort(participantIDs)
	return participantIDs
}

func (r *participantReceiveRegistry) ActiveReceiverSnapshot() (uint64, map[*participantAudioReceiver]struct{}) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L89-L100
	r.mu.RLock()
	defer r.mu.RUnlock()
	active := make(map[*participantAudioReceiver]struct{}, len(r.byDeviceID))
	for _, receiver := range r.byDeviceID {
		active[receiver] = struct{}{}
	}
	return r.rosterGeneration, active
}

func (r *participantReceiveRegistry) ActiveAudioSSRCs() []uint32 {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6e202a6d6ec5a9384bae6ccbe621966edeee6592/datasheets/group-media-rtcp-feedback.md#L133-L135
	r.mu.RLock()
	defer r.mu.RUnlock()
	ssrcs := make([]uint32, 0, len(r.bySSRC))
	for ssrc := range r.bySSRC {
		ssrcs = append(ssrcs, ssrc)
	}
	slices.Sort(ssrcs)
	return ssrcs
}

func (r *participantReceiveRegistry) ActiveVideoSSRCs() []uint32 {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6e202a6d6ec5a9384bae6ccbe621966edeee6592/datasheets/group-media-rtcp-feedback.md#L133-L135
	r.mu.RLock()
	defer r.mu.RUnlock()
	ssrcs := make([]uint32, 0, len(r.byVideoSSRC))
	for ssrc := range r.byVideoSSRC {
		ssrcs = append(ssrcs, ssrc)
	}
	slices.Sort(ssrcs)
	return ssrcs
}

func (r *participantReceiveRegistry) DecodeAudio(packet []byte) (decodedParticipantAudio, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L37-L44
	header, ok := rtp.ParseRtpHeader(packet)
	if !ok {
		return decodedParticipantAudio{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.bySSRC[header.Ssrc]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", header.Ssrc).Msg("dropping audio from inactive participant SSRC")
		return decodedParticipantAudio{}, false
	}

	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	authenticatedHeader, payload, ok := receiver.pipe.UnprotectAudio(packet)
	if !ok {
		r.log.Debug().
			Uint32("ssrc", header.Ssrc).
			Str("participant_id", receiver.participantID).
			Msg("participant audio failed authentication")
		return decodedParticipantAudio{}, false
	}
	pcm := receiver.decoder.Decode(payload)
	return decodedParticipantAudio{
		ParticipantID: receiver.participantID,
		UserJID:       receiver.userJID, DeviceJID: receiver.deviceJID,
		PID: receiver.pid, HasPID: receiver.hasPID, SSRC: authenticatedHeader.Ssrc,
		Timestamp: authenticatedHeader.Timestamp, PCM: pcm,
	}, true
}
