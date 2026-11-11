package signaling

import (
	"fmt"
	"strconv"
	"strings"

	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/types"
)

// CallLinkMedia is the media mode encoded in a call link.
type CallLinkMedia string

const (
	// CallLinkMediaAudio creates or joins an audio call.
	CallLinkMediaAudio CallLinkMedia = "audio"
	// CallLinkMediaVideo creates or joins a video call.
	CallLinkMediaVideo CallLinkMedia = "video"
)

// CallLink is a reusable call-link token.
type CallLink struct {
	Token string
	Media CallLinkMedia
}

// CallLinkPreview is the metadata returned before joining a link.
type CallLinkPreview struct {
	Token              string
	Media              CallLinkMedia
	Creator            types.JID
	CreatorPN          types.JID
	WaitingRoomEnabled bool
	IsAdmin            bool
}

// CallLinkJoin is the admitted or waiting result of joining a link.
type CallLinkJoin struct {
	Token              string
	Media              CallLinkMedia
	CallID             string
	CallCreator        types.JID
	WaitingRoomEnabled bool
	InWaitingRoom      bool
	IsAdmin            bool
	Group              *GroupCallUpdate
}

// WaitingRoom is one authoritative call-link waiting-room snapshot.
type WaitingRoom struct {
	CallID        string
	CallCreator   types.JID
	LinkToken     string
	Media         CallLinkMedia
	Enabled       bool
	IsAdmin       bool
	TransactionID uint32
	Users         []WaitingRoomUser
}

// WaitingRoomUser is one user in a waiting-room snapshot.
type WaitingRoomUser struct {
	JID   types.JID
	PN    types.JID
	State string
	// AccountKind is the raw account_kind attribute; AccountKindGuest for a
	// call-link guest.
	AccountKind string
	// Name is the user's push name, or the name a guest typed.
	Name string
}

// IsGuest reports whether the waiting user has no WhatsApp account.
func (u WaitingRoomUser) IsGuest() bool { return u.AccountKind == AccountKindGuest }

// BuildCallLinkCreate builds the service request that creates a call link.
func BuildCallLinkCreate(media CallLinkMedia, requestID string) (waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L32-L41
	if err := validateCallLinkMedia(media); err != nil {
		return waBinary.Node{}, err
	}
	return buildCallServiceRequest(requestID, waBinary.Node{Tag: "link_create", Attrs: waBinary.Attrs{"media": string(media)}})
}

// BuildCallLinkQuery builds the service request that previews a call link.
func BuildCallLinkQuery(token string, media CallLinkMedia, requestID string) (waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L43-L58
	if strings.TrimSpace(token) == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: call-link token is required")
	}
	if err := validateCallLinkMedia(media); err != nil {
		return waBinary.Node{}, err
	}
	return buildCallServiceRequest(requestID, waBinary.Node{
		Tag: "link_query", Attrs: waBinary.Attrs{"token": token, "media": string(media)},
	})
}

// CallLinkJoinOptions are optional link_join attributes.
type CallLinkJoinOptions struct {
	// PushName is the display name other participants see. A call-link guest
	// has no profile name, so it must send one; a paired account may omit it.
	//
	// Unverified in WhatsApp Web JS: link_join is built by the native VoIP
	// module, which the stored bundles do not index. The attribute name
	// matches the roster's push_name (WAWebHandleVoipCall.js:52-63).
	PushName string
}

// BuildCallLinkJoin builds the media-capability request that joins a link.
func BuildCallLinkJoin(token string, media CallLinkMedia, requestID string) (waBinary.Node, error) {
	return BuildCallLinkJoinWithOptions(token, media, requestID, CallLinkJoinOptions{})
}

// BuildCallLinkJoinWithOptions builds a link_join request with optional attributes.
func BuildCallLinkJoinWithOptions(
	token string,
	media CallLinkMedia,
	requestID string,
	opts CallLinkJoinOptions,
) (waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L60-L94
	if strings.TrimSpace(token) == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: call-link token is required")
	}
	if err := validateCallLinkMedia(media); err != nil {
		return waBinary.Node{}, err
	}
	children := []waBinary.Node{audioOpus("16000")}
	if media == CallLinkMediaVideo {
		children = append(children, waBinary.Node{
			Tag: "video", Attrs: waBinary.Attrs{"dec": "H264", "device_orientation": "0"},
		})
	}
	children = append(children,
		waBinary.Node{Tag: "net", Attrs: waBinary.Attrs{"medium": "2"}},
		waBinary.Node{Tag: "capability", Attrs: waBinary.Attrs{"ver": "1"}, Content: append([]byte(nil), CapabilityOffer...)},
	)
	attrs := waBinary.Attrs{"token": token, "media": string(media)}
	if name := strings.TrimSpace(opts.PushName); name != "" {
		attrs["push_name"] = name
	}
	return buildCallServiceRequest(requestID, waBinary.Node{
		Tag: "link_join", Attrs: attrs, Content: children,
	})
}

func validateCallLinkMedia(media CallLinkMedia) error {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L12-L19
	if media != CallLinkMediaAudio && media != CallLinkMediaVideo {
		return fmt.Errorf("signaling: invalid call-link media %q", media)
	}
	return nil
}

func buildCallServiceRequest(requestID string, action waBinary.Node) (waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L21-L30
	if requestID == "" {
		return waBinary.Node{}, fmt.Errorf("signaling: call-link request ID is required")
	}
	return waBinary.Node{
		Tag: "call", Attrs: waBinary.Attrs{"to": types.NewJID("", "call"), "id": requestID},
		Content: []waBinary.Node{action},
	}, nil
}

// BuildWaitingRoomToggle changes approval requirements for an active link call.
func BuildWaitingRoomToggle(callID string, creator types.JID, enabled bool, requestID string) waBinary.Node {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L124-L143
	value := "0"
	if enabled {
		value = "1"
	}
	return buildWaitingRoomRequest("waiting_room_toggle", callID, creator, requestID, waBinary.Attrs{"enabled": value}, nil)
}

// BuildWaitingRoomHeartbeat keeps a pending link join alive.
func BuildWaitingRoomHeartbeat(callID string, creator types.JID, requestID string) waBinary.Node {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L145-L155
	return buildWaitingRoomRequest("heartbeat", callID, creator, requestID, waBinary.Attrs{"type": "waiting_room"}, nil)
}

// BuildWaitingRoomAdmit admits one pending user.
func BuildWaitingRoomAdmit(callID string, creator, user types.JID, requestID string) waBinary.Node {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L157-L171
	return buildWaitingRoomRequest("waiting_room_admit", callID, creator, requestID, nil,
		[]waBinary.Node{{Tag: "user", Attrs: waBinary.Attrs{"jid": user}}})
}

// BuildWaitingRoomDeny denies one pending user.
func BuildWaitingRoomDeny(callID string, creator, user types.JID, requestID string) waBinary.Node {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L173-L187
	return buildWaitingRoomRequest("waiting_room_deny", callID, creator, requestID, nil,
		[]waBinary.Node{{Tag: "user", Attrs: waBinary.Attrs{"jid": user}}})
}

func buildWaitingRoomRequest(
	tag, callID string,
	creator types.JID,
	requestID string,
	extraAttrs waBinary.Attrs,
	children []waBinary.Node,
) waBinary.Node {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L96-L122
	attrs := waBinary.Attrs{"call-id": callID, "call-creator": creator}
	for key, value := range extraAttrs {
		attrs[key] = value
	}
	return waBinary.Node{
		Tag: "call", Attrs: waBinary.Attrs{"to": types.NewJID(callID, "call"), "id": requestID},
		Content: []waBinary.Node{{Tag: tag, Attrs: attrs, Content: children}},
	}
}

// ParseCallLinkCreateAck parses a link_create ACK.
func ParseCallLinkCreateAck(node *waBinary.Node) (*CallLink, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L230-L252
	child, err := validateCallLinkAck(node, "link_create")
	if err != nil {
		return nil, err
	}
	attrs := child.AttrGetter()
	media, err := parseCallLinkMedia(attrs.String("media"))
	if err != nil {
		return nil, err
	}
	link := &CallLink{Token: attrs.String("token"), Media: media}
	if err = attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse link_create ACK: %w", err)
	}
	if link.Token == "" {
		return nil, fmt.Errorf("signaling: parse link_create ACK: missing token")
	}
	return link, nil
}

// ParseCallLinkQueryAck parses a link_query ACK.
func ParseCallLinkQueryAck(node *waBinary.Node) (*CallLinkPreview, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L254-L285
	child, err := validateCallLinkAck(node, "link_query")
	if err != nil {
		return nil, err
	}
	attrs := child.AttrGetter()
	media, err := parseCallLinkMedia(attrs.String("media"))
	if err != nil {
		return nil, err
	}
	preview := &CallLinkPreview{
		Token: attrs.String("token"), Media: media, Creator: attrs.JID("link_creator"),
		CreatorPN: attrs.OptionalJIDOrEmpty("link_creator_pn"),
	}
	if err = attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse link_query ACK: %w", err)
	}
	waitingRoom, ok := child.GetOptionalChildByTag("waiting_room")
	if !ok {
		return nil, fmt.Errorf("signaling: parse link_query ACK: missing waiting_room")
	}
	wrAttrs := waitingRoom.AttrGetter()
	preview.WaitingRoomEnabled = wrAttrs.OptionalString("enabled") == "1"
	preview.IsAdmin = wrAttrs.OptionalString("is_admin") == "1"
	return preview, nil
}

// ParseCallLinkJoinAck parses either direct admission or waiting-room state.
func ParseCallLinkJoinAck(node *waBinary.Node) (*CallLinkJoin, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L332-L373
	if err := validateCallLinkAckEnvelope(node, "link_join"); err != nil {
		return nil, err
	}
	var result CallLinkJoin
	waitingNode, hasWaiting := node.GetOptionalChildByTag("waiting_room")
	if hasWaiting {
		waiting, err := parseWaitingRoomNode(&waitingNode)
		if err != nil {
			return nil, err
		}
		result.Token, result.Media = waiting.LinkToken, waiting.Media
		result.CallID, result.CallCreator = waiting.CallID, waiting.CallCreator
		result.WaitingRoomEnabled, result.IsAdmin = waiting.Enabled, waiting.IsAdmin
	}
	group, hasGroup, err := ParseInitialGroupCallAck(node)
	if err != nil {
		return nil, err
	}
	if hasGroup {
		result.Group = group
		if result.CallID == "" {
			result.CallID, result.CallCreator = group.CallID, group.CallCreator
			result.Media = CallLinkMedia(group.Media)
		} else if result.CallID != group.CallID || result.CallCreator != group.CallCreator {
			return nil, fmt.Errorf("signaling: parse link_join ACK: identity mismatch")
		}
	}
	if !hasWaiting && !hasGroup {
		return nil, fmt.Errorf("signaling: parse link_join ACK: missing group_info and waiting_room")
	}
	result.InWaitingRoom = hasWaiting && result.WaitingRoomEnabled && !hasGroup
	return &result, nil
}

// ParseWaitingRoomUpdate parses one authoritative waiting-room update action.
func ParseWaitingRoomUpdate(node *waBinary.Node) (*WaitingRoom, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L375-L398
	if node == nil || node.Tag != "waiting_room_update" {
		return nil, fmt.Errorf("signaling: parse waiting room update: unexpected node")
	}
	attrs := node.AttrGetter()
	callID, creator := attrs.String("call-id"), attrs.JID("call-creator")
	if err := attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse waiting room update identity: %w", err)
	}
	child, ok := node.GetOptionalChildByTag("waiting_room")
	if !ok {
		return nil, fmt.Errorf("signaling: parse waiting room update: missing waiting_room")
	}
	room, err := parseWaitingRoomNode(&child)
	if err != nil {
		return nil, err
	}
	if room.CallID != callID || room.CallCreator != creator {
		return nil, fmt.Errorf("signaling: parse waiting room update: identity mismatch")
	}
	return room, nil
}

// BuildWaitingRoomUpdateAck builds the typed ACK required by a waiting-room update.
func BuildWaitingRoomUpdateAck(node *waBinary.Node) (waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L400-L424
	if node == nil || node.Tag != "call" {
		return waBinary.Node{}, fmt.Errorf("signaling: build waiting room ACK: unexpected node")
	}
	children := node.GetChildren()
	if len(children) != 1 || children[0].Tag != "waiting_room_update" {
		return waBinary.Node{}, fmt.Errorf("signaling: build waiting room ACK: missing update")
	}
	attrs := node.AttrGetter()
	to, id := attrs.JID("from"), attrs.String("id")
	if err := attrs.Error(); err != nil {
		return waBinary.Node{}, fmt.Errorf("signaling: build waiting room ACK: %w", err)
	}
	return waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{
		"to": to, "id": id, "class": "call", "type": "waiting_room_update",
	}}, nil
}

func parseWaitingRoomNode(node *waBinary.Node) (*WaitingRoom, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L287-L330
	if node == nil || node.Tag != "waiting_room" {
		return nil, fmt.Errorf("signaling: parse waiting room: unexpected node")
	}
	attrs := node.AttrGetter()
	media, err := parseCallLinkMedia(attrs.String("media"))
	if err != nil {
		return nil, err
	}
	room := &WaitingRoom{
		CallID: attrs.String("call-id"), CallCreator: attrs.JID("call-creator"),
		LinkToken: attrs.String("link-token"), Media: media,
		Enabled: attrs.OptionalString("enabled") == "1", IsAdmin: attrs.OptionalString("is_admin") == "1",
	}
	if raw := attrs.OptionalString("transaction-id"); raw != "" {
		value, parseErr := strconv.ParseUint(raw, 10, 32)
		if parseErr != nil {
			return nil, fmt.Errorf("signaling: parse waiting room transaction ID: %w", parseErr)
		}
		room.TransactionID = uint32(value)
	}
	if err = attrs.Error(); err != nil {
		return nil, fmt.Errorf("signaling: parse waiting room: %w", err)
	}
	for _, child := range node.GetChildren() {
		if child.Tag != "user" {
			continue
		}
		userAttrs := child.AttrGetter()
		user := WaitingRoomUser{
			JID: userAttrs.JID("jid"), PN: userAttrs.OptionalJIDOrEmpty("user_pn"),
			State:       userAttrs.String("state"),
			AccountKind: userAttrs.OptionalString("account_kind"), Name: participantName(userAttrs),
		}
		if err = userAttrs.Error(); err != nil {
			return nil, fmt.Errorf("signaling: parse waiting room user: %w", err)
		}
		room.Users = append(room.Users, user)
	}
	return room, nil
}

func validateCallLinkAckEnvelope(node *waBinary.Node, expectedType string) error {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L189-L205
	if node == nil {
		return fmt.Errorf("signaling: parse %s ACK: nil node", expectedType)
	}
	attrs := node.AttrGetter()
	if node.Tag != "ack" || attrs.String("class") != "call" || attrs.String("type") != expectedType {
		return fmt.Errorf("signaling: parse %s ACK: unexpected envelope", expectedType)
	}
	return nil
}

func validateCallLinkAck(node *waBinary.Node, expectedType string) (*waBinary.Node, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L207-L216
	if err := validateCallLinkAckEnvelope(node, expectedType); err != nil {
		return nil, err
	}
	child, ok := node.GetOptionalChildByTag(expectedType)
	if !ok {
		return nil, fmt.Errorf("signaling: parse %s ACK: missing payload", expectedType)
	}
	return &child, nil
}

func parseCallLinkMedia(value string) (CallLinkMedia, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L218-L224
	media := CallLinkMedia(value)
	if err := validateCallLinkMedia(media); err != nil {
		return "", err
	}
	return media, nil
}
