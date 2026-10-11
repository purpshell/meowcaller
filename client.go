package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
	"github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"
)

// Client is the managed entry point to the WhatsApp calling stack. It wraps a
// *whatsmeow.Client and drives direct and group call lifecycles. Construct it
// before connecting whatsmeow so the raw call adapter can be installed safely.
//
// The library never configures logging; pass WithLogger to surface its debug/trace.
type Client struct {
	wa   *whatsmeow.Client
	log  zerolog.Logger
	diag *diag.Recorder
	eng  *engine

	audioRTP      func(callID string, ssrc uint32, seq uint16, timestamp uint32)
	reorderWindow time.Duration
	reorderReport func(callID string, audio, video AudioReorderReport)

	getGroupInfo func(context.Context, types.JID) (*types.GroupInfo, error)
	ownGroupJIDs func() []types.JID

	mu             sync.Mutex
	onIncomingCall func(*Call)
}

// CallOptions controls media negotiated for an outbound call.
type CallOptions struct {
	// Video advertises a WhatsApp video call. The caller must provide encoded H.264
	// access units with Call.SendVideo after media is active.
	Video bool
}

// GroupCallOptions controls media and optional chat binding for an outbound group call.
type GroupCallOptions struct {
	// GroupJID binds the call to a WhatsApp group. Leave empty for an ad-hoc call.
	GroupJID string
	// Video advertises an H.264 group video call.
	Video bool
}

// NewClient wraps a connected whatsmeow client and installs the call event handlers.
// Construct it before the whatsmeow client connects so the low-level <ack>/<call>
// interception is in place before the receive loop starts.
func NewClient(wa *whatsmeow.Client, opts ...Option) *Client {
	cfg := resolveConfig(opts)
	c := &Client{
		wa: wa, log: cfg.log, diag: cfg.diag,
		audioRTP:      cfg.audioRTP,
		reorderWindow: cfg.reorderWindow,
		reorderReport: cfg.reorderReport,
		getGroupInfo:  wa.GetGroupInfo,
		ownGroupJIDs: func() []types.JID {
			return []types.JID{wa.Store.GetJID(), wa.Store.GetLID()}
		},
	}
	c.eng = newEngine(c)
	c.eng.install()
	return c
}

// Call places a 1:1 call to target (a phone number, a phone JID, or an @lid JID),
// returning the live Call once the offer is on the wire. Attach a Player and listeners
// to the returned Call; media starts automatically once the peer answers and the relay
// endpoint arrives.
func (c *Client) Call(ctx context.Context, target string) (*Call, error) {
	return c.CallWithOptions(ctx, target, CallOptions{})
}

// CallWithOptions places a 1:1 call with explicit media options.
func (c *Client) CallWithOptions(ctx context.Context, target string, opts CallOptions) (*Call, error) {
	return c.eng.placeCall(ctx, target, opts)
}

// GroupCall places an audio group call to at least two remote targets.
func (c *Client) GroupCall(ctx context.Context, targets ...string) (*Call, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L61-L107
	return c.GroupCallWithOptions(ctx, targets, GroupCallOptions{})
}

// GroupCallWithOptions places a group call with explicit media and group binding.
func (c *Client) GroupCallWithOptions(
	ctx context.Context,
	targets []string,
	opts GroupCallOptions,
) (*Call, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L61-L107
	return c.eng.placeGroupCall(ctx, targets, opts)
}

// GroupCallByID places an audio call to every remote member of a WhatsApp group.
// The groupID may be a bare numeric ID or a canonical @g.us JID.
func (c *Client) GroupCallByID(ctx context.Context, groupID string) (*Call, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/c52f804a98140e46516e91a9d2495f174f894a2a/datasheets/api-initial-group-call.md#L84-L115
	return c.GroupCallByIDWithOptions(ctx, groupID, GroupCallOptions{})
}

// GroupCallByIDWithOptions resolves a WhatsApp group roster and places one bound
// group call to all remote members with explicit media options.
func (c *Client) GroupCallByIDWithOptions(
	ctx context.Context,
	groupID string,
	opts GroupCallOptions,
) (*Call, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/c52f804a98140e46516e91a9d2495f174f894a2a/datasheets/api-initial-group-call.md#L84-L115
	groupJID, err := parseGroupCallID(groupID)
	if err != nil {
		return nil, err
	}
	if c.getGroupInfo == nil {
		return nil, errors.New("meowcaller: group roster lookup is unavailable")
	}
	info, err := c.getGroupInfo(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: get group info: %w", err)
	}
	if info == nil {
		return nil, errors.New("meowcaller: group roster lookup returned no group")
	}
	targets := remoteGroupCallTargets(info.Participants, c.groupSelfJIDs())
	if len(targets) < 2 {
		return nil, errors.New("meowcaller: group call by ID requires at least two remote members")
	}
	if len(targets) > 31 {
		return nil, fmt.Errorf(
			"meowcaller: group call by ID has %d remote members; at most 31 can be called",
			len(targets),
		)
	}
	opts.GroupJID = groupJID.String()
	return c.GroupCallWithOptions(ctx, targets, opts)
}

func parseGroupCallID(raw string) (types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/c52f804a98140e46516e91a9d2495f174f894a2a/datasheets/api-initial-group-call.md#L106-L115
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return types.EmptyJID, errors.New("meowcaller: group ID is required")
	}
	if !strings.ContainsRune(raw, '@') {
		raw += "@" + types.GroupServer
	}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.Server != types.GroupServer || jid.User == "" {
		return types.EmptyJID, errors.New("meowcaller: invalid group JID")
	}
	return jid.ToNonAD(), nil
}

func (c *Client) groupSelfJIDs() []types.JID {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/c52f804a98140e46516e91a9d2495f174f894a2a/datasheets/api-initial-group-call.md#L108-L115
	if c.ownGroupJIDs == nil {
		return nil
	}
	return c.ownGroupJIDs()
}

func remoteGroupCallTargets(
	participants []types.GroupParticipant,
	self []types.JID,
) []string {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/c52f804a98140e46516e91a9d2495f174f894a2a/datasheets/api-initial-group-call.md#L108-L115
	seen := make(map[types.JID]struct{}, len(participants)*3+len(self))
	for _, jid := range self {
		if jid = jid.ToNonAD(); !jid.IsEmpty() {
			seen[jid] = struct{}{}
		}
	}
	targets := make([]string, 0, len(participants))
	for _, participant := range participants {
		identities := []types.JID{
			participant.JID.ToNonAD(),
			participant.PhoneNumber.ToNonAD(),
			participant.LID.ToNonAD(),
		}
		duplicate := false
		for _, jid := range identities {
			if jid.IsEmpty() {
				continue
			}
			if _, exists := seen[jid]; exists {
				duplicate = true
				break
			}
		}
		for _, jid := range identities {
			if !jid.IsEmpty() {
				seen[jid] = struct{}{}
			}
		}
		if duplicate {
			continue
		}
		target := types.EmptyJID
		for _, jid := range identities {
			if !jid.IsEmpty() {
				target = jid
				break
			}
		}
		if target.IsEmpty() {
			continue
		}
		targets = append(targets, target.String())
	}
	return targets
}

// OnIncomingCall registers the listener fired for each inbound call offer. The handler
// receives a Call that has not been answered yet; call Answer or Reject on it. Only the
// most recently registered listener is used.
func (c *Client) OnIncomingCall(fn func(*Call)) {
	c.mu.Lock()
	c.onIncomingCall = fn
	c.mu.Unlock()
}

// incomingCallHandler returns the registered inbound-call listener, or nil.
func (c *Client) incomingCallHandler() func(*Call) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onIncomingCall
}
