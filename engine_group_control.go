package meowcaller

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

func (e *engine) onUnknownCallEvent(node *waBinary.Node) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/48c2391ce9f7dcc2b3f223f72f1b5f0c627ad943/datasheets/voip-group-update-ingest.md#L105-L148
	envelope, err := signaling.ParseCallControlEnvelope(node)
	if err != nil {
		return
	}
	switch envelope.Action.Tag {
	case "group_update":
		parsed, parseErr := signaling.ParseGroupUpdate(&envelope.Action)
		if parseErr != nil {
			e.c.log.Warn().Err(parseErr).Str("call_id", envelope.CallID).Msg("parse group update failed")
			return
		}
		update := groupCallUpdateFromSignaling(*parsed)
		if e.applyGroupUpdate(update) && update.RekeyRequested {
			if fanoutErr := e.distributeRequestedGroupEpoch(context.Background(), update); fanoutErr != nil {
				e.c.log.Warn().Err(fanoutErr).Str("call_id", update.CallID).Msg("group rekey fanout failed")
			}
		}
	case "waiting_room_update":
		room, parseErr := signaling.ParseWaitingRoomUpdate(&envelope.Action)
		if parseErr != nil {
			e.c.log.Warn().Err(parseErr).Str("call_id", envelope.CallID).Msg("parse waiting room update failed")
			return
		}
		e.applyWaitingRoomUpdate(*room)
	case "user_action":
		raised, parseErr := signaling.ParseRaiseHand(&envelope.Action)
		if parseErr != nil {
			e.c.log.Warn().Err(parseErr).Str("call_id", envelope.CallID).Msg("parse hand state failed")
			return
		}
		e.dispatchRemoteHandState(envelope, raised)
	case "screen_share":
		screenShare, parseErr := signaling.ParseScreenShare(&envelope.Action)
		if parseErr != nil {
			e.c.log.Warn().Err(parseErr).Str("call_id", envelope.CallID).Msg("parse screen share failed")
			return
		}
		e.dispatchRemoteScreenShare(envelope, *screenShare)
	case "enc_rekey":
		e.c.log.Debug().
			Str("call_id", envelope.CallID).
			Msg("group rekey received")
		if err = e.ingestGroupEpoch(context.Background(), envelope); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", envelope.CallID).Msg("group rekey ingest failed")
			return
		}
		e.c.log.Debug().
			Str("call_id", envelope.CallID).
			Msg("group rekey installed")
	}
}

func (e *engine) applyWaitingRoomUpdate(room signaling.WaitingRoom) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L388-L441
	state := WaitingRoomState{
		Enabled: room.Enabled, IsAdmin: room.IsAdmin,
		TransactionID: room.TransactionID,
		Users:         make([]WaitingRoomUser, len(room.Users)),
	}
	for i, user := range room.Users {
		state.Users[i] = WaitingRoomUser{
			JID: user.JID, PN: user.PN, State: user.State,
			Guest: user.IsGuest(), Name: user.Name,
		}
	}
	e.mu.Lock()
	m := e.calls[room.CallID]
	if m != nil && m.call != nil {
		current, ok := m.call.WaitingRoomState()
		state.InWaitingRoom = ok && current.InWaitingRoom
	}
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		call.setWaitingRoomState(state)
	}
}

func (e *engine) dispatchRemoteHandState(envelope *signaling.CallControlEnvelope, raised bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L31-L43
	if envelope == nil {
		return
	}
	participant := callControlParticipant(envelope)
	e.mu.Lock()
	m := e.calls[envelope.CallID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		call.dispatchHandRaise(HandRaiseState{Participant: participant, Raised: raised})
	}
}

func (e *engine) dispatchRemoteScreenShare(
	envelope *signaling.CallControlEnvelope,
	screenShare signaling.ScreenShare,
) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L44-L56
	if envelope == nil {
		return
	}
	e.mu.Lock()
	m := e.calls[envelope.CallID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		call.dispatchScreenShare(ScreenShareState{
			Participant:      callControlParticipant(envelope),
			Active:           screenShare.State == signaling.ScreenShareStarted,
			Version:          screenShare.Version,
			ScreenShareID:    screenShare.ScreenShareID,
			HasScreenShareID: screenShare.HasScreenShareID,
		})
	}
}

func callControlParticipant(envelope *signaling.CallControlEnvelope) types.JID {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L31-L56
	if envelope == nil {
		return types.EmptyJID
	}
	if !envelope.Participant.IsEmpty() {
		return envelope.Participant.ToNonAD()
	}
	return envelope.From.ToNonAD()
}

func (e *engine) applyGroupUpdate(update groupCallUpdate) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L83-L100
	if update.CallID == "" {
		return false
	}
	e.mu.Lock()
	m := e.calls[update.CallID]
	if m == nil {
		e.mu.Unlock()
		e.c.log.Debug().
			Str("call_id", update.CallID).
			Uint32("transaction_id", update.TransactionID).
			Msg("dropping group update for unknown or ended call")
		return false
	}
	if m.groupUpdate != nil && update.TransactionID <= m.groupUpdate.TransactionID {
		e.mu.Unlock()
		return false
	}
	stored := cloneGroupCallUpdate(update)
	receivers := m.groupReceivers
	e.mu.Unlock()

	if receivers != nil {
		if err := receivers.ApplyGroupUpdate(stored); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", update.CallID).Msg("apply group media roster failed")
			return false
		}
	}

	e.mu.Lock()
	m = e.calls[update.CallID]
	if m == nil {
		e.mu.Unlock()
		if receivers != nil {
			receivers.clear()
		}
		return false
	}
	if m.groupUpdate != nil && update.TransactionID <= m.groupUpdate.TransactionID {
		e.mu.Unlock()
		return false
	}
	m.group = true
	m.groupUpdate = &stored
	if !update.CallCreator.IsEmpty() {
		m.creator = update.CallCreator
	}
	m.from = types.NewJID(update.CallID, "call")
	call := m.call
	waitingRoomCancel := m.waitingRoomCancel
	m.waitingRoomCancel = nil
	e.mu.Unlock()
	e.c.log.Debug().
		Str("call_id", update.CallID).
		Uint32("transaction_id", update.TransactionID).
		Bool("has_relay", stored.Relay != nil).
		Bool("rekey_requested", update.RekeyRequested).
		Msg("group update installed")

	if waitingRoomCancel != nil {
		waitingRoomCancel()
	}
	if call != nil {
		if call.State() == CallPhaseWaitingRoom {
			call.setPhase(CallPhaseConnecting)
			if state, ok := call.WaitingRoomState(); ok {
				state.InWaitingRoom = false
				call.setWaitingRoomState(state)
			}
		}
		call.setGroupState(groupCallStateFromUpdate(stored))
	}
	e.maybeStartMedia(update.CallID)
	return true
}

func (e *engine) ingestGroupEpoch(ctx context.Context, envelope *signaling.CallControlEnvelope) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/747c6a1b8a0370358ef18bbaa5e029b960c2f836/datasheets/voip-group-enc-rekey-ingest.md#L39-L123
	if envelope == nil {
		return errors.New("meowcaller: group rekey envelope is nil")
	}
	rekey, err := signaling.ParseGroupCallEncRekey(&envelope.Action)
	if err != nil {
		return err
	}
	enc, ok := envelope.Action.GetOptionalChildByTag("enc")
	if !ok {
		return errors.New("meowcaller: group rekey has no encrypted payload")
	}
	plaintext, _, err := e.c.wa.DangerousInternals().DecryptDM(
		ctx,
		&enc,
		envelope.From,
		rekey.EncryptionType == "pkmsg",
		envelope.Timestamp,
	)
	if err != nil {
		return fmt.Errorf("meowcaller: decrypt group rekey: %w", err)
	}
	defer clear(plaintext)
	var message waE2E.Message
	if err = proto.Unmarshal(plaintext, &message); err != nil {
		return fmt.Errorf("meowcaller: decode group rekey: %w", err)
	}
	rawKey := message.GetCall().GetCallKey()
	if len(rawKey) != 32 {
		return fmt.Errorf("meowcaller: group rekey key is %d bytes, want 32", len(rawKey))
	}
	defer clear(rawKey)
	return e.installGroupEpoch(rekey.CallID, rekey.TransactionID, rawKey)
}

func (e *engine) installGroupEpoch(callID string, transactionID uint32, rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/d9df3eb9d96ea5260ffcd4036b6669499a1c1bc2/datasheets/voip-group-key-epoch-fanout.md#L85-L143
	if callID == "" || transactionID == 0 || len(rawKey) != 32 {
		return errors.New("meowcaller: invalid group key epoch")
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || !m.group {
		e.mu.Unlock()
		return fmt.Errorf("meowcaller: unknown group call %s", callID)
	}
	if m.hasGroupEpoch {
		switch {
		case transactionID < m.groupEpochTxID:
			e.mu.Unlock()
			return nil
		case transactionID == m.groupEpochTxID && bytes.Equal(m.groupRawEpoch, rawKey):
			e.mu.Unlock()
			return nil
		case transactionID == m.groupEpochTxID:
			e.mu.Unlock()
			return fmt.Errorf("meowcaller: conflicting group epoch for transaction %d", transactionID)
		}
	}
	receivers := m.groupReceivers
	e.mu.Unlock()
	if receivers != nil {
		if err := receivers.ApplyGroupRawEpoch(transactionID, rawKey); err != nil {
			return err
		}
	}

	e.mu.Lock()
	m = e.calls[callID]
	if m == nil || !m.group {
		e.mu.Unlock()
		return fmt.Errorf("meowcaller: group call %s ended during rekey", callID)
	}
	if m.hasGroupEpoch {
		switch {
		case transactionID < m.groupEpochTxID:
			e.mu.Unlock()
			return nil
		case transactionID == m.groupEpochTxID && bytes.Equal(m.groupRawEpoch, rawKey):
			e.mu.Unlock()
			return nil
		case transactionID == m.groupEpochTxID:
			e.mu.Unlock()
			return fmt.Errorf("meowcaller: conflicting group epoch for transaction %d", transactionID)
		}
	}
	clear(m.groupRawEpoch)
	m.groupRawEpoch = bytes.Clone(rawKey)
	m.groupEpochTxID = transactionID
	m.hasGroupEpoch = true
	e.mu.Unlock()
	e.maybeStartMedia(callID)
	return nil
}

func (e *engine) distributeRequestedGroupEpoch(ctx context.Context, update groupCallUpdate) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/d9df3eb9d96ea5260ffcd4036b6669499a1c1bc2/datasheets/voip-group-key-epoch-fanout.md#L99-L162
	self := e.c.wa.Store.GetLID()
	recipients := groupRekeyRecipients(update, self)
	if len(recipients) == 0 {
		return errors.New("meowcaller: group rekey has no remote connected devices")
	}
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		return err
	}
	nodes := make([]waBinary.Node, len(recipients))
	for i, recipient := range recipients {
		ciphertext, encType, _, err := encryptCallKeyForDevice(ctx, e.c.wa, recipient, rawKey)
		if err != nil {
			clear(rawKey)
			return fmt.Errorf("meowcaller: encrypt group epoch for %s: %w", recipient, err)
		}
		node, err := signaling.BuildGroupEncRekey(signaling.GroupEncRekeyParams{
			CallID: update.CallID, To: recipient, CallCreator: update.CallCreator,
			TransactionID: update.TransactionID, RequestID: e.nextCallNodeID(),
			DeviceKey: signaling.OfferDeviceKey{
				DeviceJid: recipient, Ciphertext: ciphertext, EncType: encType,
			},
		})
		if err != nil {
			clear(rawKey)
			return err
		}
		nodes[i] = node
	}
	var fanoutErrors []error
	for i, node := range nodes {
		if err := e.transmitCallNode(ctx, node); err != nil {
			fanoutErrors = append(
				fanoutErrors,
				fmt.Errorf("meowcaller: send group epoch to %s: %w", recipients[i], err),
			)
		}
	}
	if err := e.installGroupEpoch(update.CallID, update.TransactionID, rawKey); err != nil {
		fanoutErrors = append(fanoutErrors, fmt.Errorf("meowcaller: install local group epoch: %w", err))
	}
	clear(rawKey)
	return errors.Join(fanoutErrors...)
}

func groupRekeyRecipients(update groupCallUpdate, self types.JID) []types.JID {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/d9df3eb9d96ea5260ffcd4036b6669499a1c1bc2/datasheets/voip-group-key-epoch-fanout.md#L145-L162
	seen := make(map[types.JID]struct{})
	var recipients []types.JID
	for _, participant := range update.Participants {
		if participant.State != "connected" {
			continue
		}
		for _, device := range participant.Devices {
			if !device.HasPID || device.JID.IsEmpty() || device.JID == self {
				continue
			}
			if _, exists := seen[device.JID]; exists {
				continue
			}
			seen[device.JID] = struct{}{}
			recipients = append(recipients, device.JID)
		}
	}
	return recipients
}

func cloneGroupCallUpdate(update groupCallUpdate) groupCallUpdate {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L59-L69
	out := update
	out.Participants = make([]groupCallParticipant, len(update.Participants))
	for i, participant := range update.Participants {
		out.Participants[i] = participant
		out.Participants[i].Devices = make([]groupCallDevice, len(participant.Devices))
		for j, device := range participant.Devices {
			out.Participants[i].Devices[j] = device
			out.Participants[i].Devices[j].Capability = bytes.Clone(device.Capability)
		}
	}
	if update.Relay != nil {
		relay := *update.Relay
		relay.Key = bytes.Clone(update.Relay.Key)
		relay.HBHKey = bytes.Clone(update.Relay.HBHKey)
		relay.Tokens = cloneByteSlices(update.Relay.Tokens)
		relay.AuthTokens = cloneByteSlices(update.Relay.AuthTokens)
		relay.Endpoints = append([]groupCallRelayEndpoint(nil), update.Relay.Endpoints...)
		out.Relay = &relay
	}
	return out
}
