package governance

import (
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

/*
 * The realtime audio input gate (Bifrost 15.3, #316): the forwarding-path decision that
 * billed audio input reaches the provider only when the session's current billing unit
 * holds a reservation (未获得单元预留时不继续向 provider 送计费输入).
 *
 * THE GATE IS BEFORE THE FORWARD, NOT AFTER. The relay asks this gate for every realtime
 * client event, and the gate — not the caller — decides which events are billed audio
 * input (the closed event-type set below). A decision the gate does not allow is never
 * written upstream: the audio is dropped, unbilled (丢弃不计费), and the interception is
 * recorded with its reason. Forwarding after a held reservation is byte-for-byte the path
 * it always was (门控不改变已预留单元的正常处理).
 *
 * NO CLIENT PARAMETER REACHES THE DECISION. The gate reads exactly three things: the event's
 * type (against the closed set), the session's unit registry (15.2's records), and the
 * reserve door's answer when it opens a unit. Nothing on the wire — no event field, no
 * session parameter, no header — can make an ungated unit look reserved (客户端无法通过
 * 参数绕过).
 *
 * UNITS ARE THE GATE'S OWN RESERVATIONS. The first billed input of a unit asks the
 * deployment's reserve door for an input-audio unit BEFORE the audio forwards: the unit's
 * id is the reserve's idempotency key (a replay of the same unit answers the original
 * hold), and the bound evidence is the mechanism THIS gate enforces itself — the audio it
 * forwards under one unit is counted conservatively and stopped at the deployment's input
 * ceiling (`self_enforced_input_truncation`, the same kind and calibration the chat path's
 * evidence carries, ADR 0001). When a unit's budget is spent, the gate opens the NEXT unit
 * the same way; when the door refuses (the account, the grant, the price basis — the door's
 * own codes) or cannot answer, the input stops and the interception names why.
 *
 * WHEN THE GATE IS OFF. The gate is part of the credit-admission extension: a deployment
 * that has not enabled it pays nothing here (one locked config read, no unit, no
 * interception), and a model the catalog prices EXPLICITLY FREE costs nothing to serve —
 * the funnel's own free semantics — so its input needs no unit.
 *
 * THE INTERCEPTIONS ARE THE OBSERVABLE. Every deny appends one record — the closed reason,
 * the bytes not forwarded, the time — and the stats read answers the counts by reason: the
 * evidence 22.2 counts and the operator reads (被拦截的输入有可观测记录与原因).
 */

// governanceRealtimeInputGateContextKey is the ctx slot the upgrade-time handle rides: set
// by PreRequestHook on the realtime upgrade request, carried into the long-lived relay
// context through the transport's middleware-value snapshot, read by the relay before every
// forward. The value is a *RealtimeInputGate — a small handle, never stream-sized data.
const governanceRealtimeInputGateContextKey = schemas.BifrostContextKey("bifrost-governance-realtime-input-gate")

// The closed interception vocabulary: a new member is a contract change.
const (
	// RealtimeInputInterceptReserveRefused: the door answered a refusal (its own code rides
	// the interception's detail) — the account, grant or price basis said no.
	RealtimeInputInterceptReserveRefused = "unit_reserve_refused"
	// RealtimeInputInterceptReserveUnreachable: no door wired, or the door could not answer
	// (transport failure, timeout, an answer that cannot be read as a hold). Treated as
	// refused — there is no optimistic branch (AC05).
	RealtimeInputInterceptReserveUnreachable = "unit_reserve_unreachable"
	// RealtimeInputInterceptUnitNotReserved: no unit holds for the session and none could be
	// opened (the admission extension is enabled without a usable reserve wiring).
	RealtimeInputInterceptUnitNotReserved = "unit_not_reserved"
)

// IsBilledRealtimeAudioInput answers whether a realtime client event is billed AUDIO INPUT —
// the closed set the gate enforces. Today the set is exactly the audio append: the event
// that carries billable audio bytes toward the provider's buffer. The commit (which starts
// a turn) and every control event are the turn pipeline's own admission's business, not
// this gate's; a new event type joins the set only with its own billing evidence.
func IsBilledRealtimeAudioInput(event *schemas.BifrostRealtimeEvent) bool {
	return event != nil && event.Type == schemas.RTEventInputAudioAppend
}

// RealtimeInputInterception is one recorded deny: why, how much audio was not forwarded,
// and the door's own code when the door answered.
type RealtimeInputInterception struct {
	Reason string    `json:"reason"`
	Detail string    `json:"detail,omitempty"`
	Bytes  int64     `json:"bytes"`
	At     time.Time `json:"at"`
}

// RealtimeInputGateStats is the observability read: how much billed input was intercepted,
// by reason — the evidence base 22.2 counts from.
type RealtimeInputGateStats struct {
	InterceptedEvents        int64            `json:"interceptedEvents"`
	InterceptedBytes         int64            `json:"interceptedBytes"`
	InterceptedBytesByReason map[string]int64 `json:"interceptedBytesByReason"`
}

// RealtimeInputGateDecision is the relay's answer: forward, or drop with the recorded
// reason. A nil decision means "not this gate's business" — forward unchanged.
type RealtimeInputGateDecision struct {
	Forward bool
	Reason  string
	Detail  string
}

// audioInputTokens is the conservative count of one append's billable audio: the serialized
// bytes at the shared calibration (a 2-byte floor per token, the 1.25 safety factor), never
// below one token — an append that carries audio carries billable audio.
func audioInputTokens(payloadBytes int) int64 {
	if payloadBytes <= 0 {
		return 1
	}
	return int64(math.Ceil((float64(payloadBytes) * conservativeSafetyFactor) / conservativeBytesPerTokenFloor))
}

// RealtimeInputGate is the per-session handle: the plugin it decides through, the audio
// budget each held unit has already spent, and the interception records. Created at the
// realtime upgrade (PreRequestHook), carried to the relay on the context, and asked before
// every billed input forward.
type RealtimeInputGate struct {
	plugin *GovernancePlugin

	mu              sync.Mutex
	forwardedTokens map[string]int64 // unit id -> conservatively counted tokens forwarded under it
	interceptions   []RealtimeInputInterception
}

// attachRealtimeInputGate creates the session's gate handle, bound to this plugin, on the
// context. Called from PreRequestHook for a realtime upgrade request; the handle is inert
// until the relay asks it to decide, so the common path (admission off, free models,
// non-audio events) never touches it.
func (p *GovernancePlugin) attachRealtimeInputGate(ctx *schemas.BifrostContext) {
	if ctx == nil {
		return
	}
	if existing, _ := ctx.Value(governanceRealtimeInputGateContextKey).(*RealtimeInputGate); existing != nil {
		return
	}
	ctx.SetValue(governanceRealtimeInputGateContextKey, &RealtimeInputGate{
		plugin:          p,
		forwardedTokens: map[string]int64{},
	})
}

// RealtimeInputGateFromContext answers the session's gate handle, or nil when the upgrade
// never attached one (a deployment without the governance plugin, or a pre-gate session).
func RealtimeInputGateFromContext(ctx *schemas.BifrostContext) *RealtimeInputGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(governanceRealtimeInputGateContextKey).(*RealtimeInputGate)
	return gate
}

// RealtimeInputGateContextKey answers the ctx key the handle rides, for the transport's
// realtime middleware-value snapshot — the one place outside this package that names the
// key, so it never has to guess the slot's spelling.
func RealtimeInputGateContextKey() schemas.BifrostContextKey {
	return governanceRealtimeInputGateContextKey
}

// recordInterception appends one deny record under the lock.
func (g *RealtimeInputGate) recordInterception(reason string, detail string, bytes int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.interceptions = append(g.interceptions, RealtimeInputInterception{
		Reason: reason,
		Detail: detail,
		Bytes:  bytes,
		At:     time.Now(),
	})
}

// Stats answers the interception totals by reason — copies, safe for any reader.
func (g *RealtimeInputGate) Stats() RealtimeInputGateStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	stats := RealtimeInputGateStats{InterceptedBytesByReason: map[string]int64{}}
	for _, interception := range g.interceptions {
		stats.InterceptedEvents++
		stats.InterceptedBytes += interception.Bytes
		stats.InterceptedBytesByReason[interception.Reason] += interception.Bytes
	}
	return stats
}

// Interceptions answers the recorded denies, oldest first, as a copy.
func (g *RealtimeInputGate) Interceptions() []RealtimeInputInterception {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]RealtimeInputInterception, len(g.interceptions))
	copy(out, g.interceptions)
	return out
}

// budgetSpent answers the tokens already forwarded under the unit.
func (g *RealtimeInputGate) budgetSpent(unitID string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.forwardedTokens[unitID]
}

// spendBudget records the tokens one forwarded append spent under the unit.
func (g *RealtimeInputGate) spendBudget(unitID string, tokens int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forwardedTokens[unitID] += tokens
}

// Decide is the forwarding decision for one realtime client event: nil — not this gate's
// business, forward unchanged — or forward-with-a-held-unit / drop-with-a-reason. The
// payload bytes are the serialized event the relay would write upstream; the gate counts
// them conservatively against the unit's input ceiling (the evidence it certified).
func (g *RealtimeInputGate) Decide(
	ctx *schemas.BifrostContext,
	sessionID string,
	provider schemas.ModelProvider,
	model string,
	event *schemas.BifrostRealtimeEvent,
	payloadBytes int,
) *RealtimeInputGateDecision {
	if !IsBilledRealtimeAudioInput(event) {
		// Not billed audio input: the gate does not touch it (a control event, a turn
		// start — the turn pipeline's admission owns those).
		return nil
	}

	plugin := g.plugin
	if plugin == nil {
		// The handle was never bound to a live plugin — a shape only a future refactor
		// could produce. Fail closed: an unbilled-forever input is the one outcome the
		// gate exists to prevent, so it refuses rather than stands down.
		g.recordInterception(RealtimeInputInterceptUnitNotReserved, "gate handle is unbound", int64(payloadBytes))
		return &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptUnitNotReserved,
			Detail:  "the input gate is not bound to a governance plugin, so no billing unit can cover this input",
		}
	}

	plugin.cfgMutex.RLock()
	enabled := plugin.creditAdmissionEnabledLocked()
	var reserve *CreditReserveConfig
	if enabled && plugin.openBotCreditAdmission != nil {
		reserve = plugin.openBotCreditAdmission.Reserve
	}
	plugin.cfgMutex.RUnlock()
	if !enabled {
		// The credit-admission extension is off: this gate is part of it, and the common
		// case pays nothing here — one explicit allow, no unit, no door, no record.
		return &RealtimeInputGateDecision{Forward: true}
	}
	if reserve == nil {
		g.recordInterception(RealtimeInputInterceptUnitNotReserved, "no reserve door is wired", int64(payloadBytes))
		return &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptUnitNotReserved,
			Detail:  "credit admission is enabled but no reserve door is wired, so no billing unit can cover this input",
		}
	}
	if problem := reserve.wiringProblem(); problem != "" {
		g.recordInterception(RealtimeInputInterceptUnitNotReserved, problem, int64(payloadBytes))
		return &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptUnitNotReserved,
			Detail:  "the credit reserve wiring is incomplete (" + problem + "), so no billing unit can cover this input",
		}
	}
	// Explicitly free costs nothing to serve — the funnel's own free semantics — so its
	// input needs no unit. Unknown pricing is NOT this branch: it goes to the door, where
	// the price basis refuses it.
	if plugin.classifyPricing(provider, model) == pricingExplicitlyFree {
		return &RealtimeInputGateDecision{Forward: true}
	}

	unit := RealtimeUnitFor(sessionID, RealtimeUnitKindInputAudio)
	ceiling := reserve.InputCeilingTokens
	tokens := audioInputTokens(payloadBytes)

	// A hold-bearing unit with budget left covers this append; anything else (no unit yet,
	// a refused unit, a spent budget) requires the NEXT unit before the audio forwards.
	if unit != nil && (unit.State == RealtimeUnitStateReserved || unit.State == RealtimeUnitStateProcessing) {
		if g.budgetSpent(unit.UnitID)+tokens <= ceiling {
			MarkRealtimeUnitProcessing(sessionID, unit.UnitID)
			g.spendBudget(unit.UnitID, tokens)
			return &RealtimeInputGateDecision{Forward: true}
		}
	}

	// Open the next input unit: the gate's own reservation, asked BEFORE the audio
	// forwards, under the unit's id as the idempotency key — the mechanism this gate
	// enforces itself is the evidence it certifies (audio counted conservatively, stopped
	// at the input ceiling).
	unitID, decision := g.openInputUnit(ctx, sessionID, provider, model, reserve)
	if decision != nil {
		g.recordInterception(decision.Reason, decision.Detail, int64(payloadBytes))
		return decision
	}
	MarkRealtimeUnitProcessing(sessionID, unitID)
	g.spendBudget(unitID, tokens)
	return &RealtimeInputGateDecision{Forward: true}
}

// openInputUnit reserves one input-audio unit through the deployment's reserve door. On
// success it answers the held unit's id; on failure it answers the drop decision with the
// door's own story, and nothing forwards.
func (g *RealtimeInputGate) openInputUnit(
	ctx *schemas.BifrostContext,
	sessionID string,
	provider schemas.ModelProvider,
	model string,
	reserve *CreditReserveConfig,
) (string, *RealtimeInputGateDecision) {
	unitID := mintRealtimeUnitID()
	recordRealtimeUnitPending(sessionID, unitID, unitID, RealtimeUnitKindInputAudio, reserve.MaximumCredits, time.Now())

	evidence := []CreditReserveBoundEvidence{{
		Kind:          "self_enforced_input_truncation",
		MaximumTokens: reserve.InputCeilingTokens,
	}}
	payload, err := json.Marshal(reserve.requestFor(&EvaluationRequest{
		RequestType: schemas.RealtimeRequest,
		Provider:    provider,
		Model:       model,
	}, unitID, evidence))
	if err != nil {
		recordRealtimeUnitRefused(unitID, CreditRefusalAdmissionUnreachable, time.Now())
		return "", &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptReserveUnreachable,
			Detail:  "the input unit's reserve request could not be built",
		}
	}

	status, body, transportErr := g.plugin.callReserveDoor(ctx, reserve, payload)
	if transportErr != nil {
		recordRealtimeUnitRefused(unitID, CreditRefusalAdmissionUnreachable, time.Now())
		return "", &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptReserveUnreachable,
			Detail:  "the input unit's reserve " + reserveTransportFailureClass(transportErr) + "; treated as refused, the input is not forwarded",
		}
	}

	switch {
	case status == http.StatusCreated:
		var answer creditReserveAnswer
		if err := json.Unmarshal(body, &answer); err != nil || answer.ReservationID == "" || answer.SnapshotVersion <= 0 {
			recordRealtimeUnitRefused(unitID, CreditRefusalAdmissionUnreachable, time.Now())
			return "", &RealtimeInputGateDecision{
				Forward: false,
				Reason:  RealtimeInputInterceptReserveUnreachable,
				Detail:  "the input unit's reserve answer could not be read as a held reservation; treated as refused",
			}
		}
		recordRealtimeUnitReserved(unitID, answer.ReservationID, time.Now())
		return unitID, nil
	case status == http.StatusUnprocessableEntity:
		var refused creditReserveRefusal
		_ = json.Unmarshal(body, &refused)
		if refused.Code == "" {
			refused.Code = "unspecified"
		}
		recordRealtimeUnitRefused(unitID, creditRefusalReasonForDoorCode(refused.Code), time.Now())
		return "", &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptReserveRefused,
			Detail:  "the input unit's reserve was refused (" + refused.Code + "); the input is not forwarded",
		}
	default:
		recordRealtimeUnitRefused(unitID, CreditRefusalAdmissionUnreachable, time.Now())
		return "", &RealtimeInputGateDecision{
			Forward: false,
			Reason:  RealtimeInputInterceptReserveUnreachable,
			Detail:  "the input unit's reserve endpoint answered a status that is not a held reservation; treated as refused",
		}
	}
}
