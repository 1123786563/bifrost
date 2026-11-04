package governance

import (
	"sync"
	"time"
)

/*
 * The realtime bypass-attempt registry (Bifrost 16.3, #324): the record that a client tried
 * to stand a realtime session on a credential the OpenBot admission never minted — or to
 * reuse one it had already spent — and was refused (旁路尝试被拒绝并记录审计).
 *
 * WHY A REGISTRY AND NOT A LOG LINE. The refusals themselves are the doors' own 401s; what
 * 22.x's 全入口核查 needs is the COUNTABLE evidence: which bypass kinds the deployment has
 * actually seen, how often, and the newest bounded records with their detail — the same
 * shape the input gate's interceptions keep (exact O(1) counters plus a bounded tail), so a
 * flood of attempts grows neither the counters' memory nor the record log. The registry is
 * process-level like the unit registry: it is the gateway's own observability, never a
 * second admission — nothing here refuses anything; the handlers refuse, and record here.
 *
 * THE CLOSED KIND SET is the two ways around the minting path the tickets name:
 *   unmapped_ephemeral_token  a provider-shaped ephemeral token this gateway did not mint
 *                             (临时 provider 会话凭据) — the provider stays authoritative
 *                             about the token's validity, but the session it would stand up
 *                             here would run on OUR provider key with OUR admission bypassed;
 *   ephemeral_token_reuse     a gateway-minted ek_bf_* presented to establish a SECOND
 *                             session after the first already stands (凭据复用建立第二个
 *                             会话) — the token is single-use for establishment by binding.
 */

const (
	RealtimeBypassUnmappedEphemeralToken = "unmapped_ephemeral_token"
	RealtimeBypassEphemeralTokenReuse    = "ephemeral_token_reuse"
)

// realtimeBypassKinds is the closed set itself: a kind outside it is a contract change, and
// the recording function drops it rather than book an unnamed attempt.
var realtimeBypassKinds = map[string]struct{}{
	RealtimeBypassUnmappedEphemeralToken: {},
	RealtimeBypassEphemeralTokenReuse:    {},
}

// RealtimeBypassAttempt is one recorded refusal: the closed kind, the sentence-free detail
// (identities and verdicts, never credentials — no token value is ever recorded), and when.
type RealtimeBypassAttempt struct {
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// realtimeBypassLogLimit bounds the retained records, the input gate's own calibration:
// counters stay exact for any volume, records keep the newest evidence.
const realtimeBypassLogLimit = 64

type realtimeBypassRegistry struct {
	mu      sync.Mutex
	byKind  map[string]int64
	total   int64
	attempt []RealtimeBypassAttempt
}

var realtimeBypass realtimeBypassRegistry

// RecordRealtimeBypassAttempt counts one refused bypass attempt. The detail is free-form
// evidence for the operator; callers pass identities and verdicts, never secret values.
func RecordRealtimeBypassAttempt(kind string, detail string) {
	if _, ok := realtimeBypassKinds[kind]; !ok {
		return
	}
	realtimeBypass.mu.Lock()
	defer realtimeBypass.mu.Unlock()
	if realtimeBypass.byKind == nil {
		realtimeBypass.byKind = map[string]int64{}
	}
	realtimeBypass.byKind[kind]++
	realtimeBypass.total++
	realtimeBypass.attempt = append(realtimeBypass.attempt, RealtimeBypassAttempt{
		Kind:   kind,
		Detail: detail,
		At:     time.Now(),
	})
	if len(realtimeBypass.attempt) > realtimeBypassLogLimit {
		realtimeBypass.attempt = append(realtimeBypass.attempt[:0], realtimeBypass.attempt[1:]...)
	}
}

// RealtimeBypassStats is the observability read: totals by kind, safe for any reader.
type RealtimeBypassStats struct {
	Total    int64            `json:"total"`
	ByKind   map[string]int64 `json:"byKind"`
	Attempts int64            `json:"attempts"` // retained records, bounded
}

// RealtimeBypassStatistics answers the registry's counters — the evidence base the 全入口
// enumeration (22.x) cites.
func RealtimeBypassStatistics() RealtimeBypassStats {
	realtimeBypass.mu.Lock()
	defer realtimeBypass.mu.Unlock()
	byKind := make(map[string]int64, len(realtimeBypass.byKind))
	for kind, count := range realtimeBypass.byKind {
		byKind[kind] = count
	}
	return RealtimeBypassStats{
		Total:    realtimeBypass.total,
		ByKind:   byKind,
		Attempts: int64(len(realtimeBypass.attempt)),
	}
}

// SnapshotRealtimeBypassAttempts answers the retained records, newest last, as a copy.
func SnapshotRealtimeBypassAttempts() []RealtimeBypassAttempt {
	realtimeBypass.mu.Lock()
	defer realtimeBypass.mu.Unlock()
	out := make([]RealtimeBypassAttempt, len(realtimeBypass.attempt))
	copy(out, realtimeBypass.attempt)
	return out
}

// ClearRealtimeBypassAttemptsForTest resets the registry between tests.
func ClearRealtimeBypassAttemptsForTest() {
	realtimeBypass.mu.Lock()
	defer realtimeBypass.mu.Unlock()
	realtimeBypass.byKind = nil
	realtimeBypass.total = 0
	realtimeBypass.attempt = nil
}
