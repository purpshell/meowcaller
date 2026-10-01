package meowcaller

import (
	"bytes"

	"github.com/purpshell/meowcaller/signaling"
	"github.com/polymorfa/hypermeow/types"
)

// groupCallUpdate is the control-plane-neutral form of one authoritative
// group-call snapshot.
type groupCallUpdate struct {
	CallID         string
	CallCreator    types.JID
	GroupJID       types.JID
	TransactionID  uint32
	Media          string
	ConnectedLimit uint32
	Joinable       bool
	AVUpgradable   bool
	RekeyRequested bool
	Participants   []groupCallParticipant
	Relay          *groupCallRelay
}

type groupCallParticipant struct {
	JID     types.JID
	PN      types.JID
	State   string
	Type    string
	Devices []groupCallDevice
}

type groupCallDevice struct {
	JID               types.JID
	Platform          string
	PID               uint32
	HasPID            bool
	CapabilityVersion uint32
	Capability        []byte
}

type groupCallRelay struct {
	TransactionID      uint32
	SelfPID            uint32
	HasSelfPID         bool
	UUID               string
	ParticipantUUID    string
	AttributePadding   bool
	WarpMITagLength    uint32
	HasWarpMITagLength bool
	Key                []byte
	HBHKey             []byte
	Tokens             [][]byte
	AuthTokens         [][]byte
	Endpoints          []groupCallRelayEndpoint
}

type groupCallRelayEndpoint struct {
	RelayID     uint32
	TokenID     uint32
	AuthTokenID uint32
	RelayName   string
	IsFNA       bool
	IPv4        string
	Port        uint16
}

type groupCallEncRekey struct {
	CallID            string
	CallCreator       types.JID
	TransactionID     uint32
	KeyGeneration     uint32
	EncryptionType    string
	EncryptionVersion uint32
	Ciphertext        []byte
}

type callLinkMedia string

const (
	callLinkMediaAudio callLinkMedia = "audio"
	callLinkMediaVideo callLinkMedia = "video"
)

type callLinkPreviewResult struct {
	Token              string
	Media              callLinkMedia
	Creator            types.JID
	CreatorPN          types.JID
	WaitingRoomEnabled bool
	IsAdmin            bool
}

type callLinkCreateResult struct {
	Token string
	Media callLinkMedia
}

type callLinkJoinResult struct {
	Token              string
	Media              callLinkMedia
	CallID             string
	CallCreator        types.JID
	WaitingRoomEnabled bool
	InWaitingRoom      bool
	IsAdmin            bool
}

func groupCallUpdateFromSignaling(update signaling.GroupCallUpdate) groupCallUpdate {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/699185f41519da3177c17ea6a10f9d4aa48b6941/datasheets/voip-group-call-state.md#L22-L68
	out := groupCallUpdate{
		CallID: update.CallID, CallCreator: update.CallCreator,
		GroupJID: update.GroupJID, TransactionID: update.TransactionID,
		Media: update.Media, ConnectedLimit: update.ConnectedLimit,
		Joinable: update.Joinable, AVUpgradable: update.AVUpgradable,
		RekeyRequested: update.RekeyRequested,
		Participants:   make([]groupCallParticipant, len(update.Participants)),
	}
	for i, participant := range update.Participants {
		out.Participants[i] = groupCallParticipant{
			JID: participant.JID, PN: participant.PN,
			State: participant.State, Type: participant.Type,
			Devices: make([]groupCallDevice, len(participant.Devices)),
		}
		for j, device := range participant.Devices {
			out.Participants[i].Devices[j] = groupCallDevice{
				JID: device.JID, Platform: device.Platform,
				PID: device.PID, HasPID: device.HasPID,
				CapabilityVersion: device.CapabilityVersion,
				Capability:        bytes.Clone(device.Capability),
			}
		}
	}
	if update.Relay != nil {
		out.Relay = &groupCallRelay{
			TransactionID: update.Relay.TransactionID,
			SelfPID:       update.Relay.SelfPID, HasSelfPID: update.Relay.HasSelfPID,
			UUID: update.Relay.UUID, ParticipantUUID: update.Relay.ParticipantUUID,
			AttributePadding:   update.Relay.AttributePadding,
			WarpMITagLength:    update.Relay.WarpMITagLength,
			HasWarpMITagLength: update.Relay.HasWarpMITagLength,
			Key:                bytes.Clone(update.Relay.Key), HBHKey: bytes.Clone(update.Relay.HBHKey),
			Tokens:     cloneByteSlices(update.Relay.Tokens),
			AuthTokens: cloneByteSlices(update.Relay.AuthTokens),
			Endpoints:  make([]groupCallRelayEndpoint, len(update.Relay.Endpoints)),
		}
		for i, endpoint := range update.Relay.Endpoints {
			out.Relay.Endpoints[i] = groupCallRelayEndpoint{
				RelayID: endpoint.RelayID, TokenID: endpoint.TokenID,
				AuthTokenID: endpoint.AuthTokenID, RelayName: endpoint.RelayName,
				IsFNA: endpoint.IsFNA, IPv4: endpoint.IPv4, Port: endpoint.Port,
			}
		}
	}
	return out
}

func cloneByteSlices(values [][]byte) [][]byte {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L59-L69
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = bytes.Clone(value)
	}
	return out
}
