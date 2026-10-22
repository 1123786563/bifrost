package governance

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

/*
 * The gateway-side realtime billing unit registry (Bifrost 15.2, #315): the process's
 * working state of WHICH unit a realtime session is currently under, and what became of
 * it — the counterpart the input gate (15.3) reads and the observability surfaces count.
 *
 * THE AUTHORITATIVE UNIT ROWS LIVE SERVER-SIDE. The OpenBot reserve composes the
 * transactional unit door (one unit row, one attempt, one hold, one commit), and THIS
 * registry is the gateway's own bookkeeping of the outcomes it caused: every realtime
 * admission the funnel runs either holds a reservation for the session's current unit or
 * refuses it, and the registry records which — with the attempt identity the reserve was
 * asked under, so a lost answer can be recovered under the same identity (06.7) instead of
 * paid for twice. Unit ids minted here (`ru-` plus random bytes, mirroring the server's
 * own prefix) are the gateway's names for its units; the idempotency key of a unit's
 * reserve IS the unit id, so a replayed reserve for the same unit answers the original
 * reservation.
 *
 * THE STATE MACHINE IS THE ONE THE TICKET PINS: pending → reserved → processing → settled
 * | unknown | refused. This side owns the first half (pending while a reserve is in
 * flight, reserved when the door answers 201, refused when anything else answers) and the
 * processing transition the forwarding path drives when a reserved unit's input actually
 * flows; settled and unknown are the settlement tickets' states, recorded here when their
 * paths land, never guessed.
 *
 * WHY A REGISTRY AND NOT THE CONTEXT. A realtime session's relay loop is one long-lived
 * context, but each TURN mints its own turn context — a reservation recorded on a turn
 * context dies with the turn, exactly when the next unit's gate decision needs it. The
 * registry is keyed by the session identity the relay resolves (the session id the
 * transport settles, or the provider's own session id when it has answered), guarded by
 * its own mutex, and every reader gets a COPY: no decision, no settlement, and no test can
 * mutate another's view of a unit.
 */

// RealtimeUnitKind is which side of a session's money a unit covers — the closed kind set
// the server's unit contract carries (输入音频 / 输出音频 / 响应 / 时长).
type RealtimeUnitKind string

const (
	RealtimeUnitKindInputAudio  RealtimeUnitKind = "input_audio"
	RealtimeUnitKindOutputAudio RealtimeUnitKind = "output_audio"
	RealtimeUnitKindResponse    RealtimeUnitKind = "response"
	RealtimeUnitKindDuration    RealtimeUnitKind = "duration"
)

// realtimeUnitKinds is the closed set itself: a kind outside it is a contract change, and
// the recording functions refuse it rather than book an unnamed unit.
var realtimeUnitKinds = map[RealtimeUnitKind]struct{}{
	RealtimeUnitKindInputAudio:  {},
	RealtimeUnitKindOutputAudio: {},
	RealtimeUnitKindResponse:    {},
	RealtimeUnitKindDuration:    {},
}

// RealtimeUnitState is the unit state machine the ticket pins: pending → reserved →
// processing → settled | unknown | refused.
type RealtimeUnitState string

const (
	RealtimeUnitStatePending    RealtimeUnitState = "pending"
	RealtimeUnitStateReserved   RealtimeUnitState = "reserved"
	RealtimeUnitStateProcessing RealtimeUnitState = "processing"
	RealtimeUnitStateSettled    RealtimeUnitState = "settled"
	RealtimeUnitStateUnknown    RealtimeUnitState = "unknown"
	RealtimeUnitStateRefused    RealtimeUnitState = "refused"
)

// RealtimeUnitRecord is one billing unit as the gateway sees it: the unit's own identity,
// the session it belongs to, the attempt and reservation the reserve answered (empty until
// a hold exists), the ceiling the unit certified, the machine's current position, and the
// refusal reason when the unit is refused.
type RealtimeUnitRecord struct {
	UnitID        string            `json:"unitId"`
	SessionID     string            `json:"sessionId"`
	AttemptID     string            `json:"attemptId"`
	ReservationID string            `json:"reservationId"`
	Kind          RealtimeUnitKind  `json:"kind"`
	BoundCents    int64             `json:"boundCents"`
	State         RealtimeUnitState `json:"state"`
	RefusalReason string            `json:"refusalReason"`
	// UnknownReason names why a holding unit became unknown (15.6's shutdown sweep,
	// 15.7's overage tail) — the reason the recovery reads instead of guessing.
	UnknownReason string    `json:"unknownReason"`
	ReservedAt    time.Time `json:"reservedAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// realtimeUnitRegistry is the per-session unit list. The current unit of a kind is the
// NEWEST record of that kind — units are appended, never reordered, so "which unit covers
// the input now" is a lookup, not a scan with an opinion.
type realtimeUnitRegistry struct {
	mu        sync.Mutex
	bySession map[string][]*RealtimeUnitRecord
}

var realtimeUnits = &realtimeUnitRegistry{bySession: map[string][]*RealtimeUnitRecord{}}

// mintRealtimeUnitID mints the gateway's own unit identity: `ru-` plus 16 random bytes as
// hex, the same shape the server's unit rows carry. The unit id doubles as the reserve's
// idempotency key, so it must be unique per unit, ever.
func mintRealtimeUnitID() string {
	body := make([]byte, 16)
	if _, err := rand.Read(body); err != nil {
		// A unit without a fresh identity cannot be reserved idempotently, and a reused
		// identity could re-answer an old hold: refuse the mint loudly rather than book a
		// unit whose money cannot be named.
		panic(fmt.Sprintf("governance: realtime unit id mint failed: %v", err))
	}
	return "ru-" + hex.EncodeToString(body)
}

// RealtimeUnitSessionID resolves the session identity a context's units belong under: the
// realtime session id the transport settled, preferring the relay's own session identity
// (the parent request id the realtime turn context carries) when the provider has not
// answered its own. Empty means the context names no session, and no unit is recorded for
// it — a decision about nothing is never a unit's fact.
func RealtimeUnitSessionID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	if sessionID, ok := ctx.Value(schemas.BifrostContextKeyRealtimeSessionID).(string); ok && strings.TrimSpace(sessionID) != "" {
		return strings.TrimSpace(sessionID)
	}
	if sessionID, ok := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); ok && strings.TrimSpace(sessionID) != "" {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

// recordRealtimeUnitPending appends a pending unit for the session: the reserve is about
// to be asked under the attempt identity, and the unit's existence and its answer must be
// one observable sequence. Returns the unit as recorded.
func recordRealtimeUnitPending(sessionID string, unitID string, attemptID string, kind RealtimeUnitKind, boundCents int64, now time.Time) *RealtimeUnitRecord {
	if sessionID == "" || unitID == "" {
		return nil
	}
	if _, ok := realtimeUnitKinds[kind]; !ok {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	unit := &RealtimeUnitRecord{
		UnitID:     unitID,
		SessionID:  sessionID,
		AttemptID:  attemptID,
		Kind:       kind,
		BoundCents: boundCents,
		State:      RealtimeUnitStatePending,
		UpdatedAt:  now,
	}
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	realtimeUnits.bySession[sessionID] = append(realtimeUnits.bySession[sessionID], unit)
	return unit
}

// recordRealtimeUnitReserved moves a pending unit to reserved with the hold the door
// answered: the reservation the unit took, under the attempt it was asked.
func recordRealtimeUnitReserved(unitID string, reservationID string, now time.Time) {
	if unitID == "" || reservationID == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	for _, units := range realtimeUnits.bySession {
		for _, unit := range units {
			if unit.UnitID == unitID {
				unit.ReservationID = reservationID
				unit.State = RealtimeUnitStateReserved
				unit.RefusalReason = ""
				unit.ReservedAt = now
				unit.UpdatedAt = now
				return
			}
		}
	}
}

// recordRealtimeUnitRefused moves a pending unit to refused with the reason that refused
// it — the door's own code when the door answered, the funnel's own code when the funnel
// refused before asking. A refused unit holds nothing; the record is the evidence.
func recordRealtimeUnitRefused(unitID string, reason string, now time.Time) {
	if unitID == "" || strings.TrimSpace(reason) == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	for _, units := range realtimeUnits.bySession {
		for _, unit := range units {
			if unit.UnitID == unitID {
				unit.State = RealtimeUnitStateRefused
				unit.RefusalReason = strings.TrimSpace(reason)
				unit.ReservationID = ""
				unit.UpdatedAt = now
				return
			}
		}
	}
}

// MarkRealtimeUnitProcessing drives the processing transition from the forwarding path:
// the moment a reserved unit's input actually flows, the unit is processing. Only a
// reserved unit moves; anything else answers the state that refused the move.
func MarkRealtimeUnitProcessing(sessionID string, unitID string) *RealtimeUnitRecord {
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	for _, unit := range realtimeUnits.bySession[sessionID] {
		if unit.UnitID == unitID {
			if unit.State == RealtimeUnitStateReserved {
				unit.State = RealtimeUnitStateProcessing
				unit.UpdatedAt = time.Now()
			}
			snapshot := *unit
			return &snapshot
		}
	}
	return nil
}

// RecordRealtimeUnitUnknown drives the unknown transition (15.6): the shutdown path
// moves a session's still-holding units to unknown with the reason they became
// unconfirmable. Only a holding unit moves; the hold itself is untouched — the row keeps
// the reservation id, and nothing but an evidence-based settlement (15.7) or the recovery
// (08.x) may move it again.
func RecordRealtimeUnitUnknown(sessionID string, unitID string, reason string) {
	if unitID == "" || strings.TrimSpace(reason) == "" {
		return
	}
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	for _, unit := range realtimeUnits.bySession[sessionID] {
		if unit.UnitID != unitID {
			continue
		}
		if unit.State == RealtimeUnitStateReserved || unit.State == RealtimeUnitStateProcessing {
			unit.State = RealtimeUnitStateUnknown
			unit.UnknownReason = strings.TrimSpace(reason)
			unit.UpdatedAt = time.Now()
		}
		snapshot := *unit
		_ = snapshot
		return
	}
}

// RecordRealtimeUnitSettled drives the settled transition (15.7): the tail settlement's
// confirmation moves a holding or unknown unit to settled — the registry's mirror of the
// reservation's own settled fact. Only a hold-bearing or unknown unit moves; a settled
// unit stays (the replay answers the first result), and nothing else does.
func RecordRealtimeUnitSettled(sessionID string, unitID string) {
	if unitID == "" {
		return
	}
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	for _, unit := range realtimeUnits.bySession[sessionID] {
		if unit.UnitID != unitID {
			continue
		}
		if unit.State == RealtimeUnitStateReserved || unit.State == RealtimeUnitStateProcessing || unit.State == RealtimeUnitStateUnknown {
			unit.State = RealtimeUnitStateSettled
			unit.UnknownReason = ""
			unit.UpdatedAt = time.Now()
		}
		return
	}
}

// RealtimeUnitFor answers the session's CURRENT unit of a kind — the newest record of
// that kind, as a copy. Nil means the session has no unit of that kind yet.
func RealtimeUnitFor(sessionID string, kind RealtimeUnitKind) *RealtimeUnitRecord {
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	units := realtimeUnits.bySession[sessionID]
	for i := len(units) - 1; i >= 0; i-- {
		if units[i].Kind == kind {
			snapshot := *units[i]
			return &snapshot
		}
	}
	return nil
}

// SnapshotRealtimeUnits answers the session's whole unit history, oldest first, as
// copies — the observability read (15.2's queryable unit state, and 22.2's evidence base).
func SnapshotRealtimeUnits(sessionID string) []RealtimeUnitRecord {
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	units := realtimeUnits.bySession[sessionID]
	if len(units) == 0 {
		return nil
	}
	out := make([]RealtimeUnitRecord, 0, len(units))
	for _, unit := range units {
		out = append(out, *unit)
	}
	return out
}

// ClearRealtimeUnits drops a session's registry entries — the relay teardown read, so a
// finished session's working state does not outlive it. The unit ROWS server-side are the
// durable record; this only clears the process's bookkeeping.
func ClearRealtimeUnits(sessionID string) {
	realtimeUnits.mu.Lock()
	defer realtimeUnits.mu.Unlock()
	delete(realtimeUnits.bySession, sessionID)
}

// beginRealtimeTurnUnit is the 15.2 reservation hook's turn half: when the admission
// funnel is about to ask the door for a REALTIME turn, the turn is a response unit, and
// its outcome is recorded for the session — reserved with the hold the door answered, or
// refused with the funnel's own code. The returned unit id is empty when the context names
// no session (nothing is recorded); the reserve proceeds unchanged either way, because the
// unit registry is bookkeeping, never a second admission.
func beginRealtimeTurnUnit(ctx *schemas.BifrostContext, attemptID string, boundCents int64) string {
	sessionID := RealtimeUnitSessionID(ctx)
	if sessionID == "" {
		return ""
	}
	unitID := mintRealtimeUnitID()
	recordRealtimeUnitPending(sessionID, unitID, attemptID, RealtimeUnitKindResponse, boundCents, time.Now())
	return unitID
}

// finishRealtimeTurnUnit records the turn unit's outcome after the funnel decided it.
func finishRealtimeTurnUnit(ctx *schemas.BifrostContext, unitID string, reservationID string, refusalReason string) {
	if unitID == "" {
		return
	}
	if reservationID != "" {
		recordRealtimeUnitReserved(unitID, reservationID, time.Now())
		return
	}
	recordRealtimeUnitRefused(unitID, refusalReason, time.Now())
}
