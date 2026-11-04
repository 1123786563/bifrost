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
	// RealtimeInputInterceptReserveExhausted (15.5, #318): the door refused the next unit
	// because the account's balance cannot cover it — the session's money is spent. Its own
	// closed reason so the exhaustion verdict is distinguishable from a transport failure
	// and from every other door refusal on every observability surface (耗尽原因与网络错误
	// 可区分); the latch keeps answering under it without re-asking the door.
	RealtimeInputInterceptReserveExhausted = "unit_reserve_exhausted"
	// RealtimeInputInterceptSessionShutdown (15.6, #319): the session's authorization is
	// gone (revoked or expired at the door) — the session is shut down, the relay closes
	// both connections, and every later event of the session answers this reason.
	RealtimeInputInterceptSessionShutdown = "session_shutdown"
	// RealtimeInputInterceptOpenInProgress (15.4, #317): another unit open for the session is
	// still in flight — the concurrent-request policy is REJECT, never queue, and never a
	// second door call while one open is pending (并发单元请求按明确策略拒绝).
	RealtimeInputInterceptOpenInProgress = "unit_open_in_progress"
)

// realtimeInputInterceptionLogLimit bounds the retained interception records: the counters
// stay exact for any volume, the records keep the most recent evidence, and no client
// cadence can grow the session's gate memory without bound (15.4's AC4).
const realtimeInputInterceptionLogLimit = 64

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
	// Shutdown (15.6): the session is over — an authorization verdict no later event can
	// stand on. The relay that receives this closes the upstream connection and stops
	// forwarding (尽力关停 both sides); the flag rides ONLY authorization refusals, never
	// a balance or posture verdict (停止不等于关闭).
	Shutdown bool
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

	// The shutdown latch (15.6): the authorization verdict that ended the session. Once
	// set it answers EVERY later event of the session — billed or not.
	shutdownReason string

	// The observability base (15.4): exact O(1) counters plus a bounded tail of records —
	// a flood of client events grows neither the counters' memory nor the record log.
	interceptedEvents        int64
	interceptedBytes         int64
	interceptedBytesByReason map[string]int64
	interceptions            []RealtimeInputInterception
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

// recordInterception counts one deny and keeps its record inside the bounded log.
func (g *RealtimeInputGate) recordInterception(reason string, detail string, bytes int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.interceptedEvents++
	g.interceptedBytes += bytes
	if g.interceptedBytesByReason == nil {
		g.interceptedBytesByReason = map[string]int64{}
	}
	g.interceptedBytesByReason[reason] += bytes
	g.interceptions = append(g.interceptions, RealtimeInputInterception{
		Reason: reason,
		Detail: detail,
		Bytes:  bytes,
		At:     time.Now(),
	})
	if len(g.interceptions) > realtimeInputInterceptionLogLimit {
		// Drop the oldest retained record: the counters keep the exact totals, the log
		// keeps the newest evidence, and the memory stays flat under any flood.
		g.interceptions = append(g.interceptions[:0], g.interceptions[1:]...)
	}
}

// Stats answers the interception totals by reason — exact counters, safe for any reader.
func (g *RealtimeInputGate) Stats() RealtimeInputGateStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	byReason := make(map[string]int64, len(g.interceptedBytesByReason))
	for reason, bytes := range g.interceptedBytesByReason {
		byReason[reason] = bytes
	}
	return RealtimeInputGateStats{
		InterceptedEvents:        g.interceptedEvents,
		InterceptedBytes:         g.interceptedBytes,
		InterceptedBytesByReason: byReason,
	}
}

// Interceptions answers the recorded denies, oldest first, as a copy.
func (g *RealtimeInputGate) Interceptions() []RealtimeInputInterception {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]RealtimeInputInterception, len(g.interceptions))
	copy(out, g.interceptions)
	return out
}

// markShutdown latches the session's end and sweeps the registry the same moment: every
// still-holding unit of the session becomes unknown (its usage is unconfirmable by the
// shutdown; the holds are kept for the tail settlement and the recovery).
func (g *RealtimeInputGate) markShutdown(sessionID string, reason string) {
	g.mu.Lock()
	already := g.shutdownReason != ""
	if !already {
		g.shutdownReason = reason
	}
	g.mu.Unlock()
	if already {
		return
	}
	for _, unit := range SnapshotRealtimeUnits(sessionID) {
		if unit.State == RealtimeUnitStateReserved || unit.State == RealtimeUnitStateProcessing {
			RecordRealtimeUnitUnknown(sessionID, unit.UnitID, "shutdown_tail_unconfirmed")
		}
	}
}

// ShutdownReason answers the verdict that shut the session down, or "" while the session
// lives — the read the relay consults before every forward.
func (g *RealtimeInputGate) ShutdownReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shutdownReason
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

// liveUnit answers the session's CONSUMING unit for one billed input: the oldest unit of
// the kind that holds a reservation and still has budget under the input ceiling. It — not
// the newest record — is the unit whose hold covers the next append, so a newer unitId
// without a hold cannot take the input over (15.4's serialization: no "new id passes"
// branch exists).
func (g *RealtimeInputGate) liveUnit(sessionID string, tokens int64, ceiling int64) *RealtimeUnitRecord {
	for _, unit := range SnapshotRealtimeUnits(sessionID) {
		if unit.Kind != RealtimeUnitKindInputAudio {
			continue
		}
		if unit.State != RealtimeUnitStateReserved && unit.State != RealtimeUnitStateProcessing {
			continue
		}
		if g.budgetSpent(unit.UnitID)+tokens <= ceiling {
			snapshot := unit
			return &snapshot
		}
	}
	return nil
}

// latchedRefusalDecision maps the recorded refusal onto the interception vocabulary the
// original open already answered with: the door's own class rides the detail, and the
// latch never re-asks the door to find out what it recorded.
func latchedRefusalDecision(unit *RealtimeUnitRecord) (string, string) {
	reason := RealtimeInputInterceptReserveRefused
	switch unit.RefusalReason {
	case CreditRefusalAdmissionUnreachable:
		reason = RealtimeInputInterceptReserveUnreachable
	case CreditRefusalInsufficientBalance:
		reason = RealtimeInputInterceptReserveExhausted
	}
	return reason, "the session's last unit open was refused (" + unit.RefusalReason +
		"); the refusal stands and this input is not forwarded"
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
	// The shutdown latch (15.6) answers EVERY event of a session whose authorization is
	// gone — billed or not: the relay stops forwarding entirely and closes both sides,
	// which is the best-effort shutdown only the transport can perform.
	if reason := g.ShutdownReason(); reason != "" {
		bytes := int64(payloadBytes)
		if !IsBilledRealtimeAudioInput(event) {
			// Control frames carry no billable bytes; the decision still stops them.
			bytes = 0
		}
		g.recordInterception(RealtimeInputInterceptSessionShutdown,
			"the session's authorization is gone ("+reason+"); the session is shut down", bytes)
		return &RealtimeInputGateDecision{
			Forward:  false,
			Reason:   RealtimeInputInterceptSessionShutdown,
			Detail:   "the session was shut down (" + reason + "); the relay closes the connection",
			Shutdown: true,
		}
	}
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

	ceiling := reserve.InputCeilingTokens
	tokens := audioInputTokens(payloadBytes)

	// THE STRICT SERIAL POLICY (15.4): at most one unit of a session consumes at a time,
	// and the live unit is the OLDEST one that holds and still has budget — a newer
	// record without a hold (the client's new unitId) never displaces it, never releases
	// unreserved input, and never spends a second hold (旧单元未结清不放开新输入).
	if unit := g.liveUnit(sessionID, tokens, ceiling); unit != nil {
		MarkRealtimeUnitProcessing(sessionID, unit.UnitID)
		g.spendBudget(unit.UnitID, tokens)
		return &RealtimeInputGateDecision{Forward: true}
	}

	// Before any new unit opens, the session's unsettled state decides: an open already
	// in flight makes the concurrent request REJECT (never queue, never a second door
	// call), and a refused open LATCHES — the recorded refusal stands, and the client's
	// cadence mints nothing and asks nothing (不因客户端行为导致无上限缓冲或内存增长).
	units := SnapshotRealtimeUnits(sessionID)
	newest := (*RealtimeUnitRecord)(nil)
	for i := range units {
		if units[i].Kind == RealtimeUnitKindInputAudio {
			if units[i].State == RealtimeUnitStatePending {
				g.recordInterception(RealtimeInputInterceptOpenInProgress,
					"another unit open for the session is still in flight; the concurrent request is rejected, not queued", int64(payloadBytes))
				return &RealtimeInputGateDecision{
					Forward: false,
					Reason:  RealtimeInputInterceptOpenInProgress,
					Detail:  "a unit open is already in flight for this session, so this billed input is rejected rather than queued",
				}
			}
			newest = &units[i]
		}
	}
	if newest != nil && newest.State == RealtimeUnitStateRefused {
		// The latch: no unit holds, and the last open was refused. The refusal keeps
		// answering until a unit holds again — recovery is a new session or a legitimate
		// rollover, never an optimistic retry loop this gate pays for.
		reason, detail := latchedRefusalDecision(newest)
		g.recordInterception(reason, detail, int64(payloadBytes))
		return &RealtimeInputGateDecision{Forward: false, Reason: reason, Detail: detail}
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
		refusalClass := creditRefusalReasonForDoorCode(refused.Code)
		recordRealtimeUnitRefused(unitID, refusalClass, time.Now())
		reason := RealtimeInputInterceptReserveRefused
		if refusalClass == CreditRefusalInsufficientBalance {
			// 15.5: the balance verdict carries its own reason — the session is out of
			// money, which no retry of this session's input can change.
			reason = RealtimeInputInterceptReserveExhausted
		}
		if refusalClass == CreditRefusalAuthorizationInvalid {
			// 15.6: an authorization the session cannot stand on ends it — revoke or key
			// expiry are one family at this door. The latch is armed, the registry's
			// holding units are unknowned, and the decision tells the relay to shut the
			// session down (close the upstream connection, stop forwarding).
			g.markShutdown(sessionID, refused.Code)
			return "", &RealtimeInputGateDecision{
				Forward:  false,
				Reason:   RealtimeInputInterceptSessionShutdown,
				Detail:   "the session's authorization was refused (" + refused.Code + "); the session is shut down and the input is not forwarded",
				Shutdown: true,
			}
		}
		return "", &RealtimeInputGateDecision{
			Forward: false,
			Reason:  reason,
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
