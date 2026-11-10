package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/guest"
	waLog "github.com/polymorfa/hypermeow/util/log"
)

// GuestOptions configures a call-link guest.
type GuestOptions struct {
	// Name is the display name other participants see. Required.
	Name string
	// Registration overrides guest registration fields such as BaseURL,
	// Browser or HTTPClient. InviteCode and Name are filled in from the link
	// and Name above.
	Registration guest.Request
	// Log is the HyperMeow client logger. waLog.Noop when nil.
	Log waLog.Logger
}

// GuestClient is a meowcaller client logged in as a one-time call-link guest.
type GuestClient struct {
	*Client
	// WA is the underlying HyperMeow client. Connect it before joining.
	WA *whatsmeow.Client
	// Registration is the guest identity the server assigned.
	Registration *guest.Registration

	token string
	name  string
}

// NewGuestClient registers a one-time guest identity for one call link and
// returns a client that logs in as that guest. The identity's keys and Signal
// state live only in memory (hypermeow guest.NewDevice) and are not reusable.
//
// Call Connect on the result, then JoinCallLink. A guest can only join the
// link it registered for.
func NewGuestClient(
	ctx context.Context,
	tokenOrURL string,
	opts GuestOptions,
	clientOpts ...Option,
) (*GuestClient, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return nil, errors.New("meowcaller: guest name is required")
	}
	token, err := normalizeCallLinkToken(tokenOrURL)
	if err != nil {
		return nil, err
	}
	logger := opts.Log
	if logger == nil {
		logger = waLog.Noop
	}
	device := guest.NewDevice(logger)
	req := opts.Registration
	req.InviteCode, req.Name = token, name
	reg, err := guest.Register(ctx, device, req)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: register guest: %w", err)
	}
	wa, err := guest.NewClient(device, reg, logger)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: guest client: %w", err)
	}
	return &GuestClient{
		Client: NewClient(wa, clientOpts...), WA: wa, Registration: reg,
		token: token, name: name,
	}, nil
}

// Connect opens the guest's WhatsApp connection.
func (g *GuestClient) Connect() error {
	return g.WA.Connect()
}

// JoinCallLink joins the link this guest registered for, showing its
// registered name. opts.DisplayName defaults to that name.
func (g *GuestClient) JoinCallLink(ctx context.Context, opts CallLinkOptions) (*Call, error) {
	if opts.DisplayName == "" {
		opts.DisplayName = g.name
	}
	return g.Client.JoinCallLink(ctx, g.token, opts)
}

// Close disconnects the guest. The identity isn't reusable afterwards.
func (g *GuestClient) Close() {
	g.WA.Disconnect()
}
