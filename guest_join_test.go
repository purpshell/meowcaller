package meowcaller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/polymorfa/hypermeow/guest"
	"github.com/polymorfa/hypermeow/proto/waWa6"
	"github.com/polymorfa/hypermeow/store"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/util/keys"
)

func freshGuestDevice() *store.Device {
	device := &store.Device{
		NoiseKey: keys.NewKeyPair(), IdentityKey: keys.NewKeyPair(), RegistrationID: 42,
	}
	device.SignedPreKey = device.IdentityKey.CreateSignedPreKey(1)
	return device
}

func TestNewGuestClientRegistersForTheLink(t *testing.T) {
	var invite, name string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invite, name = r.URL.Query().Get("invite_code"), r.URL.Query().Get("guest_name")
		_, _ = w.Write([]byte(`{"status":"ok","lid":"987654321"}`))
	}))
	defer server.Close()

	device := freshGuestDevice()
	g, err := NewGuestClient(context.Background(), device, "https://call.whatsapp.com/video/LINKTOKEN",
		GuestOptions{Name: "Ada", Registration: guest.Request{BaseURL: server.URL, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatalf("NewGuestClient: %v", err)
	}
	if invite != "LINKTOKEN" || name != "Ada" {
		t.Fatalf("registered invite=%q name=%q", invite, name)
	}
	want := types.NewJID("987654321", types.HiddenUserServer)
	if g.Registration.LID != want || device.ID == nil || *device.ID != want || g.token != "LINKTOKEN" {
		t.Fatalf("guest identity = %+v device=%v token=%q", g.Registration, device.ID, g.token)
	}
	payload := g.WA.GetClientPayload()
	if payload.GetAccountType() != waWa6.ClientPayload_GUEST || payload.GetUsername() != 987654321 {
		t.Fatalf("payload = %v", payload)
	}
	if g.Client == nil || g.Client.eng == nil {
		t.Fatal("meowcaller client was not created")
	}
}

func TestNewGuestClientRejectsPairedDeviceAndBlankName(t *testing.T) {
	paired := freshGuestDevice()
	id := types.NewJID("1", types.DefaultUserServer)
	paired.ID = &id
	if _, err := NewGuestClient(context.Background(), paired, "TOKEN", GuestOptions{Name: "Ada"}); err == nil {
		t.Fatal("a paired device must be rejected")
	}
	if _, err := NewGuestClient(context.Background(), freshGuestDevice(), "TOKEN", GuestOptions{Name: " "}); err == nil {
		t.Fatal("a blank name must be rejected")
	}
}
