// The PostLLM settlement stage (07.8): the tests hold the stage to the one order that
// makes the money safe - the actual usage is PERSISTED in the result outbox before the
// response ends, and the delivery to OpenBot's Settle is reliable: a delivery that fails
// leaves the record in the retry queue with its evidence intact, never lost, never
// silently dropped. The outbox record carries what recovery (08.x) needs to finish a
// settlement the process did not survive: the ownership, the reservation, and the locked
// versions the reserve answer echoed.
//
// The settle message itself carries no price and no account: the amount is converted
// through the LOCKED basis the reservation recorded at admission (06.5), server-side -
// the gateway reports usage, OpenBot prices it.
package governance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

// settleServiceSecret is the trusted service identity the settle door expects (the same
// 06.1 credential discipline the reserve door is held to).
const settleServiceSecret = "obot_csvc_settletestsecret"

// fakeSettleDoor stands in for OpenBot's Settle surface: it records every call and
// answers the status the test arms. A nil/empty status list answers 200 forever; a
// blocking door never answers at all, so a test can prove what is already true by the
// time PostLLMHook returns.
type fakeSettleDoor struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    int
	lastAuth string
	lastBody map[string]any
	bodies   []map[string]any

	statuses []int // popped per call; empty means always 200
	block    chan struct{}
}

func newFakeSettleDoor(t *testing.T, statuses ...int) *fakeSettleDoor {
	t.Helper()
	fake := &fakeSettleDoor{statuses: statuses}
	if fake.statuses == nil {
		fake.statuses = []int{}
	}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.calls++
		fake.lastAuth = r.Header.Get("Authorization")
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err == nil {
			fake.lastBody = raw
			fake.bodies = append(fake.bodies, raw)
		}
		status := http.StatusOK
		if len(fake.statuses) > 0 {
			status = fake.statuses[0]
			fake.statuses = fake.statuses[1:]
		}
		blocked := fake.block
		fake.mu.Unlock()

		if blocked != nil {
			<-blocked // the door never answers: the caller's order is on its own
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"status":"settled","reservationId":"cr-0000000000000001","attemptId":"att-0000000000000001","chargedCredits":1,"appliedCredits":1,"releasedCredits":99,"actualCost":{"numerator":"1","denominator":"1"},"remainder":{"numerator":"0","denominator":"1"}}`))
		} else {
			_, _ = w.Write([]byte(`{"error":"refused","code":"settlement_conflict","detail":{}}`))
		}
	}))
	t.Cleanup(func() {
		fake.unblock()
		fake.server.Close()
	})
	return fake
}

func (f *fakeSettleDoor) unblock() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.block != nil {
		close(f.block)
		f.block = nil
	}
}

func (f *fakeSettleDoor) hang() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = make(chan struct{})
}

func (f *fakeSettleDoor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSettleDoor) snapshot() (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth, f.lastBody
}

func (f *fakeSettleDoor) allBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.bodies))
	copy(out, f.bodies)
	return out
}

func (f *fakeSettleDoor) endpoint() string { return f.server.URL + "/settle" }

// waitFor polls a condition the delivery goroutine drives, bounded so a regression fails
// the test instead of hanging it.
func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", message)
}

// admissionWithReserveAndSettle arms the extension the way a strict deployment is wired:
// the reserve door and the settle door, both pointed at fakes, sharing the trusted
// identity discipline.
func admissionWithReserveAndSettle(reserveEndpoint, settleEndpoint string, timeoutMs int) *CreditAdmissionConfig {
	config := admissionWithReserve(reserveEndpoint, timeoutMs)
	config.Settle = &CreditSettleConfig{
		Endpoint:      settleEndpoint,
		ServiceSecret: settleServiceSecret,
		TimeoutMs:     timeoutMs,
	}
	return config
}

// settledChatResponse is a successful non-stream chat response whose usage the provider
// reported: 600 prompt tokens (100 of them cache reads, 50 cache writes) and 400
// completion tokens.
func settledChatResponse() *schemas.BifrostResponse {
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     600,
				CompletionTokens: 400,
				TotalTokens:      1000,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedReadTokens:  100,
					CachedWriteTokens: 50,
				},
			},
		},
	}
	// Core stamps these on every response before the post hooks read them.
	response.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	return response
}

// settleAttemptFor drives one admitted attempt the way the pipeline does: the reserve door
// holds in PreLLMHook, the provider answers, PostLLMHook settles. Returns the context so
// the caller can read the reservation the attempt was admitted under.
func settleAttemptFor(t *testing.T, plugin *GovernancePlugin, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) *schemas.BifrostContext {
	t.Helper()
	ctx := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the reserve held; the attempt dispatches")
	_, _, err = plugin.PostLLMHook(ctx, result, bifrostErr)
	require.NoError(t, err)
	return ctx
}

// --- the one order that matters ---------------------------------------------------------

func TestPostLLMSettlementOutboxPersistsTheRecordBeforeTheResponseEnds(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t)
	door.hang() // the delivery never completes: whatever is true on return is what persisted
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)

	// No sleep, no wait: by the time PostLLMHook returned - the moment the caller's
	// response ends - the record must already be in the outbox.
	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the settlement is persisted before the response ends, not after")
	record := pending[0]

	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok)
	assert.Equal(t, reservation.AttemptID, record.AttemptID, "the record carries the attempt identity")
	assert.Equal(t, "res-reserve-1", record.ReservationID)
	assert.Equal(t, int64(600-100-50), record.Usage.Input, "input is the prompt minus its cached parts")
	assert.Equal(t, int64(400), record.Usage.Output)
	assert.Equal(t, int64(100), record.Usage.CacheRead)
	assert.Equal(t, int64(50), record.Usage.CacheWrite)
	assert.Equal(t, 7, record.SnapshotVersion, "the locked versions ride the record")
	assert.Equal(t, 3, record.RateVersion)
	assert.Equal(t, "grant-reserve-1", record.GrantID, "recovery can answer whose call this was")
	assert.Equal(t, "personal", record.PayerKind)
	assert.Equal(t, "user-reserve-1", record.PayerID)
	assert.Equal(t, int64(100), record.MaximumCredits)
	assert.Equal(t, SettlementOutcomeProviderUsageReport, record.Outcome)
	assert.Equal(t, SettlementStatusPending, record.Status, "an undelivered record stays in the retry queue")

	// The delivery was attempted (the goroutine is in flight); the record's persistence
	// above is the order under test, the delivery just has to be on its way.
	waitFor(t, func() bool { return door.callCount() == 1 }, "the delivery reaches the settle door")
}

func TestSettlementOutboxDeliversTheLockedSettleMessage(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()

	require.Equal(t, 1, door.callCount(), "one settle per attempt")
	auth, body := door.snapshot()
	assert.Equal(t, "Bearer "+settleServiceSecret, auth, "the settle authenticates as the trusted billing identity")
	require.NotNil(t, body)
	assert.Equal(t, "res-reserve-1", body["reservationId"], "the reservation is named, never an account")
	assert.Nil(t, body["payer"], "no account field rides the settle: the account is the reservation's")
	assert.Nil(t, body["amount"], "no price rides the settle: OpenBot prices through the locked basis")
	assert.Equal(t, map[string]any{
		"input": float64(450), "output": float64(400), "reasoning": float64(0),
		"image": float64(0), "image_output": float64(0),
		"audio_input": float64(0), "audio_output": float64(0),
		"cache_read": float64(100), "cache_write": float64(50),
		"video": float64(0), "surcharge_web_search": float64(0), "surcharge_per_request": float64(0),
	}, body["actualUsage"], "every dimension of the closed vocabulary is reported, zero where nothing was consumed")
	assert.Equal(t, map[string]any{"kind": "provider_usage_report", "reference": "req-reserve-1"}, body["evidence"],
		"the usage stands on the provider's report, referenced to the request it answered")
	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok)
	assert.Equal(t, reservation.AttemptID, body["idempotencyKey"],
		"the settle message's retry identity is the attempt identity: a redelivery replays the same message")

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a delivered settlement leaves the retry queue")
}

// --- delivery failure never loses the message ------------------------------------------

func TestSettlementOutboxDeliveryFailureEntersTheRetryQueueWithoutLoss(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusServiceUnavailable, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed delivery stays in the retry queue, not dropped")
	assert.Equal(t, SettlementStatusPending, pending[0].Status)
	assert.Equal(t, 1, pending[0].DeliveryAttempts)
	assert.NotEmpty(t, pending[0].LastErrorCode, "the failure names its class")

	// The retry queue is driven again: the same message, the same idempotency key.
	require.NoError(t, plugin.deliverPendingSettlements(ctx))
	assert.Equal(t, 2, door.callCount())
	bodies := door.allBodies()
	require.Len(t, bodies, 2)
	assert.Equal(t, bodies[0], bodies[1], "the retry sends the same message OpenBot already may have answered")

	pending, err = plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "the succeeded retry leaves the queue")
}

func TestSettlementOutboxRefusedDeliveryBlocksAsEvidence(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusUnprocessableEntity)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()

	// A 422 is a deterministic refusal (the message or the reservation state will not heal
	// by retrying): the record is blocked, kept whole as evidence for recovery.
	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a blocked record is not retried by the queue")

	all, ok := plugin.settlementOutbox.(*localSettlementOutbox)
	require.True(t, ok)
	blocked, err := all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, blocked, 1)
	assert.Equal(t, SettlementStatusBlocked, blocked[0].Status)
	assert.NotEmpty(t, blocked[0].LastErrorCode)
	assert.Equal(t, "res-reserve-1", blocked[0].ReservationID, "the evidence stays complete")

	assert.Equal(t, 1, door.callCount(), "a blocked record is not sent again")
	require.NoError(t, plugin.deliverPendingSettlements(ctx))
	assert.Equal(t, 1, door.callCount())
}

// --- duplicate results, duplicate acks: one settlement, counted arrivals (07.9) ---------

func TestPostLLMDuplicateResultRecordsOnceAndNeverRedrivesTheDelivery(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()
	require.Equal(t, 1, door.callCount(), "the attempt's one delivery is on its way or done")

	// The same attempt's result arrives again - a replayed ending the post hook already
	// settled (a retried post hook, a re-acknowledged final chunk). The first record
	// stands; the duplicate is counted, and drives nothing.
	_, _, err := plugin.PostLLMHook(ctx, settledChatResponse(), nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	all, ok := plugin.settlementOutbox.(*localSettlementOutbox)
	require.True(t, ok)
	records, err := all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1, "a duplicate result records nothing new")
	record := records[0]
	assert.Equal(t, SettlementStatusDelivered, record.Status)
	assert.Equal(t, 1, record.DuplicateEvents, "the duplicate result is counted on the record it duplicated")
	assert.Equal(t, SettlementDuplicateSourceResult, record.LastDuplicateSource)
	assert.Equal(t, 1, door.callCount(),
		"the duplicate result drives no second delivery: the winner's delivery is the only one")
	mockLogger, ok := plugin.logger.(*MockLogger)
	require.True(t, ok)
	found := false
	for _, warning := range mockLogger.warnings {
		if strings.Contains(warning, "duplicate settlement result") {
			found = true
		}
	}
	assert.True(t, found, "the duplicate result is observable in the log, not silently swallowed")
}

func TestSettlementOutboxDuplicateAckAndLateFailureRecordOnceAndChangeNothing(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()
	require.Equal(t, 1, door.callCount())

	all, ok := plugin.settlementOutbox.(*localSettlementOutbox)
	require.True(t, ok)
	records, err := all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1)
	attemptID := records[0].AttemptID
	require.Equal(t, SettlementStatusDelivered, records[0].Status)

	// A duplicate ack: the same delivery's success reported again (a 200 that raced the
	// queue). It settles nothing new and is counted, once, on the existing record.
	require.NoError(t, plugin.settlementOutbox.MarkDelivered(ctx, attemptID))

	// A late failure report from the same race: the record is out of the queue, so it
	// schedules no retry and counts as an arrival, not an ending.
	attempts, err := plugin.settlementOutbox.RecordDeliveryFailure(ctx, attemptID, settlementFailureTimeout)
	require.NoError(t, err)
	assert.Equal(t, 0, attempts, "a late failure for a delivered record schedules no retry")

	// A block arriving after the ack: evidence lost the race; it changes nothing.
	require.NoError(t, plugin.settlementOutbox.MarkBlocked(ctx, attemptID, settlementFailureRefused))

	records, err = all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1)
	final := records[0]
	assert.Equal(t, SettlementStatusDelivered, final.Status,
		"a duplicate ack and late reports cannot un-deliver or block a settled record")
	assert.Empty(t, final.LastErrorCode)
	assert.Equal(t, 3, final.DuplicateEvents,
		"each of the three duplicate arrivals (ack, late failure, block) is counted once on the record")
	assert.Equal(t, SettlementDuplicateSourceDeliveryReport, final.LastDuplicateSource)

	// The queue does not re-drive a delivered record, whatever reported twice.
	require.NoError(t, plugin.deliverPendingSettlements(ctx))
	assert.Equal(t, 1, door.callCount(), "a delivered record is never delivered again by the queue")
}

func TestSettlementOutboxLostAckRedeliveryConvergesToOneSettlement(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t)
	door.hang() // the first send's ack is lost: the door never answers it
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 50))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait() // the first send times out; the record stays in the queue with its evidence

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, 1, pending[0].DeliveryAttempts)
	assert.Equal(t, settlementFailureTimeout, pending[0].LastErrorCode)

	// The ack was lost, not the settlement: the door had processed the first send and
	// answers the redelivery. The same message under the same idempotency key converges.
	door.unblock()
	require.NoError(t, plugin.deliverPendingSettlements(ctx))
	assert.Equal(t, 2, door.callCount())
	bodies := door.allBodies()
	require.Len(t, bodies, 2)
	assert.Equal(t, bodies[0], bodies[1],
		"the redelivery replays the same message OpenBot may already have settled")
	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok)
	assert.Equal(t, reservation.AttemptID, bodies[0]["idempotencyKey"],
		"both sends carry one charge identity: OpenBot's fence settles it once")

	pending, err = plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	all, ok := plugin.settlementOutbox.(*localSettlementOutbox)
	require.True(t, ok)
	records, err := all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1, "the lost ack and its redelivery are one settlement, not two")
	assert.Equal(t, SettlementStatusDelivered, records[0].Status)
	assert.Equal(t, 1, records[0].DeliveryAttempts, "the lost ack stays on the record as delivery evidence")
	assert.Equal(t, 0, records[0].DuplicateEvents,
		"a redelivery the queue owed while pending is the queue working, not a duplicate")
}

func TestSettlementOutboxThreeDeliveriesChargeOnce(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()

	// Two failed sends, then the acked one: three deliveries of the same message.
	require.NoError(t, plugin.deliverPendingSettlements(ctx))
	require.NoError(t, plugin.deliverPendingSettlements(ctx))

	assert.Equal(t, 3, door.callCount(), "the queue keeps delivering until the door acks")
	bodies := door.allBodies()
	require.Len(t, bodies, 3)
	assert.Equal(t, bodies[0], bodies[1])
	assert.Equal(t, bodies[1], bodies[2],
		"all three deliveries are the same message under one idempotency key: one charge on OpenBot's fence")

	all, ok := plugin.settlementOutbox.(*localSettlementOutbox)
	require.True(t, ok)
	records, err := all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1, "three deliveries settle one record")
	assert.Equal(t, SettlementStatusDelivered, records[0].Status)
	assert.Equal(t, 2, records[0].DeliveryAttempts)
}

// --- crash: the record outlives the process ---------------------------------------------

// durableSettlementOutbox is an outbox backend that outlives the plugin instance, the way
// a persistent store survives a crash. Both the pre-crash and post-restart plugins are
// wired to the same backing, so the crash test proves its claim through the production
// seam, not a test-only path.
type durableSettlementOutbox struct {
	mu      sync.Mutex
	records map[string]SettlementOutboxRecord
}

func newDurableSettlementOutbox() *durableSettlementOutbox {
	return &durableSettlementOutbox{records: make(map[string]SettlementOutboxRecord)}
}

func (s *durableSettlementOutbox) Record(_ context.Context, record SettlementOutboxRecord) (SettlementOutboxRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[record.AttemptID]; ok {
		countDuplicateArrival(&existing, SettlementDuplicateSourceResult)
		s.records[record.AttemptID] = existing
		return existing, false, nil
	}
	record.Status = SettlementStatusPending
	record.RecordedAt = time.Now().UTC()
	s.records[record.AttemptID] = record
	return record, true, nil
}

func (s *durableSettlementOutbox) Pending(_ context.Context) ([]SettlementOutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pending []SettlementOutboxRecord
	for _, record := range s.records {
		if record.Status == SettlementStatusPending {
			pending = append(pending, record)
		}
	}
	return pending, nil
}

func (s *durableSettlementOutbox) MarkDelivered(_ context.Context, attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[attemptID]
	if !ok {
		return errSettlementRecordMissing
	}
	if record.Status != SettlementStatusPending {
		countDuplicateArrival(&record, SettlementDuplicateSourceDeliveryReport)
		s.records[attemptID] = record
		return nil
	}
	record.Status = SettlementStatusDelivered
	s.records[attemptID] = record
	return nil
}

func (s *durableSettlementOutbox) RecordDeliveryFailure(_ context.Context, attemptID string, code string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[attemptID]
	if !ok {
		return 0, errSettlementRecordMissing
	}
	if record.Status != SettlementStatusPending {
		countDuplicateArrival(&record, SettlementDuplicateSourceDeliveryReport)
		s.records[attemptID] = record
		return record.DeliveryAttempts, nil
	}
	record.DeliveryAttempts++
	record.LastErrorCode = code
	s.records[attemptID] = record
	return record.DeliveryAttempts, nil
}

func (s *durableSettlementOutbox) MarkBlocked(_ context.Context, attemptID string, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[attemptID]
	if !ok {
		return errSettlementRecordMissing
	}
	if record.Status != SettlementStatusPending {
		countDuplicateArrival(&record, SettlementDuplicateSourceDeliveryReport)
		s.records[attemptID] = record
		return nil
	}
	record.Status = SettlementStatusBlocked
	record.LastErrorCode = code
	s.records[attemptID] = record
	return nil
}

func TestSettlementOutboxRecordSurvivesACrashBeforeDelivery(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t)
	door.hang()
	durable := newDurableSettlementOutbox()

	// Before the crash: the usage is persisted; the delivery is still in flight.
	plugin1 := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
	plugin1.settlementOutbox = durable
	ctx1 := settleAttemptFor(t, plugin1, settledChatResponse(), nil)
	pending, err := durable.Pending(ctx1)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	preCrash := pending[0]

	// The crash: the process (and the in-flight delivery with it) is gone.
	require.NoError(t, plugin1.Cleanup())
	door.unblock()

	// The restart: recovery's first view. The record is there, complete - the ownership,
	// the reservation and the locked versions it needs to finish the settlement.
	plugin2 := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
	plugin2.settlementOutbox = durable
	afterRestart, err := plugin2.settlementOutbox.Pending(reserveTestCtx())
	require.NoError(t, err)
	require.Len(t, afterRestart, 1, "the record survived the crash for recovery to finish")
	survivor := afterRestart[0]
	assert.Equal(t, preCrash.AttemptID, survivor.AttemptID)
	assert.Equal(t, preCrash.RecordedAt, survivor.RecordedAt, "the record was not re-written by the restart")
	assert.Equal(t, "res-reserve-1", survivor.ReservationID)
	assert.Equal(t, "grant-reserve-1", survivor.GrantID)
	assert.Equal(t, "personal", survivor.PayerKind)
	assert.Equal(t, "user-reserve-1", survivor.PayerID)
	assert.Equal(t, int64(100), survivor.MaximumCredits)
	assert.Equal(t, 7, survivor.SnapshotVersion)
	assert.Equal(t, 3, survivor.RateVersion)
	assert.Equal(t, int64(450), survivor.Usage.Input)
	assert.Equal(t, int64(400), survivor.Usage.Output)
	assert.Equal(t, SettlementOutcomeProviderUsageReport, survivor.Outcome)
}

// --- what settles and what holds ---------------------------------------------------------

func TestPostLLMSettlementOutboxWithoutUsableUsageHoldsTheReservation(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	// A success whose provider reported no usage at all: the result is unknown, not free.
	unknown := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{},
	}
	unknown.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	ctx := settleAttemptFor(t, plugin, unknown, nil)
	plugin.wg.Wait()

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the unknown result is persisted for recovery, not dropped")
	assert.Equal(t, SettlementOutcomeUsageUnknown, pending[0].Outcome)
	assert.Equal(t, SettlementStatusPending, pending[0].Status,
		"an unknown result is never delivered as a zero: the hold stays until evidence says otherwise")
	assert.Equal(t, 0, door.callCount(), "no settle message is sent on an unknown result")
}

func TestPostLLMSettlementOutboxIgnoresNonFinalStreamChunks(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	// A mid-stream chunk: the usage is not final, nothing is recorded yet.
	midStream := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
	midStream.PopulateExtraFields(schemas.ChatCompletionStreamRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	_, _, err = plugin.PostLLMHook(ctx, midStream, nil)
	require.NoError(t, err)
	plugin.wg.Wait()
	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a mid-stream chunk settles nothing")
	assert.Equal(t, 0, door.callCount())

	// The final chunk carries the stream's real usage: exactly one settlement.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	final := settledChatResponse()
	final.PopulateExtraFields(schemas.ChatCompletionStreamRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	_, _, err = plugin.PostLLMHook(ctx, final, nil)
	require.NoError(t, err)
	plugin.wg.Wait()
	pending, err = plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "the final chunk's settlement is delivered")
	assert.Equal(t, 1, door.callCount(), "one settle for the stream, on its final usage")
}

func TestSettlementOutboxIsNotWrittenWithoutAReservation(t *testing.T) {
	// The explicitly free model reserves nothing (07.2): PostLLM settles nothing.
	free := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
	free.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, reserveFreeModel, reserveFreeModel)

	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := reserveTestCtx()
	req := reserveChatRequest(reserveFreeModel)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "an explicitly free model needs no reservation")
	assert.Nil(t, ctx.Value(CreditReservationContextKey))
	_, _, err = plugin.PostLLMHook(ctx, free, nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a request that reserved nothing writes no settlement record")
	assert.Equal(t, 0, door.callCount())
}

// --- the usage extraction, pure ----------------------------------------------------------

func TestSettlementOutboxUsageMapsTheClosedDimensions(t *testing.T) {
	t.Run("chat usage maps input minus cache and the provider cap is held", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens:     600,
			CompletionTokens: 400,
			TotalTokens:      1000,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				CachedReadTokens:  100,
				CachedWriteTokens: 50,
			},
			ToolUsage: &schemas.ToolUsage{WebSearch: &schemas.WebSearchToolUsage{NumRequests: 2}},
		}
		mapped := settlementUsageFromLLM(usage, 0)
		assert.Equal(t, int64(450), mapped.Input)
		assert.Equal(t, int64(400), mapped.Output)
		assert.Equal(t, int64(100), mapped.CacheRead)
		assert.Equal(t, int64(50), mapped.CacheWrite)
		assert.Equal(t, int64(2), mapped.SurchargeWebSearch)
		assert.Equal(t, int64(0), mapped.Reasoning,
			"reasoning tokens are inside completion (Bifrost prices them there); the dim stays zero rather than billing them twice")

		capped := settlementUsageFromLLM(usage, 300)
		assert.Equal(t, int64(300), capped.Output, "the attested output cap is what the settle may book")
		assert.Equal(t, int64(450), capped.Input, "the cap holds output only")
	})

	t.Run("malformed cache details cannot go negative", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				CachedReadTokens:  999,
				CachedWriteTokens: 999,
			},
		}
		mapped := settlementUsageFromLLM(usage, 0)
		assert.Equal(t, int64(0), mapped.Input)
		assert.Equal(t, int64(10), mapped.CacheRead, "clamped to the prompt the same way pricing clamps it")
		assert.Equal(t, int64(0), mapped.CacheWrite)
	})

	t.Run("responses API usage maps through its own shape", func(t *testing.T) {
		usage := &schemas.ResponsesResponseUsage{
			InputTokens:         500,
			InputTokensDetails:  &schemas.ResponsesResponseInputTokens{CachedReadTokens: 200},
			OutputTokens:        300,
			OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 120},
		}
		mapped := settlementUsageFromResponses(usage, 0)
		assert.Equal(t, int64(300), mapped.Input)
		assert.Equal(t, int64(200), mapped.CacheRead)
		assert.Equal(t, int64(300), mapped.Output)
	})

	t.Run("billed usage on a failed attempt settles the same way", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{PromptTokens: 40, TotalTokens: 40}
		mapped := settlementUsageFromLLM(usage, 0)
		assert.Equal(t, int64(40), mapped.Input)
		assert.Equal(t, int64(0), mapped.Output)
	})
}

// --- wiring posture -----------------------------------------------------------------------

func TestSettlementOutboxWithoutADoorStillPersistsForRecovery(t *testing.T) {
	// A deployment that wired the reserve but not the settle door loses nothing: the
	// record is persisted (fail-durable) and simply not delivered - never dropped,
	// never a hole in the ledger's evidence.
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(reserve.endpoint(), 0))

	ctx := settleAttemptFor(t, plugin, settledChatResponse(), nil)
	plugin.wg.Wait()

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the record outlives the missing door")
	assert.Equal(t, SettlementStatusPending, pending[0].Status)
}
