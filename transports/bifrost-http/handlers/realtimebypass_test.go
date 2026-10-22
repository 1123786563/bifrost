package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * Bifrost 16.3 (#324), transports half: the bypass refusal every realtime establishment
 * asks (refuseRealtimeEphemeralTokenBypass) and the one place a provider credential could
 * reach a client (the client_secrets rewrite). What this file pins:
 *
 *   - a provider-shaped ephemeral token the gateway did not mint is REFUSED on every
 *     entrance (the entrance name only rides the audit detail), and recorded under
 *     unmapped_ephemeral_token — the provider stays authoritative upstream, but no session
 *     stands up here on the operator's key with the admission bypassed;
 *   - a gateway-minted token is single-use for establishment: the second entrance that
 *     presents it is refused and recorded under ephemeral_token_reuse, while the first
 *     establishment's claim is what the second one trips over;
 *   - a deployment that does not enforce inference auth keeps its chosen open posture —
 *     nothing is refused and nothing is recorded;
 *   - the minted client secret the client receives carries the gateway's ek_bf_* token and
 *     NOT the provider's — the one road a long-lived direct-upstream credential had to the
 *     client is the rewrite client_secrets already does, now pinned as the bypass bound.
 */

func bypassKV(t *testing.T) *kvstore.Store {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedMappedToken(t *testing.T, store *kvstore.Store, token string) {
	t.Helper()
	payload, err := json.Marshal(realtimeEphemeralKeyMapping{KeyID: "key_bypass", VirtualKey: "sk-bf-bypass"})
	require.NoError(t, err)
	require.NoError(t, store.SetWithTTL(buildRealtimeEphemeralKeyMappingKey(token), payload, time.Minute))
}

func TestRealtimeBypassUnmappedEphemeralTokenIsRefusedAndAudited(t *testing.T) {
	governance.ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(governance.ClearRealtimeBypassAttemptsForTest)
	store := bypassKV(t)

	// A raw provider ephemeral token: present in the bearer, absent from the mapping store.
	err := refuseRealtimeEphemeralTokenBypass(true, store, "ek_raw_provider_secret", false, "webrtc")
	require.NotNil(t, err, "an unmapped provider token cannot establish a realtime session")
	require.NotNil(t, err.StatusCode)
	assert.Equal(t, 401, *err.StatusCode)
	assert.Contains(t, err.Error.Message, "not minted by this gateway")

	attempts := governance.SnapshotRealtimeBypassAttempts()
	require.Len(t, attempts, 1)
	assert.Equal(t, governance.RealtimeBypassUnmappedEphemeralToken, attempts[0].Kind)
	assert.Contains(t, attempts[0].Detail, "webrtc")
	assert.NotContains(t, strings.Join([]string{attempts[0].Kind, attempts[0].Detail}, " "), "ek_raw_provider_secret",
		"the audit detail records the verdict, never the credential value")
}

func TestRealtimeBypassMintedTokenIsSingleUseForEstablishment(t *testing.T) {
	governance.ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(governance.ClearRealtimeBypassAttemptsForTest)
	store := bypassKV(t)
	seedMappedToken(t, store, "ek_bf_first_use")

	// The first establishment claims the token and proceeds.
	first := refuseRealtimeEphemeralTokenBypass(true, store, "ek_bf_first_use", true, "websocket")
	assert.Nil(t, first)

	// The second establishment with the SAME token is refused and audited — one minted
	// secret cannot stand two sessions (恢复走服务端 restore，不复用凭据).
	second := refuseRealtimeEphemeralTokenBypass(true, store, "ek_bf_first_use", true, "webrtc")
	require.NotNil(t, second)
	require.NotNil(t, second.StatusCode)
	assert.Equal(t, 401, *second.StatusCode)
	assert.Contains(t, second.Error.Message, "cannot establish a second")

	attempts := governance.SnapshotRealtimeBypassAttempts()
	require.Len(t, attempts, 1)
	assert.Equal(t, governance.RealtimeBypassEphemeralTokenReuse, attempts[0].Kind)
	assert.Contains(t, attempts[0].Detail, "already bound")
}

func TestRealtimeBypassOpenDeploymentPostureIsUnchanged(t *testing.T) {
	governance.ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(governance.ClearRealtimeBypassAttemptsForTest)
	store := bypassKV(t)

	// No inference auth: the operator's open posture keeps the provider authoritative —
	// exactly the behavior before this ticket — and books nothing.
	assert.Nil(t, refuseRealtimeEphemeralTokenBypass(false, store, "ek_raw_provider_secret", false, "webrtc"))
	assert.Nil(t, refuseRealtimeEphemeralTokenBypass(false, store, "ek_bf_any", true, "websocket"))
	assert.EqualValues(t, 0, governance.RealtimeBypassStatistics().Total)
}

func TestRealtimeBypassNonEphemeralCredentialsAreNotThisGate(t *testing.T) {
	governance.ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(governance.ClearRealtimeBypassAttemptsForTest)
	store := bypassKV(t)

	// Virtual keys and sk-bf-* tokens answer to the resolution gates, not this one.
	assert.Nil(t, refuseRealtimeEphemeralTokenBypass(true, store, "sk-bf-ordinary", false, "websocket"))
	assert.Nil(t, refuseRealtimeEphemeralTokenBypass(true, store, "", false, "websocket"))
	assert.EqualValues(t, 0, governance.RealtimeBypassStatistics().Total)
}

func TestRealtimeBypassMintedClientSecretCarriesNoProviderToken(t *testing.T) {
	governance.ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(governance.ClearRealtimeBypassAttemptsForTest)
	store := bypassKV(t)

	// The provider's answer carries its own ephemeral token under client_secret.value; the
	// rewrite replaces it with the gateway's ek_bf_* before the client ever sees the body.
	providerAnswer := `{"client_secret":{"value":"provider_raw_ephemeral_token","expires_at":9999999999},"model":"gpt-realtime"}`
	response := &schemas.BifrostPassthroughResponse{Body: []byte(providerAnswer)}
	bifrostErr := replaceAndCacheRealtimeEphemeralToken(store, response, "key_bypass", "sk-bf-bypass")
	require.Nil(t, bifrostErr)

	body := string(response.Body)
	assert.Contains(t, body, "ek_bf_", "the client receives the gateway-minted token")
	assert.NotContains(t, body, "provider_raw_ephemeral_token",
		"the provider's own credential never reaches the client — the direct-upstream road stays closed")
}
