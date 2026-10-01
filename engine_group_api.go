package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/types"
)

func (e *engine) placeGroupCall(
	ctx context.Context,
	targets []string,
	opts GroupCallOptions,
) (*Call, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7cb6045001dafd2514f53e85cd8c3e419c13adbe/datasheets/voip-initial-group-call.md#L63-L101
	if err := e.requireRawCallAdapter(); err != nil {
		return nil, err
	}
	self := e.c.wa.Store.GetLID()
	if self.IsEmpty() {
		return nil, errors.New("meowcaller: no own LID on this session")
	}
	selected, err := e.resolveGroupTargets(ctx, targets, self)
	if err != nil {
		return nil, err
	}
	groupJID, err := parseOptionalGroupJID(opts.GroupJID)
	if err != nil {
		return nil, err
	}
	participants, err := e.discoverGroupParticipants(ctx, append([]types.JID{self.ToNonAD()}, selected...))
	if err != nil {
		return nil, err
	}
	callID := newCallID()
	node, err := signaling.BuildInitialGroupOffer(signaling.InitialGroupOfferParams{
		CallID: callID, CallCreator: self, GroupJID: groupJID,
		Participants: participants, Video: opts.Video,
	})
	if err != nil {
		return nil, err
	}
	node.Attrs["id"] = e.nextCallNodeID()
	call := &Call{
		eng: e, id: callID, peer: selected[0], phase: CallPhaseCalling,
		groupState: selectedGroupCallState(selected),
	}
	e.mu.Lock()
	m := e.entry(callID)
	m.call = call
	m.selfLID = self.String()
	m.peerLID = selected[0].String()
	m.creator = self
	m.from = types.NewJID(callID, "call")
	m.direction = CallDirectionOutgoing
	m.group = true
	m.localVideo = opts.Video
	m.remoteVideo = opts.Video
	m.inviteSelfDevice = groupCallDevice{
		JID: self, CapabilityVersion: 1,
		Capability: append([]byte(nil), signaling.CapabilityOffer...),
	}
	e.mu.Unlock()
	if err = e.transmitCallNode(ctx, node); err != nil {
		e.finishCall(callID, "offer_failed")
		return nil, fmt.Errorf("meowcaller: send group offer: %w", err)
	}
	e.c.log.Info().
		Str("call_id", callID).
		Int("target_count", len(selected)).
		Bool("video", opts.Video).
		Msg("group offer sent")
	return call, nil
}

func (e *engine) inviteParticipant(ctx context.Context, callID, target string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/25eda415afb0f926112ca375c5892b95b4bd6f60/datasheets/voip-group-invite-offer.md#L81-L106
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	targetLID, err := resolvePeerLID(ctx, e.c.wa, target)
	if err != nil {
		return err
	}
	targetLID = targetLID.ToNonAD()
	self := e.c.wa.Store.GetLID()
	if targetLID == self.ToNonAD() {
		return errors.New("meowcaller: cannot add this client to its own call")
	}
	creator, participants, video, err := e.groupInviteRoster(callID)
	if err != nil {
		return err
	}
	for _, participant := range participants {
		if participant.JID.ToNonAD() == targetLID {
			return errors.New("meowcaller: participant is already in the call roster")
		}
	}
	targetDevices, err := e.discoverTargetDevices(ctx, targetLID)
	if err != nil {
		return err
	}
	node, err := signaling.BuildGroupInviteOffer(signaling.GroupInviteOfferParams{
		CallID: callID, To: targetLID, CallCreator: creator,
		TargetDevices: targetDevices, Participants: participants, Video: video,
	})
	if err != nil {
		return err
	}
	node.Attrs["id"] = e.nextCallNodeID()
	if err = e.transmitCallNode(ctx, node); err != nil {
		return fmt.Errorf("meowcaller: add participant: %w", err)
	}
	return nil
}

func (e *engine) ringParticipant(ctx context.Context, callID, target string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/25eda415afb0f926112ca375c5892b95b4bd6f60/datasheets/voip-group-invite-offer.md#L81-L106
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	targetLID, err := resolvePeerLID(ctx, e.c.wa, target)
	if err != nil {
		return err
	}
	targetLID = targetLID.ToNonAD()
	creator, participants, video, err := e.groupInviteRoster(callID)
	if err != nil {
		return err
	}
	found := false
	connected := make([]signaling.GroupCallParticipant, 0, len(participants))
	for _, participant := range participants {
		if participant.JID.ToNonAD() != targetLID {
			if participant.State == "connected" {
				connected = append(connected, participant)
			}
			continue
		}
		if participant.State == "connected" {
			return errors.New("meowcaller: participant is already connected")
		}
		found = true
	}
	if !found {
		return errors.New("meowcaller: participant is not in the call roster")
	}
	if len(connected) == 0 {
		return errors.New("meowcaller: group call has no connected participant roster")
	}
	targetDevices, err := e.discoverTargetDevices(ctx, targetLID)
	if err != nil {
		return err
	}
	node, err := signaling.BuildGroupInviteOffer(signaling.GroupInviteOfferParams{
		CallID: callID, To: targetLID, CallCreator: creator,
		TargetDevices: targetDevices, Participants: connected, Video: video,
	})
	if err != nil {
		return err
	}
	node.Attrs["id"] = e.nextCallNodeID()
	if err = e.transmitCallNode(ctx, node); err != nil {
		return fmt.Errorf("meowcaller: ring participant: %w", err)
	}
	return nil
}

func (e *engine) groupInviteRoster(
	callID string,
) (types.JID, []signaling.GroupCallParticipant, bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/1ebd064663ac336ff3d1fc65d9baa974148fe73e/datasheets/voip-group-participant-invite.md#L36-L72
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		return types.EmptyJID, nil, false, errors.New("meowcaller: call is not active")
	}
	if m.call.State() != CallPhaseActive {
		return types.EmptyJID, nil, false, errors.New("meowcaller: call is not connected")
	}
	if m.creator.IsEmpty() {
		return types.EmptyJID, nil, false, errors.New("meowcaller: call creator is unavailable")
	}
	video := m.localVideo || m.remoteVideo
	if m.groupUpdate != nil {
		return m.creator, signalingParticipantsFromUpdate(*m.groupUpdate), video, nil
	}
	if m.inviteSelfDevice.JID.IsEmpty() || len(m.inviteSelfDevice.Capability) == 0 {
		return types.EmptyJID, nil, false, errors.New("meowcaller: local active device capability is unavailable")
	}
	if m.invitePeerDevice.JID.IsEmpty() || len(m.invitePeerDevice.Capability) == 0 {
		return types.EmptyJID, nil, false, errors.New("meowcaller: peer active device capability is unavailable")
	}
	self, err := types.ParseJID(m.selfLID)
	if err != nil || self.IsEmpty() {
		return types.EmptyJID, nil, false, errors.New("meowcaller: local call identity is unavailable")
	}
	peer, err := types.ParseJID(m.peerLID)
	if err != nil || peer.IsEmpty() {
		return types.EmptyJID, nil, false, errors.New("meowcaller: peer call identity is unavailable")
	}
	participants := []signaling.GroupCallParticipant{
		{
			JID: self.ToNonAD(), State: "connected",
			Devices: []signaling.GroupCallDevice{signalingDeviceFromInternal(m.inviteSelfDevice)},
		},
		{
			JID: peer.ToNonAD(), State: "connected",
			Devices: []signaling.GroupCallDevice{signalingDeviceFromInternal(m.invitePeerDevice)},
		},
	}
	return m.creator, participants, video, nil
}

func signalingParticipantsFromUpdate(update groupCallUpdate) []signaling.GroupCallParticipant {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/1ebd064663ac336ff3d1fc65d9baa974148fe73e/datasheets/voip-group-participant-invite.md#L36-L72
	participants := make([]signaling.GroupCallParticipant, len(update.Participants))
	for i, participant := range update.Participants {
		participants[i] = signaling.GroupCallParticipant{
			JID: participant.JID, PN: participant.PN,
			State: participant.State, Type: participant.Type,
			Devices: make([]signaling.GroupCallDevice, len(participant.Devices)),
		}
		for j, device := range participant.Devices {
			participants[i].Devices[j] = signalingDeviceFromInternal(device)
		}
	}
	return participants
}

func signalingDeviceFromInternal(device groupCallDevice) signaling.GroupCallDevice {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/1ebd064663ac336ff3d1fc65d9baa974148fe73e/datasheets/voip-group-participant-invite.md#L36-L72
	return signaling.GroupCallDevice{
		JID: device.JID, Platform: device.Platform,
		PID: device.PID, HasPID: device.HasPID,
		CapabilityVersion: device.CapabilityVersion,
		Capability:        append([]byte(nil), device.Capability...),
	}
}

func (e *engine) discoverTargetDevices(ctx context.Context, target types.JID) ([]types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/25eda415afb0f926112ca375c5892b95b4bd6f60/datasheets/voip-group-invite-offer.md#L81-L106
	devices, err := e.c.wa.GetUserDevices(ctx, []types.JID{target})
	if err != nil {
		return nil, fmt.Errorf("meowcaller: call invite device discovery: %w", err)
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("meowcaller: call invite target %s has no devices", target)
	}
	return devices, nil
}

func (e *engine) resolveGroupTargets(
	ctx context.Context,
	targets []string,
	self types.JID,
) ([]types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L99
	selected := make([]types.JID, 0, len(targets))
	seen := make(map[types.JID]struct{}, len(targets))
	for i, target := range targets {
		participant, err := resolvePeerLID(ctx, e.c.wa, target)
		if err != nil {
			return nil, fmt.Errorf("meowcaller: resolve group target %d: %w", i, err)
		}
		participant = participant.ToNonAD()
		if participant == self.ToNonAD() {
			return nil, fmt.Errorf("meowcaller: group target %d is this client", i)
		}
		if _, exists := seen[participant]; exists {
			continue
		}
		seen[participant] = struct{}{}
		selected = append(selected, participant)
	}
	if len(selected) < 2 {
		return nil, errors.New("meowcaller: group call requires at least two distinct remote targets")
	}
	if len(selected) > 31 {
		return nil, errors.New("meowcaller: group call supports at most 31 remote targets")
	}
	return selected, nil
}

func (e *engine) discoverGroupParticipants(
	ctx context.Context,
	users []types.JID,
) ([]signaling.GroupCallParticipant, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7cb6045001dafd2514f53e85cd8c3e419c13adbe/datasheets/voip-initial-group-call.md#L63-L101
	devices, err := e.c.wa.GetUserDevices(ctx, users)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: group device discovery: %w", err)
	}
	byUser := make(map[types.JID][]types.JID, len(users))
	for _, device := range devices {
		user := device.ToNonAD()
		byUser[user] = append(byUser[user], device)
	}
	self := e.c.wa.Store.GetLID()
	byUser[self.ToNonAD()] = []types.JID{self}
	participants := make([]signaling.GroupCallParticipant, 0, len(users))
	for _, user := range users {
		user = user.ToNonAD()
		userDevices := byUser[user]
		if len(userDevices) == 0 {
			return nil, fmt.Errorf("meowcaller: participant %s has no reachable devices", user)
		}
		participant := signaling.GroupCallParticipant{JID: user}
		for _, device := range userDevices {
			groupDevice := signaling.GroupCallDevice{JID: device}
			if device == self {
				groupDevice.CapabilityVersion = 1
				groupDevice.Capability = append([]byte(nil), signaling.CapabilityOffer...)
			}
			participant.Devices = append(participant.Devices, groupDevice)
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func parseOptionalGroupJID(raw string) (types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L96
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return types.EmptyJID, nil
	}
	return parseGroupCallID(raw)
}

func selectedGroupCallState(targets []types.JID) *GroupCallState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L104
	state := &GroupCallState{Participants: make([]GroupCallParticipant, len(targets))}
	for i, target := range targets {
		state.Participants[i] = GroupCallParticipant{JID: target, State: "outgoing"}
	}
	return state
}

func (e *engine) createPublicCallLink(ctx context.Context, media callLinkMedia) (*callLinkCreateResult, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L17-L50
	if err := e.requireRawCallAdapter(); err != nil {
		return nil, err
	}
	requestID := e.nextCallNodeID()
	node, err := signaling.BuildCallLinkCreate(signaling.CallLinkMedia(media), requestID)
	if err != nil {
		return nil, err
	}
	response, err := e.requestCall(ctx, node, requestID)
	if err != nil {
		return nil, err
	}
	link, err := signaling.ParseCallLinkCreateAck(response)
	if err != nil {
		return nil, err
	}
	return &callLinkCreateResult{Token: link.Token, Media: callLinkMedia(link.Media)}, nil
}

func (e *engine) previewPublicCallLink(
	ctx context.Context,
	token string,
	media callLinkMedia,
) (*callLinkPreviewResult, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L52-L74
	if err := e.requireRawCallAdapter(); err != nil {
		return nil, err
	}
	requestID := e.nextCallNodeID()
	node, err := signaling.BuildCallLinkQuery(token, signaling.CallLinkMedia(media), requestID)
	if err != nil {
		return nil, err
	}
	response, err := e.requestCall(ctx, node, requestID)
	if err != nil {
		return nil, err
	}
	preview, err := signaling.ParseCallLinkQueryAck(response)
	if err != nil {
		return nil, err
	}
	return &callLinkPreviewResult{
		Token: preview.Token, Media: callLinkMedia(preview.Media),
		Creator: preview.Creator, CreatorPN: preview.CreatorPN,
		WaitingRoomEnabled: preview.WaitingRoomEnabled, IsAdmin: preview.IsAdmin,
	}, nil
}

func (e *engine) joinPublicCallLink(
	ctx context.Context,
	token string,
	opts CallLinkOptions,
) (*Call, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L76-L155
	if err := e.requireRawCallAdapter(); err != nil {
		return nil, err
	}
	media := selectedCallLinkMedia(opts)
	requestID := e.nextCallNodeID()
	node, err := signaling.BuildCallLinkJoin(token, signaling.CallLinkMedia(media), requestID)
	if err != nil {
		return nil, err
	}
	response, err := e.requestCall(ctx, node, requestID)
	if err != nil {
		return nil, err
	}
	joined, err := signaling.ParseCallLinkJoinAck(response)
	if err != nil {
		return nil, err
	}
	if joined.CallID == "" {
		return nil, errors.New("meowcaller: call-link join returned no call ID")
	}
	result := callLinkJoinResult{
		Token: joined.Token, Media: callLinkMedia(joined.Media),
		CallID: joined.CallID, CallCreator: joined.CallCreator,
		WaitingRoomEnabled: joined.WaitingRoomEnabled,
		InWaitingRoom:      joined.InWaitingRoom, IsAdmin: joined.IsAdmin,
	}
	phase := CallPhaseConnecting
	if result.InWaitingRoom {
		phase = CallPhaseWaitingRoom
	}
	call := &Call{eng: e, id: result.CallID, peer: result.CallCreator, phase: phase}
	call.setWaitingRoomState(waitingRoomStateFromJoin(&result))

	e.mu.Lock()
	m := e.entry(result.CallID)
	m.call = call
	m.group = true
	m.selfLID = e.c.wa.Store.GetLID().String()
	m.peerLID = result.CallCreator.String()
	m.creator = result.CallCreator
	m.from = types.NewJID(result.CallID, "call")
	m.direction = CallDirectionOutgoing
	m.localVideo = opts.Video
	m.remoteVideo = opts.Video
	self := e.c.wa.Store.GetLID()
	m.inviteSelfDevice = groupCallDevice{
		JID: self, CapabilityVersion: 1,
		Capability: append([]byte(nil), signaling.CapabilityOffer...),
	}
	e.mu.Unlock()
	// Unlike the reference, defer roster installation to group_update, which can reuse the
	// ACK's transaction ID.
	if result.InWaitingRoom {
		e.startWaitingRoomHeartbeat(result.CallID)
	}
	return call, nil
}

func (e *engine) requestCall(ctx context.Context, node waBinary.Node, requestID string) (*waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/e9a033b24933681519c70b9237db3e91e93e4265/client.go#L745-L759
	if e.requestCallNode == nil {
		return nil, errors.New("meowcaller: call request transport is unavailable")
	}
	return e.requestCallNode(ctx, node, requestID)
}

func (e *engine) setApprovalRequired(ctx context.Context, callID string, enabled bool) error {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L316-L386
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	creator, err := e.callControlCreator(callID)
	if err != nil {
		return err
	}
	node := signaling.BuildWaitingRoomToggle(callID, creator, enabled, e.nextCallNodeID())
	return e.transmitCallNode(ctx, node)
}

func (e *engine) controlWaitingParticipant(
	ctx context.Context,
	callID, user string,
	admit bool,
) error {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L321-L386
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	creator, err := e.callControlCreator(callID)
	if err != nil {
		return err
	}
	jid, err := parseCallTarget(user)
	if err != nil {
		return err
	}
	var node = signaling.BuildWaitingRoomDeny(callID, creator, jid.ToNonAD(), e.nextCallNodeID())
	if admit {
		node = signaling.BuildWaitingRoomAdmit(callID, creator, jid.ToNonAD(), e.nextCallNodeID())
	}
	return e.transmitCallNode(ctx, node)
}

func (e *engine) setHandRaised(callID string, raised bool) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L31-L43
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	creator, err := e.callControlCreator(callID)
	if err != nil {
		return err
	}
	node := signaling.BuildRaiseHand(
		callID,
		types.NewJID(callID, "call"),
		creator,
		e.nextCallNodeID(),
		raised,
	)
	if err = e.transmitCallNode(context.Background(), node); err != nil {
		return err
	}
	e.mu.Lock()
	var call *Call
	if current := e.calls[callID]; current != nil {
		call = current.call
	}
	participant := e.c.wa.Store.GetLID().ToNonAD()
	e.mu.Unlock()
	if call != nil {
		call.dispatchHandRaise(HandRaiseState{Participant: participant, Raised: raised})
	}
	return nil
}

func (e *engine) setScreenShare(callID string, active bool, screenShareID *uint32) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L44-L56
	if err := e.requireRawCallAdapter(); err != nil {
		return err
	}
	creator, err := e.callControlCreator(callID)
	if err != nil {
		return err
	}
	state := signaling.ScreenShareStopped
	if active {
		state = signaling.ScreenShareStarted
	}
	node := signaling.BuildScreenShare(
		callID,
		types.NewJID(callID, "call"),
		creator,
		e.nextCallNodeID(),
		state,
		screenShareID,
	)
	if err = e.transmitCallNode(context.Background(), node); err != nil {
		return err
	}
	e.mu.Lock()
	var call *Call
	if current := e.calls[callID]; current != nil {
		call = current.call
	}
	participant := e.c.wa.Store.GetLID().ToNonAD()
	e.mu.Unlock()
	if call != nil {
		call.dispatchScreenShare(ScreenShareState{
			Participant: participant, Active: active, Version: 2,
			ScreenShareID: valueOrZero(screenShareID), HasScreenShareID: screenShareID != nil,
		})
		if active {
			call.requestVideoKeyframe()
		}
	}
	return nil
}

func (e *engine) callControlCreator(callID string) (types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/699185f41519da3177c17ea6a10f9d4aa48b6941/datasheets/voip-group-call-state.md#L62-L68
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded || m.creator.IsEmpty() {
		return types.EmptyJID, errors.New("meowcaller: group call is not active")
	}
	return m.creator, nil
}

func (e *engine) startWaitingRoomHeartbeat(callID string) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L215-L259
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.waitingRoomCancel = cancel
	creator := m.creator
	e.mu.Unlock()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				node := signaling.BuildWaitingRoomHeartbeat(callID, creator, e.nextCallNodeID())
				if err := e.transmitCallNode(ctx, node); err != nil && ctx.Err() == nil {
					e.c.log.Debug().Err(err).Str("call_id", callID).Msg("waiting-room heartbeat failed")
				}
			}
		}
	}()
}

func valueOrZero(value *uint32) uint32 {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L44-L56
	if value == nil {
		return 0
	}
	return *value
}
