package handlers

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
)

// realtimeAuthRefusalMessage is what an anonymous realtime caller is told. It names the two
// credentials that work, because the realtime flow has a second one (the ephemeral client
// secret) that the generic inference message does not mention.
const realtimeAuthRefusalMessage = "authentication is required for realtime connections. Provide a virtual key (x-bf-vk or an sk-bf- bearer token), or an ephemeral client secret minted by POST /v1/realtime/client_secrets."

// refuseUnauthenticatedRealtime reports whether a realtime connection must be refused before it
// is established, and is the admission gate for every realtime transport (WebSocket upgrade and
// both WebRTC SDP routes).
//
// Realtime needs this gate because it is the one inference surface where accepting the
// connection already spends something: both transports select the operator's provider key and
// open an upstream session before any turn exists. The per-turn pipeline
// (RunRealtimeTurnPreHooks -> governance PreLLMHook) is the authoritative check for access and
// limits and keeps that job, but it first runs a turn too late to stop an anonymous client from
// standing up a session on the operator's key. Nothing in the transport middleware covers the
// gap either: inference auth is delegated wholesale to governance, whose refusal arrives with
// the first turn.
//
// The question asked here is deliberately the narrow one - "was any credential presented?" -
// answered by governance's own exported predicate rather than a second implementation of it, so
// admission cannot drift from the funnel's first step. It settles no limits, so it cannot
// double-count usage against the turns that follow.
//
// Any ephemeral client secret counts as a credential. Bifrost-minted tokens resolve through the
// shared mapping after admission. Unmapped tokens remain opaque provider credentials and are sent
// upstream so the provider remains authoritative about whether they are valid.
//
// Returns nil when the connection may proceed.
func refuseUnauthenticatedRealtime(
	enforceAuthOnInference bool,
	bifrostCtx *schemas.BifrostContext,
	authorizationHeader string,
) *schemas.BifrostError {
	// The operator has not asked for authentication on inference, so realtime is open for the
	// same reason every other inference route is. Closing it here regardless would make realtime
	// stricter than /v1/chat/completions on the same deployment.
	if !enforceAuthOnInference {
		return nil
	}
	if governance.PresentedAnyCredential(bifrostCtx) {
		return nil
	}
	if token := extractRealtimeBearerTokenFromHeader(authorizationHeader); isRealtimeEphemeralToken(token) {
		return nil
	}
	return newRealtimeWireBifrostError(401, "invalid_request_error", realtimeAuthRefusalMessage)
}

// realtimeUnresolvedCredentialMessage is what a caller presenting a credential governance could
// not resolve is told, mirroring the per-turn refusal so admission and turns name the same fault.
const realtimeUnresolvedCredentialMessage = "the provided credential does not exist, has expired, or has been revoked"

// refuseUnresolvedRealtimeCredential is the second admission question, asked after the
// per-request pipeline (RunPreRequestHooks) has resolved the request's access: the credential
// that was presented, did it turn out to exist? Without this, a forged sk-bf-* key passes the
// presence gate above, and the connection selects the operator's provider key and opens a real
// upstream session that only the first turn refuses - by which point it has already consumed a
// session slot and an upstream connection.
//
// It reads the answer governance recorded on the grant through governance's own exported
// predicate; nothing is resolved or re-implemented here, so admission cannot drift from what the
// per-turn pipeline enforces.
//
// token is the credential the transport extracted from whichever auth header carried it.
// Unmapped ephemeral client secrets (ek_*) are exempt: Bifrost may not have minted them, so
// absence of a local resolution proves nothing and the provider stays authoritative upstream.
// A mapped token whose mapping restored a virtual key is different: that key was placed on the
// context before the pipeline ran, so it is locally resolvable and must still resolve -
// otherwise a token minted before its virtual key was revoked would keep admitting connections
// for the remainder of its TTL. Callers therefore pass mappedVirtualKey only when the mapping
// carries a virtual key; a provider-token-only mapping (minted on an open deployment or with a
// direct provider key) restores nothing locally resolvable and keeps the exemption.
//
// Returns nil when the connection may proceed.
func refuseUnresolvedRealtimeCredential(
	enforceAuthOnInference bool,
	bifrostCtx *schemas.BifrostContext,
	token string,
	mappedVirtualKey bool,
) *schemas.BifrostError {
	if !enforceAuthOnInference {
		return nil
	}
	if isRealtimeEphemeralToken(token) && !mappedVirtualKey {
		return nil
	}
	if governance.PresentedCredentialResolved(bifrostCtx) {
		return nil
	}
	return newRealtimeWireBifrostError(401, "invalid_request_error", realtimeUnresolvedCredentialMessage)
}

// realtimeEphemeralTokenSpentPrefix is where a gateway-minted token's single-use binding
// lives: one establishment per token, ever, in the same store the token's mapping rides.
const realtimeEphemeralTokenSpentPrefix = "realtime:ephemeral-key:spent:"

// realtimeEphemeralTokenSpentTTL outlives any secret a provider mints (ephemeral realtime
// secrets live minutes), so a spent marker never lapses before the token it binds.
const realtimeEphemeralTokenSpentTTL = 24 * time.Hour

func buildRealtimeEphemeralTokenSpentKey(token string) string {
	return realtimeEphemeralTokenSpentPrefix + token
}

// lookupRealtimeEphemeralTokenSpend answers the establishment a token already bound, if any.
func lookupRealtimeEphemeralTokenSpend(kv schemas.KVStore, token string) (string, bool) {
	if kv == nil {
		return "", false
	}
	raw, err := kv.Get(buildRealtimeEphemeralTokenSpentKey(token))
	if err != nil || raw == nil {
		return "", false
	}
	switch value := raw.(type) {
	case string:
		if value == "" {
			return "", false
		}
		return value, true
	default:
		return "", false
	}
}

func markRealtimeEphemeralTokenSpent(kv schemas.KVStore, token string, entrance string) {
	if kv == nil {
		return
	}
	_ = kv.SetWithTTL(buildRealtimeEphemeralTokenSpentKey(token), entrance, realtimeEphemeralTokenSpentTTL)
}

// refuseRealtimeEphemeralTokenBypass is 16.3's (#324) own admission question, asked by EVERY
// realtime establishment (the WebSocket upgrade, its transcription variant, and both WebRTC
// SDP routes) right after the ephemeral-token mapping resolves — 防临时 provider 会话绕开:
//
//   - a provider-shaped ephemeral token this gateway did NOT mint (unmapped ek_*) is refused:
//     the provider stays authoritative about the token upstream, but the session it would
//     stand up here would select the operator's provider key with the OpenBot admission
//     bypassed entirely (no minting, no grant, no reservation identity) — exactly the second
//     road the ticket says must not exist. On deployments that do not enforce inference auth
//     the operator has chosen the open posture and nothing changes;
//   - a gateway-minted token that has already established a session is refused: the token is
//     single-use for establishment (bound at first use), so one minted secret cannot stand
//     two concurrent sessions on the account it was minted under. A reconnect after a drop
//     recovers server-side (the session's durable identity), never by re-spending the token.
//
// Both refusals are recorded in the governance bypass registry for the 全入口 enumeration.
func refuseRealtimeEphemeralTokenBypass(
	enforceAuth bool,
	kv schemas.KVStore,
	token string,
	mapped bool,
	entrance string,
) *schemas.BifrostError {
	if !enforceAuth || !isRealtimeEphemeralToken(token) {
		return nil
	}
	if !mapped {
		governance.RecordRealtimeBypassAttempt(governance.RealtimeBypassUnmappedEphemeralToken,
			entrance+" establishment presented an ephemeral token this gateway did not mint")
		return newRealtimeWireBifrostError(401, "invalid_request_error",
			"the ephemeral token was not minted by this gateway; realtime sessions cannot be established with provider credentials directly — mint one via POST /v1/realtime/client_secrets")
	}
	if spentAt, spent := lookupRealtimeEphemeralTokenSpend(kv, token); spent {
		governance.RecordRealtimeBypassAttempt(governance.RealtimeBypassEphemeralTokenReuse,
			entrance+" establishment reuses an ephemeral token already bound to a session ("+spentAt+")")
		return newRealtimeWireBifrostError(401, "invalid_request_error",
			"this ephemeral client secret has already established a realtime session and cannot establish a second one; mint a new secret")
	}
	markRealtimeEphemeralTokenSpent(kv, token, entrance)
	return nil
}
