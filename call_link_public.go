package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/polymorfa/hypermeow/types"
)

// CallLinkOptions selects an audio or video call link.
type CallLinkOptions struct {
	Video bool
}

// CallLink is one reusable public call-link token and URL.
type CallLink struct {
	Token string
	URL   string
	Video bool
}

// CallLinkPreview is sanitized link metadata returned before joining.
type CallLinkPreview struct {
	Token              string
	Video              bool
	ApprovalRequired   bool
	IsAdmin            bool
	Creator            types.JID
	CreatorPhoneNumber types.JID
}

// CreateCallLink creates a reusable audio or video call link.
func (c *Client) CreateCallLink(ctx context.Context, opts CallLinkOptions) (CallLink, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L41-L50
	if c == nil || c.eng == nil {
		return CallLink{}, errors.New("meowcaller: call-link signaling is unavailable")
	}
	link, err := c.eng.createPublicCallLink(ctx, selectedCallLinkMedia(opts))
	if err != nil {
		return CallLink{}, fmt.Errorf("meowcaller: create call link: %w", err)
	}
	if link == nil || link.Token == "" {
		return CallLink{}, errors.New("meowcaller: create call link returned no token")
	}
	return publicCallLink(link.Token, link.Media), nil
}

// PreviewCallLink queries call-link metadata without joining.
func (c *Client) PreviewCallLink(
	ctx context.Context,
	tokenOrURL string,
	opts CallLinkOptions,
) (CallLinkPreview, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L52-L74
	if c == nil || c.eng == nil {
		return CallLinkPreview{}, errors.New("meowcaller: call-link signaling is unavailable")
	}
	token, err := normalizeCallLinkToken(tokenOrURL)
	if err != nil {
		return CallLinkPreview{}, err
	}
	preview, err := c.eng.previewPublicCallLink(ctx, token, selectedCallLinkMedia(opts))
	if err != nil {
		return CallLinkPreview{}, fmt.Errorf("meowcaller: preview call link: %w", err)
	}
	if preview == nil {
		return CallLinkPreview{}, errors.New("meowcaller: preview call link returned no result")
	}
	return CallLinkPreview{
		Token:              preview.Token,
		Video:              preview.Media == callLinkMediaVideo,
		ApprovalRequired:   preview.WaitingRoomEnabled,
		IsAdmin:            preview.IsAdmin,
		Creator:            preview.Creator,
		CreatorPhoneNumber: preview.CreatorPN,
	}, nil
}

// JoinCallLink joins an active call-link session or enters its waiting room.
func (c *Client) JoinCallLink(
	ctx context.Context,
	tokenOrURL string,
	opts CallLinkOptions,
) (*Call, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L76-L155
	if c == nil || c.eng == nil {
		return nil, errors.New("meowcaller: call-link signaling is unavailable")
	}
	token, err := normalizeCallLinkToken(tokenOrURL)
	if err != nil {
		return nil, err
	}
	return c.eng.joinPublicCallLink(ctx, token, opts)
}

func waitingRoomStateFromJoin(join *callLinkJoinResult) WaitingRoomState {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L157-L197
	if join == nil {
		return WaitingRoomState{}
	}
	return WaitingRoomState{
		Enabled:       join.WaitingRoomEnabled,
		IsAdmin:       join.IsAdmin,
		InWaitingRoom: join.InWaitingRoom,
	}
}

func selectedCallLinkMedia(opts CallLinkOptions) callLinkMedia {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/voip/call_link.go#L12-L18
	if opts.Video {
		return callLinkMediaVideo
	}
	return callLinkMediaAudio
}

// callLinkAudioPath is the path WhatsApp serves audio call links under; /audio/ returns 404.
const callLinkAudioPath = "voice"

func publicCallLink(token string, media callLinkMedia) CallLink {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/types/call.go#L26-L38
	path := string(media)
	if media == callLinkMediaAudio {
		path = callLinkAudioPath
	}
	return CallLink{
		Token: token,
		URL:   fmt.Sprintf("https://call.whatsapp.com/%s/%s", path, token),
		Video: media == callLinkMediaVideo,
	}
}

func normalizeCallLinkToken(raw string) (string, error) {
	// Source of truth: https://github.com/tulir/whatsmeow/blob/3775fbadf88fdf44ada62ae5c5db5d7cc6f26259/call_link.go#L52-L81
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("meowcaller: call-link token is required")
	}
	if !strings.Contains(raw, "://") {
		if strings.ContainsRune(raw, '/') {
			return "", errors.New("meowcaller: invalid call-link token")
		}
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "call.whatsapp.com" {
		return "", errors.New("meowcaller: invalid WhatsApp call-link URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 ||
		(parts[0] != callLinkAudioPath && parts[0] != string(callLinkMediaAudio) &&
			parts[0] != string(callLinkMediaVideo)) ||
		parts[1] == "" {
		return "", errors.New("meowcaller: invalid WhatsApp call-link URL")
	}
	return parts[1], nil
}
