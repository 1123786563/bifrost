// The dispatch commitment (07.4): the record the gateway persists for a provider attempt
// BEFORE the attempt may dispatch, and the idempotent claim that writes it.
//
// Why the order is fixed. A provider attempt that reaches a provider without a persisted
// commitment is an attempt whose cost no one can later account for: if the process dies
// between dispatch and settlement, the reservation's fate - charged, chargeable, releasable -
// is nobody's evidence. So the claim runs inside the last gate before dispatch (the PreLLM
// funnel's reserve step, creditadmission.go): a request the claim does not let through never
// reaches a provider, and a request it lets through always has its commitment already written.
// The reserve is asked first (the commitment carries the reservation the hold produced), the
// commitment is claimed second, the dispatch is authorized last.
//
// Why the claim is idempotent. The attempt identity (creditAttemptID: bfat-, a stable digest
// of the attempt's own coordinates) is the claim's key. One attempt identity maps to one
// commitment and at most one provider call, ever: a replayed dispatch of the same attempt
// finds the existing commitment and is refused instead of dispatching again - the first
// dispatch's result is unknown to the replay, so a second provider call under the same
// identity is how double billing is born. This is the gateway-side half of the idempotency
// the reserve door already enforces (06.7 answers a replayed reserve with the original
// reservation); the refusal for the duplicate is DecisionDispatchAlreadyClaimed.
//
// Why pending stays pending. A claimed attempt whose result is not yet known - the process
// died mid-flight, the answer never came back - keeps its commitment in pending status. The
// hold it carries is 06.8's unknown-result hold: never reclaimed, never re-sent. Recovery
// (08.x) walks the store's scan seam (ListPending), decides from evidence, and settles or
// releases; nothing in the dispatch path re-sends a pending attempt on its own.
//
// The store is a seam, not a backend. The default implementation keeps records in the process;
// the interface is the recovery contract downstream tickets build on - a backend that outlives
// the process implements the same two methods, and every rule in this file holds of it: claim
// once by attempt identity, return the existing record to later claimants, never lose a
// pending record silently.
package governance

import (
	"context"
	"fmt"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// DispatchCommitmentStatusPending marks a commitment whose dispatch result is not yet known.
// It is the only status this ticket writes: a commitment is born pending and stays pending
// until settlement or recovery (07.8/08.x) moves it, on evidence.
const DispatchCommitmentStatusPending = "pending"

// DispatchCommitment is the record one provider attempt carries from its claim to its
// settlement: the attempt identity and what was dispatched, when it was claimed, and the
// ownership and reservation basis recovery needs to finish what the dispatch started -
// whose grant and account the hold bills to, the hold's ceiling, and the locked price and
// conversion-rate versions the reserve answer echoed (06.5's basis).
type DispatchCommitment struct {
	// The attempt identity (bfat-...), the claim's idempotency key.
	AttemptID string `json:"attemptId"`
	// What the held reservation is, from the reserve answer.
	ReservationID string `json:"reservationId"`
	// What was dispatched, as the funnel saw it.
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	ClaimedAt time.Time `json:"claimedAt"`
	// The dispatch result's state; pending until settlement or recovery says otherwise.
	Status string `json:"status"`

	// Ownership: the grant the call runs under and the payment account the hold bills to,
	// as the reserve door carried them.
	GrantID   string `json:"grantId"`
	PayerKind string `json:"payerKind"`
	PayerID   string `json:"payerId"`

	// The reservation basis: the hold's ceiling in scaled credit units and the locked
	// versions the reserve answer echoed.
	MaximumCredits  int64 `json:"maximumCredits"`
	SnapshotVersion int   `json:"snapshotVersion"`
	RateVersion     int   `json:"rateVersion"`

	// The retry chain (07.5): where this attempt sits in its request's chain of real
	// attempts, recorded so retry count and ownership are auditable from the records alone
	// (22.2's evidence) - no re-derivation, no other surface to correlate.
	//
	// The coordinates are the attempt identity's own: the request id and the unforgeable
	// billing nonce of the physical call (the caller-supplied request id alone cannot forge
	// a chain - two records agreeing on the request id but not the nonce are different
	// physical calls), and the fallback slot the attempt dispatched. The coordinates are
	// the slot's, not the intra-slot retry counter's (NumberOfRetries): core runs the
	// PreLLM hooks once per slot and a slot's physical retries re-enter no hook, so they
	// are this attempt - one identity, one commitment, one hold - while each slot of the
	// chain is a distinct identity that admits itself again.
	RequestID     string `json:"requestId"`
	BillingNonce  string `json:"billingNonce"`
	FallbackIndex int    `json:"fallbackIndex"`
	// The attempt this one retries: the previous attempt of the same physical request that
	// claimed and dispatched, read from the reservation the funnel stamped on the context at
	// that attempt's own admission - nothing between the two admissions replaces it. Empty
	// on the chain's root. An attempt the funnel refused between the two never claimed, so
	// it is correctly not a link: the chain connects the attempts that really dispatched.
	RetryOfAttemptID string `json:"retryOfAttemptId"`
}

// DispatchCommitmentStore persists dispatch commitments and answers recovery's scan. Claim is
// idempotent by AttemptID: the first claim writes the record and wins (claimed=true); every
// later claim of the same attempt identity returns the existing record, unchanged, and loses
// (claimed=false). ListPending is the scan seam recovery (08.x) walks: every commitment whose
// result is still unknown.
type DispatchCommitmentStore interface {
	Claim(ctx context.Context, commitment DispatchCommitment) (DispatchCommitment, bool, error)
	ListPending(ctx context.Context) ([]DispatchCommitment, error)
}

// localDispatchCommitmentStore is the default in-process backend: a mutex and a map keyed by
// attempt identity. Records live as long as the process does; a deployment that needs
// commitments to survive restarts wires a persistent backend under the same interface.
type localDispatchCommitmentStore struct {
	mu      sync.Mutex
	records map[string]DispatchCommitment
}

// newLocalDispatchCommitmentStore builds the default in-process commitment store.
func newLocalDispatchCommitmentStore() *localDispatchCommitmentStore {
	return &localDispatchCommitmentStore{records: make(map[string]DispatchCommitment)}
}

// Claim writes the commitment on the first claim of its attempt identity and elects that one
// caller the winner; later claimants of the same identity are handed the existing record,
// unchanged, and told they lost. The store stamps claimedAt and status, so both are written
// exactly once, with the record.
func (s *localDispatchCommitmentStore) Claim(ctx context.Context, commitment DispatchCommitment) (DispatchCommitment, bool, error) {
	if commitment.AttemptID == "" {
		return DispatchCommitment{}, false, fmt.Errorf("dispatch commitment has no attempt id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[commitment.AttemptID]; ok {
		return existing, false, nil
	}
	commitment.Status = DispatchCommitmentStatusPending
	commitment.ClaimedAt = time.Now().UTC()
	s.records[commitment.AttemptID] = commitment
	return commitment, true, nil
}

// ListPending returns every commitment whose result is still unknown - the records recovery
// walks. The returned records are copies; a caller may read them without seeing later writes.
func (s *localDispatchCommitmentStore) ListPending(ctx context.Context) ([]DispatchCommitment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]DispatchCommitment, 0, len(s.records))
	for _, record := range s.records {
		if record.Status == DispatchCommitmentStatusPending {
			pending = append(pending, record)
		}
	}
	return pending, nil
}

// commitDispatch claims the attempt's commitment at the moment dispatch becomes possible: the
// reserve held, nothing has been sent. Nil means the commitment is claimed and the request may
// proceed to the provider - the record is already persisted at this point, which is the order
// the recovery story depends on. A result means the dispatch is refused: the attempt was
// already claimed (the duplicate gets the existing commitment and no dispatch), or the
// commitment could not be persisted (no commitment, no dispatch), and the request is answered
// on the same refusal path as every other credit ending - marked refused, no provider called.
func (p *GovernancePlugin) commitDispatch(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, attemptID string, reserve *CreditReserveConfig, answer creditReserveAnswer) *EvaluationResult {
	store := p.dispatchCommitments
	if store == nil {
		// A plugin built outside the constructors has no store to claim in: the gateway
		// cannot prove a commitment exists, so it dispatches nothing.
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("no dispatch commitment store is wired: model %q is refused and no provider is called",
				evaluationRequest.Model),
		}
	}
	committed, claimed, err := store.Claim(ctx, DispatchCommitment{
		AttemptID:        attemptID,
		ReservationID:    answer.ReservationID,
		Provider:         string(evaluationRequest.Provider),
		Model:            evaluationRequest.Model,
		GrantID:          reserve.GrantID,
		PayerKind:        reserve.PayerKind,
		PayerID:          reserve.PayerID,
		MaximumCredits:   answer.MaximumCredits,
		SnapshotVersion:  answer.SnapshotVersion,
		RateVersion:      answer.RateVersion,
		RequestID:        bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID),
		BillingNonce:     bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyBillingNonce),
		FallbackIndex:    fallbackIndexOf(ctx),
		RetryOfAttemptID: retryOfAttemptID(ctx),
	})
	if err != nil {
		// The commitment could not be written. Whatever the reserve door answered, an
		// attempt without a persisted commitment is an attempt whose cost recovery cannot
		// account for, so the request does not dispatch.
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the dispatch commitment could not be persisted (%s): model %q is refused and no provider is called",
				err, evaluationRequest.Model),
		}
	}
	if !claimed {
		// The attempt identity is already claimed and its result is unknown to this
		// replay. The existing commitment stands - this dispatch adds nothing and sends
		// nothing: one attempt identity, one commitment, one provider call, ever.
		return &EvaluationResult{
			Decision:   DecisionDispatchAlreadyClaimed,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("attempt %s is already claimed and its result is pending: the gateway does not dispatch one attempt twice, so model %q is refused and no provider is called",
				committed.AttemptID, evaluationRequest.Model),
		}
	}
	return nil
}

// fallbackIndexOf reads the attempt's slot in its request's fallback chain, the same coordinate
// the attempt identity digests. Zero is the primary attempt - both the context default and the
// explicit value.
func fallbackIndexOf(ctx *schemas.BifrostContext) int {
	fallbackIndex, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	return fallbackIndex
}

// retryOfAttemptID names the attempt this one retries, from the reservation the funnel stamped
// on the context at the previous attempt's own admission. The stamp is written exactly when an
// attempt claims (reserveBeforeDispatch: reserve, claim, stamp) and is read here BEFORE this
// attempt's own stamp replaces it, so it always names the last attempt of this request that
// claimed and dispatched. An attempt the funnel refused between the two left no stamp of its
// own, so a chain whose middle slot was refused links around it - the records connect the
// attempts that really dispatched. Reading it never fabricates a link: no previous stamp (the
// chain's root, or a first attempt on a fresh context) means an empty RetryOfAttemptID, not a
// guess.
func retryOfAttemptID(ctx *schemas.BifrostContext) string {
	if previous, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation); ok {
		return previous.AttemptID
	}
	return ""
}
