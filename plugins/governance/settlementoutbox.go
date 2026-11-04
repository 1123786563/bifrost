// The result outbox and the PostLLM settlement stage (07.8): the record that carries an
// attempt's actual usage from the post hook to OpenBot's Settle, and the delivery that
// gets it there reliably.
//
// Why the order is fixed. The usage is PERSISTED before the response ends: PostLLMHook's
// return is the moment the caller's response is on its way, and a settlement written only
// after that - asynchronously, like the in-memory budget accounting - would be lost by any
// crash in between, taking the reservation's evidence with it. So the record is written
// synchronously inside the hook, and only the DELIVERY is asynchronous: a delivery that
// fails, or a process that dies mid-flight, leaves the record where recovery (08.x) can
// find it, with everything finishing the settlement needs - whose call it was (the grant
// and payment account the reserve carried), what was held (the reservation and its
// ceiling), and at what locked terms it must be priced (the versions the reserve answer
// echoed).
//
// Why the message carries no price and no account. OpenBot prices the settlement through
// the LOCKED basis the reservation recorded at admission (06.5) and resolves the account
// from the reservation's own row (06.4): a gateway-reported amount or account key would
// be refused, and the outbox never carries one.
//
// Why one attempt is one message. The record's identity is the attempt identity
// (creditadmission.go's bfat- digest) - the same identity the reserve was idempotent
// under and the dispatch commitment was claimed under. One attempt persists one record,
// whatever replays; every redelivery of that record is the SAME message under the SAME
// idempotency key, which is what lets a lost acknowledgement converge on the first result
// (06.9 fences the charge on the reservation itself).
//
// The store is a seam, not a backend, exactly like the dispatch commitment's. The default
// implementation keeps records in the process; a deployment that needs settlements to
// survive restarts wires a persistent backend under the same interface, and every rule in
// this file holds of it: record once by attempt identity, keep failed deliveries in the
// retry queue with their evidence, never lose a record silently.
package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// The delivery states of a settlement record. They mirror the usage outbox's vocabulary:
// pending is the retry queue, delivered is done, blocked is terminal evidence a retry
// cannot fix - kept whole for recovery (08.x) and audit, never deleted.
const (
	SettlementStatusPending   = "pending"
	SettlementStatusDelivered = "delivered"
	SettlementStatusBlocked   = "blocked"
)

// What the attempt's result stood on. A usage the provider actually reported settles
// through the message; a result whose usage could not be read (a success without usage,
// a shape this stage cannot honestly map) is recorded but NOT delivered - the hold stays
// occupied until evidence (07.10/07.11/08.x) says settle or release. Unknown is never
// settled as zero: that would be a made-up bill of sale on the reservation.
const (
	SettlementOutcomeProviderUsageReport = "provider_usage_report"
	SettlementOutcomeUsageUnknown        = "usage_unknown"
)

// The evidence kind the settle message stands on: the provider's own usage report. The
// closed vocabulary lives on OpenBot's side (credit-settle.go); a new member is a
// contract change on both sides, never a free string.
const settleEvidenceProviderUsageReport = "provider_usage_report"

// settledBy is the identifier the settle message names as its settling party.
const settledByGateway = "bifrost-governance"

// The sources a duplicate arrival can come from (07.9): the attempt's result recorded a
// second time, or a delivery ending reported for a record that had already left the queue
// (an ack, failure or block that raced the record's settled ending). The closed vocabulary
// lives on the record's audit trail; a new member is an observable-event contract, never a
// free string.
const (
	SettlementDuplicateSourceResult         = "duplicate_result"
	SettlementDuplicateSourceDeliveryReport = "duplicate_delivery_report"
)

// errSettlementRecordMissing is what the store answers when asked about an attempt that
// has no record - a defect in the caller, never a silent no-op.
var errSettlementRecordMissing = errors.New("settlement record not found")

// The delivery ceiling: a pending record stops being retried after this many attempts and
// becomes blocked evidence. Attempts say "we keep failing"; the blocked state says so to
// recovery instead of hot-looping until somebody notices the pending count.
const maxSettlementDeliveryAttempts = 8

// The failure codes the delivery itself mints. The 422 refusal codes from OpenBot's
// closed vocabulary pass through as-is (they are already stable, displayable codes);
// these name the endings that happened before OpenBot could answer.
const (
	settlementFailureUnreachable  = "settle_unreachable"
	settlementFailureTimeout      = "settle_timeout"
	settlementFailureServerError  = "settle_server_error"
	settlementFailureRefused      = "settle_refused"
	settlementFailureNotWired     = "settle_door_not_wired"
	settlementFailureMarshalError = "settle_request_unwritable"
)

// SettlementUsage is the actual billed usage in the closed billing-dimension vocabulary
// the settle contract prices (the gateway's price snapshot dims). Every dimension is
// always present - zero where nothing was consumed - so a message can never understate
// the vocabulary by omission, and never carries a dimension OpenBot does not price.
type SettlementUsage struct {
	Input               int64 `json:"input"`
	Output              int64 `json:"output"`
	Reasoning           int64 `json:"reasoning"`
	Image               int64 `json:"image"`
	ImageOutput         int64 `json:"image_output"`
	AudioInput          int64 `json:"audio_input"`
	AudioOutput         int64 `json:"audio_output"`
	CacheRead           int64 `json:"cache_read"`
	CacheWrite          int64 `json:"cache_write"`
	Video               int64 `json:"video"`
	SurchargeWebSearch  int64 `json:"surcharge_web_search"`
	SurchargePerRequest int64 `json:"surcharge_per_request"`
}

// SettlementOutboxRecord is one attempt's settlement, from its persisting to its
// delivery. The ownership, reservation and locked-version members are what recovery
// needs to finish a settlement whose delivering process did not survive.
type SettlementOutboxRecord struct {
	// The attempt identity (bfat-...): the record's idempotency key and the settle
	// message's retry identity.
	AttemptID string `json:"attemptId"`
	// The held reservation the usage settles against.
	ReservationID string `json:"reservationId"`

	// What the attempt dispatched, as the funnel saw it.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// The attempt's coordinates, mirroring the dispatch commitment's: auditable from the
	// records alone, no other surface to correlate.
	RequestID     string `json:"requestId"`
	BillingNonce  string `json:"billingNonce"`
	FallbackIndex int    `json:"fallbackIndex"`

	// Ownership: the grant the call ran under and the payment account it bills to, as the
	// reserve door carried them at admission. Never a self-reported account: the settle
	// message names no account, these are recovery's record of whose it was.
	GrantID   string `json:"grantId"`
	PayerKind string `json:"payerKind"`
	PayerID   string `json:"payerId"`

	// The reservation basis: the hold's ceiling and the locked versions the reserve
	// answer echoed (06.5's basis, as admitted - never re-derived from current config).
	MaximumCredits  int64 `json:"maximumCredits"`
	SnapshotVersion int   `json:"snapshotVersion"`
	RateVersion     int   `json:"rateVersion"`

	// What the result stood on, and the usage it reported.
	Outcome SettlementOutcome `json:"outcome"`
	Usage   SettlementUsage   `json:"usage"`

	// The delivery state: pending until a 200 says delivered; blocked when a retry
	// cannot fix the ending.
	Status           string    `json:"status"`
	DeliveryAttempts int       `json:"deliveryAttempts"`
	LastErrorCode    string    `json:"lastErrorCode,omitempty"`
	RecordedAt       time.Time `json:"recordedAt"`

	// Duplicate-event evidence (07.9): how many arrivals found this record already
	// settled in the store or already out of the retry queue, and which kind came last.
	// Counted on the record the first arrival created - never a second record, never a
	// second ending - so a duplicate delivery or a duplicate ack is auditable from the
	// records alone, with its count and its source.
	DuplicateEvents     int    `json:"duplicateEvents,omitempty"`
	LastDuplicateSource string `json:"lastDuplicateSource,omitempty"`
}

// SettlementOutcome names what the attempt's result stood on.
type SettlementOutcome = string

// SettlementOutbox persists settlement records and drives the retry queue. Record is
// idempotent by AttemptID: the first call writes the record and wins; every later call of
// the same attempt returns the existing record - settlement content unchanged - loses, and
// is counted on the record as a duplicate arrival. A delivery ending reported for a record
// already out of the queue is likewise counted, and changes nothing. Pending is the
// retry queue and recovery's scan seam (08.x walks it).
type SettlementOutbox interface {
	Record(ctx context.Context, record SettlementOutboxRecord) (SettlementOutboxRecord, bool, error)
	Pending(ctx context.Context) ([]SettlementOutboxRecord, error)
	MarkDelivered(ctx context.Context, attemptID string) error
	RecordDeliveryFailure(ctx context.Context, attemptID string, code string) (int, error)
	MarkBlocked(ctx context.Context, attemptID string, code string) error
}

// localSettlementOutbox is the default in-process backend: a mutex and a map keyed by
// attempt identity. Records live as long as the process does; a deployment that needs
// settlements to survive restarts wires a persistent backend under the same interface.
type localSettlementOutbox struct {
	mu      sync.Mutex
	records map[string]SettlementOutboxRecord
}

func newLocalSettlementOutbox() *localSettlementOutbox {
	return &localSettlementOutbox{records: make(map[string]SettlementOutboxRecord)}
}

// Record writes the record on the first call of its attempt identity and elects that one
// caller the winner; later callers are handed the existing record - settlement content
// unchanged, the first record standing - told they lost, and counted on the record as a
// duplicate result. The store stamps the recorded-at moment and the pending status, so
// both are written exactly once, with the record.
func (s *localSettlementOutbox) Record(_ context.Context, record SettlementOutboxRecord) (SettlementOutboxRecord, bool, error) {
	if record.AttemptID == "" {
		return SettlementOutboxRecord{}, false, errors.New("settlement record has no attempt id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[record.AttemptID]; ok {
		// The duplicate changes nothing about the settlement: it is counted on the record
		// the first arrival created, so an anomalous redelivery is observable evidence.
		countDuplicateArrival(&existing, SettlementDuplicateSourceResult)
		s.records[record.AttemptID] = existing
		return existing, false, nil
	}
	record.Status = SettlementStatusPending
	record.RecordedAt = time.Now().UTC()
	s.records[record.AttemptID] = record
	return record, true, nil
}

// Pending returns every record still awaiting delivery - the retry queue, and the seam
// recovery (08.x) scans. The returned records are copies; a caller may read them without
// seeing later writes.
func (s *localSettlementOutbox) Pending(_ context.Context) ([]SettlementOutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]SettlementOutboxRecord, 0, len(s.records))
	for _, record := range s.records {
		if record.Status == SettlementStatusPending {
			pending = append(pending, record)
		}
	}
	return pending, nil
}

// snapshot returns every record whatever its state; the blocked-evidence assertions read
// through it.
func (s *localSettlementOutbox) snapshot(_ context.Context) ([]SettlementOutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]SettlementOutboxRecord, 0, len(s.records))
	for _, record := range s.records {
		all = append(all, record)
	}
	return all, nil
}

// countDuplicateArrival records a duplicate arrival on the record it duplicated: the
// settled ending stands, the arrival itself is evidence with its source named. The caller
// holds the store mutex and writes the record back.
func countDuplicateArrival(record *SettlementOutboxRecord, source string) {
	record.DuplicateEvents++
	record.LastDuplicateSource = source
}

func (s *localSettlementOutbox) MarkDelivered(_ context.Context, attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[attemptID]
	if !ok {
		return errSettlementRecordMissing
	}
	if record.Status != SettlementStatusPending {
		// A delivery success for a record already out of the queue is a duplicate arrival
		// (a late 200 that raced the settled ending): counted, never acted on - a blocked
		// record stays blocked, a delivered one stays delivered, and neither re-drives.
		countDuplicateArrival(&record, SettlementDuplicateSourceDeliveryReport)
		s.records[attemptID] = record
		return nil
	}
	// Evidence outranks a late delivery report from a worker that raced the block: a
	// record already blocked stays blocked.
	record.Status = SettlementStatusDelivered
	record.LastErrorCode = ""
	s.records[attemptID] = record
	return nil
}

func (s *localSettlementOutbox) RecordDeliveryFailure(_ context.Context, attemptID string, code string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[attemptID]
	if !ok {
		return 0, errSettlementRecordMissing
	}
	if record.Status != SettlementStatusPending {
		// A failure report for a record already out of the queue schedules no retry: it is
		// a late arrival from a worker that raced the settled ending - counted, not acted on.
		countDuplicateArrival(&record, SettlementDuplicateSourceDeliveryReport)
		s.records[attemptID] = record
		return record.DeliveryAttempts, nil
	}
	record.DeliveryAttempts++
	record.LastErrorCode = code
	s.records[attemptID] = record
	return record.DeliveryAttempts, nil
}

func (s *localSettlementOutbox) MarkBlocked(_ context.Context, attemptID string, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[attemptID]
	if !ok {
		return errSettlementRecordMissing
	}
	if record.Status != SettlementStatusPending {
		// A block for a record already out of the queue lost its race with the settled
		// ending: counted as an arrival, and the settled ending stands.
		countDuplicateArrival(&record, SettlementDuplicateSourceDeliveryReport)
		s.records[attemptID] = record
		return nil
	}
	record.Status = SettlementStatusBlocked
	record.LastErrorCode = code
	s.records[attemptID] = record
	return nil
}

// CreditSettleConfig is the deployment's settle door: where OpenBot's Settle surface
// answers, under the same trusted service identity 06.1 issued for the reserve door.
// Absent or miswired, nothing is refused for it: the records keep persisting (fail-
// durable) and their delivery waits for wiring - a settlement that cannot be sent is a
// pending record, never a dropped one.
type CreditSettleConfig struct {
	// The Settle surface's URL, e.g. http://openbot:3000/api/credit-protocol/settle.
	Endpoint string `json:"endpoint"`
	// The trusted billing service secret (06.1), presented as Bearer credentials.
	ServiceSecret string `json:"serviceSecret"`
	// How long the delivery waits for the answer. Zero or negative means the default (5s).
	TimeoutMs int `json:"timeoutMs"`
}

// wiringProblem names the first way the door's wiring fails the Settle contract's shape,
// or "" when a delivery could honestly be built.
func (cfg *CreditSettleConfig) wiringProblem() string {
	switch {
	case cfg == nil:
		return "no settle door"
	case strings.TrimSpace(cfg.Endpoint) == "":
		return "no endpoint"
	case strings.TrimSpace(cfg.ServiceSecret) == "":
		return "no service secret"
	}
	return ""
}

// The settle message's wire shape (06.9's request contract): the reservation named by id,
// the actual usage in the closed vocabulary, the evidence the usage stands on, and the
// attempt identity as the message's retry key. No price, no account - OpenBot derives
// both from the reservation's own locked, persisted basis.
type settleDeliverRequest struct {
	ReservationID  string          `json:"reservationId"`
	ActualUsage    SettlementUsage `json:"actualUsage"`
	Evidence       settleEvidence  `json:"evidence"`
	SettledBy      string          `json:"settledBy"`
	IdempotencyKey string          `json:"idempotencyKey"`
}

type settleEvidence struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference,omitempty"`
}

// settleRequestFor builds the message a record delivers. Every redelivery of the record
// builds the same bytes: the message is a function of the record, which is what makes the
// retry a replay rather than a new claim.
func settleRequestFor(record SettlementOutboxRecord) settleDeliverRequest {
	return settleDeliverRequest{
		ReservationID: record.ReservationID,
		ActualUsage:   record.Usage,
		Evidence: settleEvidence{
			Kind:      settleEvidenceProviderUsageReport,
			Reference: record.RequestID,
		},
		SettledBy:      settledByGateway,
		IdempotencyKey: record.AttemptID,
	}
}

// --- the usage extraction ----------------------------------------------------------------

// settlementUsageFrom reads the attempt's actual usage off its ending. The bool answers
// whether the provider's own report was readable at all: a false is a result whose usage
// is UNKNOWN - recorded for recovery, never delivered as zeros. The extraction is held to
// the attested output cap (07.7) exactly where the accounting holds it: what is settled
// can never exceed what the reservation's evidence proved enforceable.
func settlementUsageFrom(result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, outputCapTokens int64) (SettlementUsage, bool) {
	if result != nil {
		switch {
		case result.ChatResponse != nil:
			usage := result.ChatResponse.Usage
			return settlementUsageFromLLM(usage, outputCapTokens), usage != nil
		case result.TextCompletionResponse != nil:
			usage := result.TextCompletionResponse.Usage
			return settlementUsageFromLLM(usage, outputCapTokens), usage != nil
		case result.ResponsesResponse != nil:
			usage := result.ResponsesResponse.Usage
			return settlementUsageFromResponses(usage, outputCapTokens), usage != nil
		case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil:
			usage := result.ResponsesStreamResponse.Response.Usage
			return settlementUsageFromResponses(usage, outputCapTokens), usage != nil
		case result.EmbeddingResponse != nil:
			usage := result.EmbeddingResponse.Usage
			return settlementUsageFromLLM(usage, 0), usage != nil
		}
	}
	// A failed attempt whose provider still processed tokens: the cost is real (the
	// provider charges for it) and rides the error's billed usage.
	if bifrostErr != nil && bifrostErr.ExtraFields.BilledUsage != nil {
		return settlementUsageFromLLM(bifrostErr.ExtraFields.BilledUsage, outputCapTokens), true
	}
	return SettlementUsage{}, false
}

// settlementUsageFromLLM maps the LLM usage shape onto the closed vocabulary. The
// mapping is the pricing's own (datasheet cost): the prompt total INCLUDES its cached
// parts, so input is the prompt minus the clamped cache reads and writes, and reasoning
// tokens stay inside completion - Bifrost prices them there, and splitting them out would
// bill the same tokens twice against a basis that prices both dims.
func settlementUsageFromLLM(usage *schemas.BifrostLLMUsage, outputCapTokens int64) SettlementUsage {
	mapped := SettlementUsage{}
	if usage == nil {
		return mapped
	}
	prompt := int64(usage.PromptTokens)
	cachedRead, cachedWrite := int64(0), int64(0)
	if details := usage.PromptTokensDetails; details != nil {
		cachedRead = int64(details.CachedReadTokens)
		cachedWrite = int64(details.CachedWriteTokens)
	}
	// Clamped the way pricing clamps: a malformed provider payload cannot bill negative
	// input or double-count its cache.
	if cachedRead > prompt {
		cachedRead = prompt
	}
	if cachedWrite > prompt-cachedRead {
		cachedWrite = prompt - cachedRead
	}
	completion := int64(usage.CompletionTokens)
	if outputCapTokens > 0 && completion > outputCapTokens {
		completion = outputCapTokens
	}
	mapped.Input = prompt - cachedRead - cachedWrite
	mapped.CacheRead = cachedRead
	mapped.CacheWrite = cachedWrite
	mapped.Output = completion
	if usage.ToolUsage != nil && usage.ToolUsage.WebSearch != nil && usage.ToolUsage.WebSearch.NumRequests > 0 {
		mapped.SurchargeWebSearch = int64(usage.ToolUsage.WebSearch.NumRequests)
	}
	return mapped
}

// settlementUsageFromResponses maps the Responses API's own usage shape, with the same
// cache semantics: the input total includes its cached reads.
func settlementUsageFromResponses(usage *schemas.ResponsesResponseUsage, outputCapTokens int64) SettlementUsage {
	mapped := SettlementUsage{}
	if usage == nil {
		return mapped
	}
	input := int64(usage.InputTokens)
	cachedRead := int64(0)
	if details := usage.InputTokensDetails; details != nil {
		cachedRead = int64(details.CachedReadTokens)
	}
	if cachedRead > input {
		cachedRead = input
	}
	output := int64(usage.OutputTokens)
	if outputCapTokens > 0 && output > outputCapTokens {
		output = outputCapTokens
	}
	mapped.Input = input - cachedRead
	mapped.CacheRead = cachedRead
	mapped.Output = output
	if usage.ToolUsage != nil && usage.ToolUsage.WebSearch != nil && usage.ToolUsage.WebSearch.NumRequests > 0 {
		mapped.SurchargeWebSearch = int64(usage.ToolUsage.WebSearch.NumRequests)
	}
	return mapped
}

// --- the stage in the hook ----------------------------------------------------------------

// recordAttemptSettlement is the PostLLM settlement stage: it persists the attempt's
// settlement record SYNCHRONOUSLY - by the time the hook returns and the response ends,
// the record is in the outbox - and hands the delivery to a worker. A reservation that is
// not on the context (free work, non-strict paths) records nothing: it reserved nothing.
//
// A record that cannot be persisted is logged and left to recovery: the provider already
// ran and the hold is already taken, so failing the response would waste both; the
// dispatch commitment stays pending, and recovery settles from its evidence.
func (p *GovernancePlugin) recordAttemptSettlement(reservation *CreditReservation, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, provider schemas.ModelProvider, model string, requestType schemas.RequestType, requestID string, billingNonce string, fallbackIndex int, isFinalChunk bool, outputCapTokens int64) {
	if reservation == nil || p.settlementOutbox == nil {
		return
	}
	// The usage settles when it is final: a non-stream response is final at once, a
	// stream at its final chunk (07.10 owns the shapes of stream endings).
	if bifrost.IsStreamRequestType(requestType) && !isFinalChunk {
		return
	}
	usage, known := settlementUsageFrom(result, bifrostErr, outputCapTokens)
	outcome := SettlementOutcomeUsageUnknown
	if known {
		outcome = SettlementOutcomeProviderUsageReport
	}
	record := SettlementOutboxRecord{
		AttemptID:       reservation.AttemptID,
		ReservationID:   reservation.ReservationID,
		Provider:        string(provider),
		Model:           model,
		RequestID:       requestID,
		BillingNonce:    billingNonce,
		FallbackIndex:   fallbackIndex,
		GrantID:         reservation.GrantID,
		PayerKind:       reservation.PayerKind,
		PayerID:         reservation.PayerID,
		MaximumCredits:  reservation.MaximumCredits,
		SnapshotVersion: reservation.SnapshotVersion,
		RateVersion:     reservation.RateVersion,
		Outcome:         outcome,
		Usage:           usage,
	}
	record, won, err := p.settlementOutbox.Record(p.ctx, record)
	if err != nil {
		p.logger.Error("the settlement record for attempt %s could not be persisted (%s): recovery must settle reservation %s from its dispatch commitment",
			record.AttemptID, err, record.ReservationID)
		return
	}
	if !won {
		// A duplicate result for an attempt already recorded: the first record stands and
		// drives the one delivery it is owed (the store has counted the duplicate on the
		// record). Driving a second delivery here would send the settle message twice
		// outside the retry queue's accounting - the double-drive 07.9 closes.
		p.logger.Warn("a duplicate settlement result arrived for attempt %s; the first record stands (duplicate events: %d)",
			record.AttemptID, record.DuplicateEvents)
		return
	}
	if outcome != SettlementOutcomeProviderUsageReport {
		// An unknown result delivers no message: the hold stays until evidence says
		// otherwise (Q12), and 07.10/07.11/08.x own what happens next.
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		// A delivery panic can never take the process down with the record in flight.
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("recovered from panic in governance settlement delivery: %v", r)
			}
		}()
		p.deliverSettlement(record)
	}()
}

// settleDoor reads the settle door's wiring as configured right now. The delivery reads
// it late on purpose: a door fixed after a failed delivery is a door the retry uses.
func (p *GovernancePlugin) settleDoor() *CreditSettleConfig {
	p.cfgMutex.RLock()
	defer p.cfgMutex.RUnlock()
	if p.openBotCreditAdmission == nil {
		return nil
	}
	return p.openBotCreditAdmission.Settle
}

// The default wait for a settle answer before the delivery treats the door as failed.
const defaultSettleTimeout = 5 * time.Second

// deliverSettlement delivers one record: build the message, ask the door, and persist the
// ending. A 200 marks the record delivered; a 422 is a deterministic refusal (the message
// or the reservation state will not heal by retrying) and blocks the record as evidence;
// anything else - timeout, unreachable, server error - is a retryable failure that keeps
// the record pending with its failure recorded, until the attempt ceiling ends it.
// Every ending here is durable; none of them loses the message.
func (p *GovernancePlugin) deliverSettlement(record SettlementOutboxRecord) {
	if p.settlementOutbox == nil {
		return
	}
	door := p.settleDoor()
	if problem := door.wiringProblem(); problem != "" {
		// Fail-durable: the record stays pending with the wiring state named on it, and
		// the retry queue delivers it when the door is wired. Nothing is dropped.
		if _, err := p.settlementOutbox.RecordDeliveryFailure(p.ctx, record.AttemptID, settlementFailureNotWired); err != nil {
			p.logger.Error("the settlement for attempt %s could not record its undelivered state (%s)", record.AttemptID, err)
		}
		p.logger.Warn("the settlement for attempt %s stays pending: the settle door is not wired (%s)",
			record.AttemptID, problem)
		return
	}
	payload, err := json.Marshal(settleRequestFor(record))
	if err != nil {
		// A message that cannot be built is a defect, not a network condition: block the
		// record as evidence rather than spin.
		p.logger.Error("the settle message for attempt %s could not be built (%s)", record.AttemptID, err)
		if _, err := p.settlementOutbox.RecordDeliveryFailure(p.ctx, record.AttemptID, settlementFailureMarshalError); err == nil {
			_ = p.settlementOutbox.MarkBlocked(p.ctx, record.AttemptID, settlementFailureMarshalError)
		}
		return
	}
	status, body, transportErr := p.callSettleDoor(door, payload)
	switch {
	case transportErr != nil:
		code := settlementFailureUnreachable
		if isDeadlineError(transportErr) {
			code = settlementFailureTimeout
		}
		p.recordSettlementFailure(record, code, transportErr)
	case status >= 200 && status < 300:
		if err := p.settlementOutbox.MarkDelivered(p.ctx, record.AttemptID); err != nil {
			p.logger.Error("the settlement for attempt %s could not be marked delivered (%s)", record.AttemptID, err)
		}
	case status == http.StatusUnprocessableEntity:
		code := settleRefusalCode(body)
		p.logger.Warn("the settlement for attempt %s was refused (%s): the record is blocked as evidence, not retried",
			record.AttemptID, code)
		if err := p.settlementOutbox.MarkBlocked(p.ctx, record.AttemptID, code); err != nil {
			p.logger.Error("the settlement for attempt %s could not be blocked (%s)", record.AttemptID, err)
		}
	default:
		p.recordSettlementFailure(record, settlementFailureServerError, nil)
	}
}

// recordSettlementFailure records a retryable delivery failure and holds the record in
// the retry queue; past the attempt ceiling the record becomes blocked evidence.
func (p *GovernancePlugin) recordSettlementFailure(record SettlementOutboxRecord, code string, cause error) {
	if cause != nil {
		p.logger.Warn("the settlement delivery for attempt %s failed (%s: %s); the record stays in the retry queue",
			record.AttemptID, code, cause)
	} else {
		p.logger.Warn("the settlement delivery for attempt %s failed (%s); the record stays in the retry queue",
			record.AttemptID, code)
	}
	attempts, err := p.settlementOutbox.RecordDeliveryFailure(p.ctx, record.AttemptID, code)
	if err != nil {
		p.logger.Error("the settlement for attempt %s could not record its delivery failure (%s)", record.AttemptID, err)
		return
	}
	if attempts >= maxSettlementDeliveryAttempts {
		p.logger.Warn("the settlement for attempt %s exhausted its delivery attempts: the record is blocked as evidence",
			record.AttemptID)
		if err := p.settlementOutbox.MarkBlocked(p.ctx, record.AttemptID, code); err != nil {
			p.logger.Error("the settlement for attempt %s could not be blocked (%s)", record.AttemptID, err)
		}
	}
}

// settleRefusalCode reads the closed reason code off a 422 answer. An unreadable body is
// still evidence - it blocks under a code of its own rather than retrying a refusal whose
// words the gateway could not read.
func settleRefusalCode(body []byte) string {
	if len(body) == 0 {
		return settlementFailureRefused
	}
	var refused struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &refused); err != nil || refused.Code == "" {
		return settlementFailureRefused
	}
	return refused.Code
}

// isDeadlineError reports whether an ending was the door's wait running out, as against
// the door being unreachable at all.
func isDeadlineError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// callSettleDoor asks the settle door, the way the reserve door is asked: one client with
// a hard timeout, the request bound to the plugin's own context so a cancelled delivery
// stops waiting with it. Any transport-level ending - timeout, connection refused, reset -
// comes back as an error for the caller to classify.
func (p *GovernancePlugin) callSettleDoor(door *CreditSettleConfig, payload []byte) (int, []byte, error) {
	timeout := defaultSettleTimeout
	if door.TimeoutMs > 0 {
		timeout = time.Duration(door.TimeoutMs) * time.Millisecond
	}
	client := http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, door.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+door.ServiceSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// deliverPendingSettlements drives one pass of the retry queue: every pending record
// whose outcome is deliverable is delivered again, the same message under the same
// idempotency key. This is the seam the recovery scanner (08.x) schedules; the pass
// itself is the queue's whole mechanism.
func (p *GovernancePlugin) deliverPendingSettlements(ctx context.Context) error {
	if p.settlementOutbox == nil {
		return nil
	}
	pending, err := p.settlementOutbox.Pending(ctx)
	if err != nil {
		return err
	}
	for _, record := range pending {
		if record.Status != SettlementStatusPending {
			continue
		}
		if record.Outcome != SettlementOutcomeProviderUsageReport {
			// An unknown result is recovery's question, not the queue's.
			continue
		}
		p.deliverSettlement(record)
	}
	return nil
}
