package meowcaller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/polymorfa/hypermeow/guest"
	"github.com/polymorfa/hypermeow/proto/waWa6"
	"github.com/polymorfa/hypermeow/types"
)

func TestNewGuestClientRegistersForTheLink(t *testing.T) {
	var invite, name string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invite, name = r.URL.Query().Get("invite_code"), r.URL.Query().Get("guest_name")
		_, _ = w.Write([]byte(`{"status":"ok","lid":"987654321"}`))
	}))
	defer server.Close()

	g, err := NewGuestClient(context.Background(), "https://call.whatsapp.com/video/LINKTOKEN",
		GuestOptions{Name: "Ada", Registration: guest.Request{BaseURL: server.URL, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatalf("NewGuestClient: %v", err)
	}
	if invite != "LINKTOKEN" || name != "Ada" {
		t.Fatalf("registered invite=%q name=%q", invite, name)
	}
	want := types.NewJID("987654321", types.HiddenUserServer)
	if g.Registration.LID != want || g.WA.Store.ID == nil || *g.WA.Store.ID != want || g.token != "LINKTOKEN" {
		t.Fatalf("guest identity = %+v device=%v token=%q", g.Registration, g.WA.Store.ID, g.token)
	}
	payload := g.WA.GetClientPayload()
	if payload.GetAccountType() != waWa6.ClientPayload_GUEST || payload.GetUsername() != 987654321 {
		t.Fatalf("payload = %v", payload)
	}
	if g.Client == nil || g.Client.eng == nil {
		t.Fatal("meowcaller client was not created")
	}
}

func TestNewGuestClientRejectsBlankNameAndBadLink(t *testing.T) {
	if _, err := NewGuestClient(context.Background(), "TOKEN", GuestOptions{Name: " "}); err == nil {
		t.Fatal("a blank name must be rejected")
	}
	if _, err := NewGuestClient(context.Background(), "", GuestOptions{Name: "Ada"}); err == nil {
		t.Fatal("an empty link must be rejected")
	}
}
