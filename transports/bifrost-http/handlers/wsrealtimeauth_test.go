package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/valyala/fasthttp"
	"strings"
)

// TestRealtimeNativeSubprotocolAdmission exercises HTTP admission before model resolution using synthetic credentials, not a live voice session.
//
// 16.3 (#324) changed one posture these cases used to pin: a provider-shaped ek_* token the
// gateway did not mint is no longer passed upstream on an enforcing deployment — it is the
// 旁路 the ticket closes, refused 401 and audited. The cases that intend a USABLE credential
// now seed the gateway's own mapping for it (an ek_bf_* the minting path issued).
func TestRealtimeNativeSubprotocolAdmission(t *testing.T) {
	SetLogger(&mockLogger{})
	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedBypassTestMapping(t, store, "ek_bf_synthetic")
	seedBypassTestMapping(t, store, "ek_bf_same")
	for _, tc := range []struct {
		name, protocol string
		status         int
	}{
		{"xai mapped", "xai-client-secret.ek_bf_synthetic", 101},
		// An unmapped provider token is refused on an enforcing deployment (16.3, #324):
		// the provider stays authoritative upstream, but no session stands up here.
		{"openai unmapped refused", "realtime, openai-insecure-api-key.ek_synthetic", 401},
		{"unknown", "future-protocol", 401},
		{"empty xai", "xai-client-secret.", 400},
		{"conflicting", "xai-client-secret.ek_first, openai-insecure-api-key.ek_second", 400},
		{"duplicate same", "xai-client-secret.ek_bf_same, xai-client-secret.ek_bf_same", 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newRealtimeUpgradeRequest("/v1/realtime")
			ctx.Request.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
			handler := newRealtimeUpgradeHandler(true)
			handler.config.KVStore = store
			handler.handleUpgrade(ctx)
			if ctx.Response.StatusCode() != tc.status {
				t.Fatalf("status %d, want %d", ctx.Response.StatusCode(), tc.status)
			}
		})
	}
}

// seedBypassTestMapping registers a gateway-minted ephemeral token's mapping, the shape the
// client_secrets minting path leaves behind.
func seedBypassTestMapping(t *testing.T, store *kvstore.Store, token string) {
	t.Helper()
	payload, err := json.Marshal(realtimeEphemeralKeyMapping{KeyID: "key_" + token, VirtualKey: "sk-bf-" + token})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWithTTL(buildRealtimeEphemeralKeyMappingKey(token), payload, time.Minute); err != nil {
		t.Fatal(err)
	}
}

// TestRealtimeInvalidNativeCredentialsRejectOpenDeployment prevents malformed credentials falling back to an operator key when authentication is optional.
func TestRealtimeInvalidNativeCredentialsRejectOpenDeployment(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := newRealtimeUpgradeRequest("/v1/realtime")
	ctx.Request.Header.Set("Sec-WebSocket-Protocol", "xai-client-secret.")
	newRealtimeUpgradeHandler(false).handleUpgrade(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatal("invalid credential was treated as anonymous")
	}
}

// TestRealtimeNativeExplicitAuthorizationWins preserves the existing explicit-header precedence over browser credential protocols.
// The explicit token is a gateway-minted, mapped one (16.3, #324): the precedence being
// pinned is the header's, and the token itself must be one the gateway issued.
func TestRealtimeNativeExplicitAuthorizationWins(t *testing.T) {
	SetLogger(&mockLogger{})
	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedBypassTestMapping(t, store, "ek_bf_explicit")
	ctx := newRealtimeUpgradeRequest("/v1/realtime")
	ctx.Request.Header.Set("Authorization", "Bearer ek_bf_explicit")
	ctx.Request.Header.Set("Sec-WebSocket-Protocol", "xai-client-secret.")
	handler := newRealtimeUpgradeHandler(true)
	handler.config.KVStore = store
	handler.handleUpgrade(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusSwitchingProtocols {
		t.Fatal("explicit authentication precedence changed")
	}
}

// TestRealtimeNativeCredentialParser preserves opaque token bytes and never exposes credentials in errors.
func TestRealtimeNativeCredentialParser(t *testing.T) {
	for _, tc := range []struct {
		protocol, want string
		invalid        bool
	}{
		{"  realtime , xai-client-secret.ek_opaque-ABC_123  ", "ek_opaque-ABC_123", false},
		{"unknown.ek_synthetic", "", false},
		{"xai-client-secret.ek_secret space", "", true},
		{"openai-insecure-api-key.ek_secret, xai-client-secret.ek_different", "", true},
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
		token, err := extractRealtimeSubprotocolAPIKey(ctx)
		if (err != nil) != tc.invalid || token != tc.want {
			t.Fatal("unexpected credential parse result")
		}
		if err != nil && strings.Contains(err.Error(), "ek_") {
			t.Fatal("credential included in error")
		}
	}
}
