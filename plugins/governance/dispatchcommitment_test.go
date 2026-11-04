package governance

// The dispatch commitment (07.4): before any provider is called, the gateway persists a
// commitment for the attempt - keyed by the same attempt identity the reserve carried - and
// the claim is idempotent: a replayed dispatch of one attempt claims nothing new and dispatches
// nothing new. A commitment whose result is unknown stays pending; recovery (08.x) reads it
// from the store's scan seam, and nothing re-sends the attempt on its own.
//
// The tests reuse the 07.2 harness (the fake credit service, the armed extension, the funnel
// context): "dispatch" in this harness is the PreLLM hook letting the request through, the
// same observable review-264 established - a short circuit reaches no provider, a nil one does.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatchCommitmentClaimedBeforeDispatchCarriesRecoveryBasis holds the first AC: a
// request that is allowed through to a provider has a persisted commitment for its attempt at
// the moment it is allowed through, and the commitment carries everything recovery needs -
// who the attempt belongs to, what it reserved, under which locked basis.
func TestDispatchCommitmentClaimedBeforeDispatchCarriesRecoveryBasis(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	pending, err := plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	require.Empty(t, pending, "nothing has been dispatched yet, so no commitment exists")

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a held reservation and a claimed commitment let the request proceed to dispatch")

	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok, "the held reservation is stamped on the request context")

	pending, err = plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "one dispatch, one commitment")
	record := pending[0]

	// The identity: the same attempt identity the reserve was asked under and the context
	// stamps - not a second identity minted elsewhere.
	assert.Regexp(t, `^bfat-[0-9a-f]{32}$`, record.AttemptID)
	assert.Equal(t, reservation.AttemptID, record.AttemptID)
	assert.Equal(t, "res-reserve-1", record.ReservationID)
	assert.Equal(t, "openai", record.Provider)
	assert.Equal(t, reservePaidModel, record.Model)
	assert.Equal(t, DispatchCommitmentStatusPending, record.Status,
		"a claimed dispatch whose result is not yet known is pending")

	// claimedAt: when the commitment was claimed, and it is a real timestamp.
	require.False(t, record.ClaimedAt.IsZero(), "a commitment records when it was claimed")
	assert.WithinDuration(t, time.Now(), record.ClaimedAt, time.Minute)

	// The recovery basis: ownership (whose grant, whose account the hold bills to) and the
	// reservation itself (the hold's ceiling and the locked versions the answer echoed).
	assert.Equal(t, "grant-reserve-1", record.GrantID)
	assert.Equal(t, "personal", record.PayerKind)
	assert.Equal(t, "user-reserve-1", record.PayerID)
	assert.Equal(t, int64(100), record.MaximumCredits)
	assert.Equal(t, 7, record.SnapshotVersion)
	assert.Equal(t, 3, record.RateVersion)

	// Only dispatchable work commits: explicitly free work reserves nothing, so it leaves
	// no commitment either.
	freeCtx := reserveTestCtx()
	_, shortCircuit, err = plugin.PreLLMHook(freeCtx, reserveChatRequest(reserveFreeModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "explicitly free work needs no reservation and proceeds")
	pending, err = plugin.dispatchCommitments.ListPending(freeCtx)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "the free dispatch added no commitment")
}

// TestDispatchCommitmentDuplicateClaimReturnsExistingAndBlocksSecondDispatch holds the second
// AC: a replayed dispatch of the same attempt - the same attempt coordinates reaching the funnel
// twice - returns the existing commitment instead of creating a second one, and the second
// dispatch is refused before any provider sees it.
func TestDispatchCommitmentDuplicateClaimReturnsExistingAndBlocksSecondDispatch(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))

	// The first dispatch: claimed and let through.
	ctx1 := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx1, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	before, err := plugin.dispatchCommitments.ListPending(ctx1)
	require.NoError(t, err)
	require.Len(t, before, 1)
	first := before[0]

	// The duplicate: the same attempt coordinates dispatched again (a replayed attempt, the
	// way a retried pipeline pass or a recovered context arrives - same request id, same
	// fallback index, same provider and model, so the same attempt identity).
	ctx2 := reserveTestCtx()
	_, shortCircuit, err = plugin.PreLLMHook(ctx2, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a duplicate dispatch of a claimed attempt is refused, not dispatched")
	require.NotNil(t, shortCircuit.Error, "a refused dispatch carries the reason")
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, true, ctx2.Value(governanceRejectedContextKey),
		"the refused duplicate is marked refused for everything downstream")
	assert.Equal(t, string(DecisionDispatchAlreadyClaimed), refusalDecisionType(t, shortCircuit.Error),
		"the duplicate refusal names its decision")
	require.NotNil(t, shortCircuit.Error.Error.Code)
	assert.Equal(t, CreditRefusalAdmissionUnreachable, *shortCircuit.Error.Error.Code,
		"a dispatch the admission machinery cannot vouch for answers in its category")
	require.NotNil(t, shortCircuit.Error.AllowFallbacks)
	assert.False(t, *shortCircuit.Error.AllowFallbacks,
		"the refusal is final: no provider on this attempt, none on any fallback")
	assert.Nil(t, ctx2.Value(CreditReservationContextKey),
		"a refused dispatch commits to nothing: no reservation is stamped on its context")

	// Still exactly one commitment for the attempt - the original, untouched.
	after, err := plugin.dispatchCommitments.ListPending(ctx2)
	require.NoError(t, err)
	require.Len(t, after, 1, "the duplicate claimed nothing new")
	assert.Equal(t, first, after[0], "the existing commitment is returned as-is, not re-claimed")
}

// refusalDecisionType returns the decision a refusal names in its error Type.
func refusalDecisionType(t *testing.T, bifrostErr *schemas.BifrostError) string {
	t.Helper()
	require.NotNil(t, bifrostErr, "the refusal carries an error")
	require.NotNil(t, bifrostErr.Type, "a credit refusal names its decision in error type")
	return *bifrostErr.Type
}

// replayableDispatchCommitmentStore stands in for a commitment backend that outlives the
// process: the records it holds survive the plugin instance that wrote them, the way a
// persistent store survives a crash. Both the pre-crash and the post-restart plugin are wired
// to the same backing, so what the restart test proves about recovery is proved through the
// production seam (the store interface), not through test-only paths.
type replayableDispatchCommitmentStore struct {
	mu      sync.Mutex
	records map[string]DispatchCommitment
}

func newReplayableDispatchCommitmentStore() *replayableDispatchCommitmentStore {
	return &replayableDispatchCommitmentStore{records: make(map[string]DispatchCommitment)}
}

func (s *replayableDispatchCommitmentStore) Claim(ctx context.Context, commitment DispatchCommitment) (DispatchCommitment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[commitment.AttemptID]; ok {
		return existing, false, nil
	}
	// A compliant backend writes the claim moment and the pending status with the record,
	// once, the way the default store does.
	commitment.Status = DispatchCommitmentStatusPending
	commitment.ClaimedAt = time.Now().UTC()
	s.records[commitment.AttemptID] = commitment
	return commitment, true, nil
}

func (s *replayableDispatchCommitmentStore) ListPending(ctx context.Context) ([]DispatchCommitment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]DispatchCommitment, 0, len(s.records))
	for _, record := range s.records {
		pending = append(pending, record)
	}
	return pending, nil
}

// TestDispatchCommitmentSurvivesRestartPendingAndBlocksRedispatch is the crash-point test: the
// process dies after the commitment is written and before the dispatch does anything. A
// restarted gateway finds the commitment still pending, does not re-send the attempt on its
// own, and a replay of the same attempt after the restart is refused - one attempt identity
// never dispatches twice, whatever happened to the process in between.
func TestDispatchCommitmentSurvivesRestartPendingAndBlocksRedispatch(t *testing.T) {
	durable := newReplayableDispatchCommitmentStore()
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())

	// Before the crash: the attempt is claimed.
	plugin1 := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	plugin1.dispatchCommitments = durable
	ctx1 := reserveTestCtx()
	_, shortCircuit, err := plugin1.PreLLMHook(ctx1, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the pre-crash dispatch proceeds over its claimed commitment")
	preCrash, err := durable.ListPending(ctx1)
	require.NoError(t, err)
	require.Len(t, preCrash, 1)

	// The crash: the process - and with it the plugin instance - is gone. The commitment
	// outlives it, still pending, with its claimedAt exactly as written.
	require.NoError(t, plugin1.Cleanup())

	// The restart: a fresh plugin over the same persistent backing. Recovery's first view:
	// the commitment is there, pending, untouched - nothing has re-sent the attempt.
	plugin2 := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	plugin2.dispatchCommitments = durable
	afterRestart, err := plugin2.dispatchCommitments.ListPending(reserveTestCtx())
	require.NoError(t, err)
	require.Len(t, afterRestart, 1, "the commitment survived the restart for recovery to scan")
	assert.Equal(t, preCrash[0].ClaimedAt, afterRestart[0].ClaimedAt,
		"the restarted gateway did not touch the record: no re-claim, no re-send")
	assert.Equal(t, DispatchCommitmentStatusPending, afterRestart[0].Status)

	// And if the same attempt coordinates arrive after the restart, the answer is the same
	// as it would have been without the crash: the attempt is already claimed, and a second
	// provider call under its identity is refused.
	ctx2 := reserveTestCtx()
	_, shortCircuit, err = plugin2.PreLLMHook(ctx2, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a replayed attempt is not re-dispatched after a restart")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, string(DecisionDispatchAlreadyClaimed), refusalDecisionType(t, shortCircuit.Error))
	afterReplay, err := durable.ListPending(ctx2)
	require.NoError(t, err)
	require.Len(t, afterReplay, 1, "the replay added no second commitment")
	assert.Equal(t, preCrash[0].ClaimedAt, afterReplay[0].ClaimedAt)
}

// failingDispatchCommitmentStore is a commitment backend that cannot write: every claim fails.
type failingDispatchCommitmentStore struct{}

func (failingDispatchCommitmentStore) Claim(ctx context.Context, commitment DispatchCommitment) (DispatchCommitment, bool, error) {
	return DispatchCommitment{}, false, errors.New("commitment store unavailable")
}

func (failingDispatchCommitmentStore) ListPending(ctx context.Context) ([]DispatchCommitment, error) {
	return nil, errors.New("commitment store unavailable")
}

// TestDispatchCommitmentStoreFailureRefusesDispatch holds the order rule from the fail-closed
// side: when the commitment cannot be persisted, the request is not dispatched. No commitment,
// no provider - whatever the reserve door answered.
func TestDispatchCommitmentStoreFailureRefusesDispatch(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	plugin.dispatchCommitments = failingDispatchCommitmentStore{}
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "an attempt without a persistable commitment is dispatched to no provider")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey))
	require.NotNil(t, shortCircuit.Error.Error.Code)
	assert.Equal(t, CreditRefusalAdmissionUnreachable, *shortCircuit.Error.Error.Code,
		"a commitment the machinery cannot write is an admission ending it cannot vouch for")
	require.NotNil(t, shortCircuit.Error.AllowFallbacks)
	assert.False(t, *shortCircuit.Error.AllowFallbacks)
	assert.Nil(t, ctx.Value(CreditReservationContextKey),
		"the failed dispatch is refused and carries no reservation stamp")
}

// TestDispatchCommitmentConcurrentClaimsElectOneWinner pins the idempotent claim under
// concurrency: many claims of one attempt identity elect exactly one winner and write exactly
// one commitment.
func TestDispatchCommitmentConcurrentClaimsElectOneWinner(t *testing.T) {
	store := newLocalDispatchCommitmentStore()
	record := DispatchCommitment{
		AttemptID:     "bfat-concurrent",
		ReservationID: "res-concurrent-1",
		Provider:      "openai",
		Model:         reservePaidModel,
	}

	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimed, err := store.Claim(context.Background(), record)
			require.NoError(t, err)
			if claimed {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), winners.Load(), "exactly one claim wins the attempt")
	pending, err := store.ListPending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1, "one attempt identity, one commitment")
	assert.Equal(t, "res-concurrent-1", pending[0].ReservationID)
	assert.Equal(t, DispatchCommitmentStatusPending, pending[0].Status)
	require.False(t, pending[0].ClaimedAt.IsZero(), "the store records when the winning claim happened")
}
