package governance

// Retry admission (07.5): every real provider attempt is a new admission. A retry is not a
// continuation of the attempt it retries - it is its own attempt, with its own identity, its
// own hold, and its own dispatch commitment - and nothing about the attempt it follows lets
// it (or anything after it) skip the funnel or ride a spent identity for free:
//
//   - Each real attempt of a request reaches the funnel under its own attempt identity. Core
//     runs the PreLLM hooks once per fallback slot and digests the slot into the identity
//     (creditAttemptID: provider, model, request id, billing nonce, fallback index), so each
//     slot reserves and claims anew; a slot's intra-slot physical retries re-enter no hook and
//     are that one attempt - one identity, one hold, one commitment.
//   - A spent identity dispatches nothing. Replaying an attempt's coordinates derives the same
//     identity, the claim finds the commitment already claimed, and the replay is refused
//     (dispatch_already_claimed) before any provider is called - one attempt identity, one
//     provider call, ever.
//   - The chain is traceable and the ownership does not move. Each commitment records its
//     coordinates (request id, billing nonce, fallback slot) and the attempt it retries (the
//     previous attempt of the same physical request that claimed, read from the reservation
//     stamp that attempt's own admission left), so the records alone reconstruct the chain -
//     which connects only the attempts that really dispatched - under one unchanging
//     grant-and-payer basis.
//   - Admission is not bypassed. A slot whose own admission fails dispatches nothing and
//     claims nothing, leaves no stamp of its own (so it enters no chain), and leaves the next
//     slot nothing to inherit: whatever arrives at the funnel next pays full admission again.
//
// The tests reuse the 07.2 harness (the armed extension, the funnel context, PreLLMHook as
// the dispatch verdict) over a numbered fake door: each NEW attempt identity the door sees is
// answered with a NEW hold, a replayed identity is answered with the hold its first admission
// already drew (06.7's same-id answer), and a one-shot refusal can be injected at a chosen
// arrival so a retry's own admission can fail. Core's per-slot pass is mirrored by advancing
// the context's fallback index and re-running the hook - exactly the state core hands the
// funnel per slot (clearCtxForFallback keeps the previous slot's reservation stamp, and that
// stamp is all the linkage the chain records).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryChainRequestID is the request id every attempt of the chain's physical request carries.
const retryChainRequestID = "req-retry-chain"

// retryChainNonce is the billing nonce the transport minted for the chain's physical request:
// one per HTTP request, stable across its fallback slots (clearCtxForFallback keeps it), so
// the slot - not the nonce - is what distinguishes the chain's attempt identities.
const retryChainNonce = "bfnonce-retry-chain-physical-request"

// retryChainCtx is the request context the funnel receives for one attempt of the chain: the
// transport's request id and nonce, and the slot the attempt dispatches, already settled - so
// the identity the admission derives is the one production would derive.
func retryChainCtx(nonce string, fallbackIndex int) *schemas.BifrostContext {
	ctx := presentCtx(reserveTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, retryChainRequestID)
	if nonce != "" {
		ctx.SetValue(schemas.BifrostContextKeyBillingNonce, nonce)
	}
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, fallbackIndex)
	return ctx
}

// runRetrySlot runs one fallback slot the way core hands it to the funnel: the context
// advances to the slot (the previous slot's reservation stamp survives), and the PreLLM hook's
// verdict is the dispatch verdict - a nil short circuit dispatches, a non-nil one does not.
func runRetrySlot(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext, slot int) *schemas.LLMPluginShortCircuit {
	t.Helper()
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, slot)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	return shortCircuit
}

// retryStamp reads the reservation stamp the funnel left on the context after an admitted
// attempt: what PostLLM settlement settles against, and where the next slot's chain link is
// read from.
func retryStamp(t *testing.T, ctx *schemas.BifrostContext) *CreditReservation {
	t.Helper()
	stamp, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok, "an admitted attempt stamps its reservation on the request context")
	return stamp
}

// commitmentForSlot finds a chain's commitment by the slot it dispatched.
func commitmentForSlot(commitments []DispatchCommitment, slot int) (DispatchCommitment, bool) {
	for _, commitment := range commitments {
		if commitment.FallbackIndex == slot {
			return commitment, true
		}
	}
	return DispatchCommitment{}, false
}

// retryNumberedDoor stands in for OpenBot's reserve door with the two behaviours the retry
// contract is read through: each NEW attempt identity is held anew (a new reservation id),
// while the SAME identity is answered with the hold its first admission drew - 06.7's same-id
// answer, so a replayed reserve mints no second hold - and an optional one-shot refusal makes
// exactly one chosen new identity's own admission fail.
type retryNumberedDoor struct {
	server *httptest.Server

	refuseKthNewIdentity int // 1-based arrival ordinal of the new identity to refuse; 0 refuses none

	mu       sync.Mutex
	seen     map[string]doorAnswer // identity -> the answer its first arrival drew, replayed verbatim
	arrivals []string              // distinct identities, in arrival order
	holds    map[string]string     // identity -> the reservation the door holds for it
	holdSeq  int
	refusals int
}

// doorAnswer is what the door answered one identity's first arrival, and answers every later
// arrival of the same identity.
type doorAnswer struct {
	status int
	body   string
}

func newRetryNumberedDoor(t *testing.T, refuseKthNewIdentity int) *retryNumberedDoor {
	t.Helper()
	door := &retryNumberedDoor{
		refuseKthNewIdentity: refuseKthNewIdentity,
		seen:                 make(map[string]doorAnswer),
		holds:                make(map[string]string),
	}
	door.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		identity, _ := body["idempotencyKey"].(string)

		door.mu.Lock()
		defer door.mu.Unlock()
		answer, answered := door.seen[identity]
		if !answered {
			if len(door.arrivals)+1 == door.refuseKthNewIdentity {
				// The chosen identity's own admission fails, once: this identity is refused
				// now and on any later arrival of it - a refused admission has no hold to
				// replay, so the door answers the refusal deterministically.
				door.refusals++
				answer = doorAnswer{
					status: http.StatusUnprocessableEntity,
					body:   refusedAnswer(CreditRefusalInsufficientBalance),
				}
			} else {
				// A new identity is held anew, under its own reservation.
				door.holdSeq++
				reservationID := fmt.Sprintf("res-retry-%d", door.holdSeq)
				door.holds[identity] = reservationID
				answer = doorAnswer{
					status: http.StatusCreated,
					body: fmt.Sprintf(`{"attemptId":"att-%s","reservationId":"%s","maximumCredits":100,"snapshotVersion":7,"rateVersion":3}`,
						reservationID, reservationID),
				}
			}
			door.seen[identity] = answer
			door.arrivals = append(door.arrivals, identity)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		_, _ = w.Write([]byte(answer.body))
	}))
	t.Cleanup(door.server.Close)
	return door
}

func (d *retryNumberedDoor) endpoint() string { return d.server.URL + "/reserve" }

// holdCount is how many reservations the door holds: one per admitted identity, never more.
func (d *retryNumberedDoor) holdCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.holdSeq
}

// holdForKey is the reservation the door holds for one identity (empty for a refused one).
func (d *retryNumberedDoor) holdForKey(identity string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.holds[identity]
}

// identityCount is how many distinct attempt identities have reached the door.
func (d *retryNumberedDoor) identityCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.arrivals)
}

// identities is every distinct identity that reached the door, in arrival order.
func (d *retryNumberedDoor) identities() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.arrivals...)
}

// refusalCount is how many admissions the door has refused.
func (d *retryNumberedDoor) refusalCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.refusals
}

// TestRetryAdmissionChainReservesAndCommitsAnewPerAttempt holds the first AC: two real
// attempts of one request - the primary and the retry that follows it - each reserve anew and
// each commit anew, under distinct attempt identities, with no reuse in either direction. The
// retry does not ride the primary's hold, and the primary does not answer for the retry.
func TestRetryAdmissionChainReservesAndCommitsAnewPerAttempt(t *testing.T) {
	door := newRetryNumberedDoor(t, 0)
	plugin := newReserveTestPlugin(t, admissionWithReserve(door.endpoint(), 0))
	ctx := retryChainCtx(retryChainNonce, 0)

	stamps := make([]*CreditReservation, 2)
	for slot := 0; slot < 2; slot++ {
		require.Nil(t, runRetrySlot(t, plugin, ctx, slot),
			"slot %d is a real attempt: its admission holds and it dispatches", slot)
		stamps[slot] = retryStamp(t, ctx)
	}

	assert.Equal(t, 2, door.identityCount(),
		"each real attempt reached the door under its own attempt identity")
	assert.Equal(t, 2, door.holdCount(),
		"the retry reserved again instead of reusing the primary's hold: one real attempt, one reservation")

	commitments, err := plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, commitments, 2, "two real attempts, two dispatch commitments")

	identities := make([]string, 0, 2)
	for slot := 0; slot < 2; slot++ {
		commitment, ok := commitmentForSlot(commitments, slot)
		require.True(t, ok, "slot %d dispatched, so its commitment is in the store", slot)
		identities = append(identities, commitment.AttemptID)

		assert.Regexp(t, `^bfat-[0-9a-f]{32}$`, commitment.AttemptID,
			"each attempt's commitment is keyed by a gateway-minted attempt identity")
		assert.Equal(t, stamps[slot].AttemptID, commitment.AttemptID,
			"the context stamp, the commitment and the identity the door saw are one identity per attempt")
		assert.Equal(t, door.holdForKey(commitment.AttemptID), commitment.ReservationID,
			"slot %d commits against its own hold, not another attempt's", slot)
	}
	assert.NotEqual(t, identities[0], identities[1],
		"two real attempts of one request mint two attempt identities")
	assert.NotEqual(t, door.holdForKey(identities[0]), door.holdForKey(identities[1]),
		"two real attempts of one request hold two reservations")
	assert.ElementsMatch(t, identities, door.identities(),
		"the identities on the wire are exactly the identities the records carry")
}

// TestRetryAdmissionReplayingASpentAttemptIdDispatchesNothing holds the second AC: the same
// attempt coordinates arriving again - the same request id, the same physical request's nonce,
// the same slot, so the same attempt identity - dispatch nothing. The spent identity's
// commitment is already claimed; the replay is refused before any provider is called, and
// leaves with no hold, no commitment, and no reservation stamp of its own.
func TestRetryAdmissionReplayingASpentAttemptIdDispatchesNothing(t *testing.T) {
	door := newRetryNumberedDoor(t, 0)
	plugin := newReserveTestPlugin(t, admissionWithReserve(door.endpoint(), 0))
	ctx := retryChainCtx(retryChainNonce, 0)

	require.Nil(t, runRetrySlot(t, plugin, ctx, 0), "the first dispatch of the attempt proceeds")
	original, err := plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, original, 1)
	holdsBeforeReplay := door.holdCount()

	// The replay: the same attempt coordinates again, as a retried pipeline pass or a
	// recovered context would carry them.
	replay := retryChainCtx(retryChainNonce, 0)
	shortCircuit := runRetrySlot(t, plugin, replay, 0)

	require.NotNil(t, shortCircuit,
		"a spent attemptId dispatches nothing: one attempt identity, one provider call, ever")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Type)
	assert.Equal(t, string(DecisionDispatchAlreadyClaimed), *shortCircuit.Error.Type,
		"the replay is refused because its identity is already claimed, not re-admitted")
	require.NotNil(t, shortCircuit.Error.StatusCode)
	assert.Equal(t, 503, *shortCircuit.Error.StatusCode)
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, true, replay.Value(governanceRejectedContextKey),
		"the replayed attempt is marked refused for everything downstream")
	require.NotNil(t, shortCircuit.Error.AllowFallbacks)
	assert.False(t, *shortCircuit.Error.AllowFallbacks,
		"the refusal is final: no provider on this attempt, none on any fallback after it")
	assert.Nil(t, replay.Value(CreditReservationContextKey),
		"the replay leaves with no reservation stamp: there is nothing held for it to settle against")

	after, err := plugin.dispatchCommitments.ListPending(replay)
	require.NoError(t, err)
	require.Len(t, after, 1, "the replay claimed nothing new")
	assert.Equal(t, original[0], after[0], "the original commitment stands untouched")
	assert.Equal(t, holdsBeforeReplay, door.holdCount(),
		"no second reservation: the replayed identity was answered under the key its first admission already drew")
	assert.Equal(t, []string{original[0].AttemptID}, door.identities(),
		"the door has still seen exactly one identity: the replay brought it no new one, only the spent one back")
}

// TestRetryAdmissionChainIsTraceableWithUnchangedOwnership holds the traceability AC: a
// three-slot chain is reconstructible from the commitments alone - each link records its own
// coordinates and names the attempt it retries, the links connect only the attempts that
// really dispatched, and every link bills to the same grant and payment account the request
// started under. The retry moves no ownership.
func TestRetryAdmissionChainIsTraceableWithUnchangedOwnership(t *testing.T) {
	door := newRetryNumberedDoor(t, 0)
	plugin := newReserveTestPlugin(t, admissionWithReserve(door.endpoint(), 0))
	ctx := retryChainCtx(retryChainNonce, 0)

	for slot := 0; slot < 3; slot++ {
		require.Nil(t, runRetrySlot(t, plugin, ctx, slot), "slot %d is a real attempt and dispatches", slot)
	}

	commitments, err := plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, commitments, 3, "three real attempts, three commitments")

	links := make([]DispatchCommitment, 0, 3)
	for slot := 0; slot < 3; slot++ {
		link, ok := commitmentForSlot(commitments, slot)
		require.True(t, ok, "slot %d dispatched, so its commitment is in the store", slot)
		links = append(links, link)

		// The coordinates: the slot the attempt dispatched, under the request and the
		// physical request's nonce the chain belongs to.
		assert.Equal(t, retryChainRequestID, link.RequestID)
		assert.Equal(t, retryChainNonce, link.BillingNonce)
		assert.Equal(t, slot, link.FallbackIndex)

		// The ownership: every link bills where the request's admission billed - the grant
		// and payment account are the request's, not any attempt's to move.
		assert.Equal(t, "grant-reserve-1", link.GrantID)
		assert.Equal(t, "personal", link.PayerKind)
		assert.Equal(t, "user-reserve-1", link.PayerID)
	}

	assert.NotEqual(t, links[0].AttemptID, links[1].AttemptID)
	assert.NotEqual(t, links[1].AttemptID, links[2].AttemptID)
	assert.NotEqual(t, links[0].AttemptID, links[2].AttemptID,
		"each slot of the chain is its own attempt identity")

	// The chain: root retries nothing; every later link names the attempt of this physical
	// request that claimed and dispatched immediately before it.
	assert.Empty(t, links[0].RetryOfAttemptID, "the chain's root retries nothing")
	assert.Equal(t, links[0].AttemptID, links[1].RetryOfAttemptID,
		"the first retry links to the attempt it followed")
	assert.Equal(t, links[1].AttemptID, links[2].RetryOfAttemptID,
		"each retry links to the attempt it followed, so the records alone reconstruct the chain")
}

// TestRetryAdmissionARetryWhoseAdmissionFailsExecutesNothing holds the no-bypass AC: a retry
// whose own admission fails dispatches nothing and claims nothing - and leaves the funnel
// nothing to inherit. The next slot arrives to a full admission again (its own identity, its
// own trip to the door, its own hold) even though a valid reservation stamp from an earlier
// slot is still on the context: an earlier slot's stamp admits nothing but its own attempt,
// and the refused slot - which never claimed - enters no chain.
func TestRetryAdmissionARetryWhoseAdmissionFailsExecutesNothing(t *testing.T) {
	// The second new identity to reach the door - the first retry slot - has its own
	// admission refused.
	door := newRetryNumberedDoor(t, 2)
	plugin := newReserveTestPlugin(t, admissionWithReserve(door.endpoint(), 0))
	ctx := retryChainCtx(retryChainNonce, 0)

	require.Nil(t, runRetrySlot(t, plugin, ctx, 0), "the root attempt dispatches over its own admission")
	root := retryStamp(t, ctx)

	// The retry whose admission fails.
	shortCircuit := runRetrySlot(t, plugin, ctx, 1)
	require.NotNil(t, shortCircuit, "a retry whose admission failed dispatches nothing")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *shortCircuit.Error.Type)
	assert.Contains(t, shortCircuit.Error.Error.Message, CreditRefusalInsufficientBalance,
		"the refusal says the retry's own admission failed, at the door, like any other attempt's")
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"the refused retry is marked refused for everything downstream")

	// It executed nothing: no hold was drawn for it, no commitment was claimed for it, and
	// its refusal wrote no stamp of its own.
	assert.Equal(t, 1, door.holdCount(), "the refused retry held nothing")
	assert.Equal(t, 2, door.identityCount(), "the refused retry still paid a full admission: its own identity reached the door and was refused there")
	assert.Equal(t, 1, door.refusalCount())
	commitments, err := plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	_, claimed := commitmentForSlot(commitments, 1)
	assert.False(t, claimed, "the refused retry claimed nothing")
	require.Len(t, commitments, 1)
	assert.Same(t, root, retryStamp(t, ctx),
		"the refusal wrote no stamp of its own: the context still carries the root attempt's reservation")

	// The next slot inherits nothing: despite the still-valid stamp on the context, its
	// admission runs in full - its own identity, its own hold - before it dispatches.
	require.Nil(t, runRetrySlot(t, plugin, ctx, 2),
		"the next slot is admitted on its own merits, not on the earlier attempt's stamp")
	assert.Equal(t, 3, door.identityCount(), "the next slot reached the door under its own identity")
	assert.Equal(t, 2, door.holdCount(), "the next slot held anew: no attempt rides another's reservation")

	commitments, err = plugin.dispatchCommitments.ListPending(ctx)
	require.NoError(t, err)
	require.Len(t, commitments, 2, "two real attempts, two commitments: the refused slot is in neither list")
	resumed, ok := commitmentForSlot(commitments, 2)
	require.True(t, ok)
	rootLink, ok := commitmentForSlot(commitments, 0)
	require.True(t, ok)
	assert.Equal(t, rootLink.AttemptID, resumed.RetryOfAttemptID,
		"the chain links around the refused slot: it never claimed, so it is not an attempt that dispatched")
	assert.Equal(t, door.holdForKey(resumed.AttemptID), resumed.ReservationID,
		"the resumed attempt settles against its own hold")
	assert.NotSame(t, root, retryStamp(t, ctx),
		"the resumed attempt stamped its own reservation, replacing the root's")
}

// TestRetryAdmissionAttemptIdentityMapsFallbackSlotsNotRetryCounters pins the mapping between
// the identity and the attempt it names: the identity is a pure function of the attempt's slot
// coordinates - provider, model, request id, billing nonce, fallback slot - and of nothing
// else. The same coordinates derive the same identity (so a replay is recognisable as one),
// each new slot derives a new one (so a retry is a new admission), and the routing machinery's
// intra-slot attempt counters are invisible to it (so a slot's physical retries are that one
// attempt, not new admissions).
func TestRetryAdmissionAttemptIdentityMapsFallbackSlotsNotRetryCounters(t *testing.T) {
	ctx := retryChainCtx(retryChainNonce, 0)

	primary := creditAttemptID(ctx, schemas.OpenAI, reservePaidModel)
	assert.Regexp(t, `^bfat-[0-9a-f]{32}$`, primary)
	assert.Equal(t, primary, creditAttemptID(ctx, schemas.OpenAI, reservePaidModel),
		"the same slot coordinates derive the same identity: a replay of the attempt is recognisable as one")

	slotIdentity := make(map[int]string)
	for slot := 0; slot < 3; slot++ {
		ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, slot)
		slotIdentity[slot] = creditAttemptID(ctx, schemas.OpenAI, reservePaidModel)
	}
	assert.NotEqual(t, slotIdentity[0], slotIdentity[1])
	assert.NotEqual(t, slotIdentity[1], slotIdentity[2])
	assert.NotEqual(t, slotIdentity[0], slotIdentity[2],
		"each fallback slot of the request derives its own attempt identity: a retry is a new admission")

	otherRequest := retryChainCtx("bfnonce-another-physical-request", 0)
	assert.NotEqual(t, primary, creditAttemptID(otherRequest, schemas.OpenAI, reservePaidModel),
		"another physical request's nonce derives another identity: the identity is not the caller's to forge")

	// The routing machinery's intra-slot counters - a physical retry's attempt number, the
	// load balancer's per-attempt decisions - are not coordinates of the identity: they
	// change within a slot without the slot's admission changing.
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	ctx.SetValue(schemas.BifrostContextKeyLoadBalancerAttempt, map[string]string{"bf-lb-attempt": "1"})
	assert.Equal(t, slotIdentity[0], creditAttemptID(ctx, schemas.OpenAI, reservePaidModel),
		"the identity maps fallback slots, not retry counters: a slot's physical retries are that one attempt")

	// One identity, three surfaces: what the reserve carried as its idempotency key, what
	// the commitment is keyed by, and what the context stamps are the same string - so
	// reserve, commitment and settlement are correlatable per attempt without a fourth
	// identity minted anywhere.
	door := newRetryNumberedDoor(t, 0)
	plugin := newReserveTestPlugin(t, admissionWithReserve(door.endpoint(), 0))
	admitted := retryChainCtx(retryChainNonce, 0)
	require.Nil(t, runRetrySlot(t, plugin, admitted, 0))
	stamp := retryStamp(t, admitted)
	commitments, err := plugin.dispatchCommitments.ListPending(admitted)
	require.NoError(t, err)
	require.Len(t, commitments, 1)
	assert.Equal(t, stamp.AttemptID, commitments[0].AttemptID)
	assert.Equal(t, []string{stamp.AttemptID}, door.identities(),
		"the reserve's idempotency key, the commitment's key and the stamp are one attempt identity")
}
