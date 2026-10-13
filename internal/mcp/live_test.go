package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// TestLiveMailDiscovery is opt-in: it adds the real endpoint to an isolated registry,
// resolves live OAuth metadata and prints non-secret diagnostics. It never logs in,
// opens a browser or calls a real tool.
func TestLiveMailDiscovery(t *testing.T) {
	if os.Getenv("OROLE_MCP_LIVE") != "1" {
		t.Skip("set OROLE_MCP_LIVE=1 to probe live public OAuth metadata")
	}
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server, err := h.m.Add(ctx, AddParams{Name: "Orole Mail", URL: "https://mail.orole.be/api/mcp", Adapter: AdapterMail, AuthKind: AuthOAuth})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := h.m.DiscoverAuth(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.AS == nil || discovery.AS.AuthorizationEndpoint != "https://mail.orole.be/api/auth/oauth2/authorize" || discovery.AS.TokenEndpoint != "https://mail.orole.be/api/auth/oauth2/token" {
		t.Fatalf("unexpected discovery: %+v", discovery)
	}
	diag, err := h.m.Diagnostics(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(diag, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Live diagnostics (no login):\n%s", raw)
	if discovery.AS.RegistrationEndpoint == "" && !discovery.AS.ClientIDMetadataDocumentOK {
		_, err = h.m.StartAuth(ctx, server.ID)
		if !errors.Is(err, ErrNoRegistration) {
			t.Fatalf("expected explicit client registration blocker, got %v", err)
		}
		t.Logf("Stopped before user authorization: %v", err)
	}
	if len(h.creds.Keys()) != 0 {
		t.Fatal("metadata-only smoke unexpectedly stored credentials")
	}
}
