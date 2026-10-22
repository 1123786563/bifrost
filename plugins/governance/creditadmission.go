// The OpenBot credit-admission extension is the mount point the OpenBot integration is built on:
// the switch that says this gateway may dispatch paid work to providers, the identity OpenBot's
// capability negotiation looks for, and the hard rule that a gateway which cannot prove the
// extension is enabled does not send provably-paid work to a provider.
//
// The rule is fail-closed on purpose. An OpenBot deployment serves models it prices; if the
// extension's configuration is missing, was stripped, or carries the switch off, serving those
// models would spend money no one reserved for (see ADR 0003). Rather than falling back to
// direct provider dispatch, the gate refuses such requests with its own explainable decision,
// and nothing downstream runs: no budget is consulted, no provider is called, and the refusal
// mark keeps the request from being billed for work it never started.
//
// What the gate refuses is what is KNOWN paid: the catalog carries a pricing entry for the model
// with at least one strictly positive rate. Explicitly free models - an entry whose every rate
// is absent or zero - cost nothing to serve and stay servable with the switch off. A model the
// catalog says nothing about is a different gate's question: unknown pricing is refused by the
// price-lock admission that runs once the extension is enabled (and by OpenBot's price snapshot
// before that), not by this one, which answers only "may paid work be dispatched at all".
//
// The gate sits in Evaluate, the funnel every evaluating caller passes through: the LLM hook
// (which runs again on every fallback attempt and carries the streaming pipeline), the realtime
// admission question asked before any turn exists, and the MCP and batch passes that evaluate
// per model. A request the gate refuses is refused on every attempt; there is no second entry
// that dispatches a provider call around it.
package governance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

const (
	// CreditAdmissionExtensionName is the name the extension declares itself under in the
	// gateway's plugin manifest. OpenBot's capability negotiation (01.6) looks the extension up
	// by this exact name; changing it is a breaking contract change on both sides.
	CreditAdmissionExtensionName = "openbot-credit-admission"

	// CreditAdmissionExtensionVersion is the version the extension declares, in strict X.Y.Z.
	// The negotiation trusts exactly one major line (>=1.0.0 <2.0.0): a major bump signals a
	// semantic change the consumer must adopt before trusting the extension again.
	CreditAdmissionExtensionVersion = "1.0.0"
)

// CreditAdmissionConfig is the switch block of the credit-admission extension, carried in the
// governance plugin's configuration under `openbot_credit_admission`.
//
// Enabled turns the extension on: paid models may proceed to the admission pipeline that
// reserves before dispatch. With the block absent or the switch off the gate refuses known-paid
// work - there is no configuration in which a gateway serves known-paid models without the
// extension enabled.
type CreditAdmissionConfig struct {
	Enabled *bool `json:"enabled"`
	// The deployment's reserve door (07.2): where PreLLM authentication calls OpenBot's
	// Reserve under the trusted service identity. Nil means no door is wired. An enabled
	// extension without a door cannot prove a hold, so dispatchable work is refused rather
	// than passed unreserved; explicitly free work still costs nothing and needs no hold.
	Reserve *CreditReserveConfig `json:"reserve"`
	// The per-model output-cap enforcement evidence (07.7): the cells whose provider the
	// deployment has actually verified to enforce the output cap, with the verification
	// environment, result and reproducible source. The gate is fail-closed: a cell with no
	// verified record here is refused before the reserve door is asked, whatever the request
	// itself carries - absence of evidence is never enablement (Q11). Records that concluded
	// unverified or not_enforced stay with their reason and keep their cell unavailable.
	OutputCapEvidence []OutputCapEnforcementRecord `json:"outputCapEvidence"`
	// The per-path audio capability evidence (14.4): the audio entrances whose billing the
	// deployment has actually verified, carried the way the output-cap records are. The gate
	// is fail-closed the same way: a speech or transcription entrance with no VERIFIED
	// record here is refused before the pricing classification and the reserve door, with
	// the specific missing evidence items named - an unverified audio path is not callable
	// at all, whatever the model's price says.
	AudioCapabilityEvidence []AudioCapabilityRecord `json:"audioCapabilityEvidence"`
	// The deployment's settle door (07.8): where the PostLLM settlement stage delivers the
	// attempt's actual usage, under the same trusted service identity as the reserve door.
	// Nil or miswired, dispatch is unaffected - settlements persist undelivered and wait
	// for the wiring; a settlement that cannot be sent is never a dropped one.
	Settle *CreditSettleConfig `json:"settle"`
}

// The bound-evidence kinds the Reserve contract accepts (ADR 0003: only a call whose cost
// ceiling can be ENFORCED may be admitted; an estimate is a rejection aid, never evidence).
// Since 07.6 the evidence is constructed per request in boundevidence.go and only ever
// carries these two mechanisms.
var creditReserveBoundEvidenceKinds = map[string]bool{
	"provider_max_tokens":            true,
	"self_enforced_input_truncation": true,
}

// CreditReserveBoundEvidence is one mechanism-level ceiling the reserve carries: how one cost
// side of the call is provably capped. The kinds are OpenBot's closed vocabulary, built here
// per request (07.6) - never accepted from configuration, so an estimate-shaped or unknown
// entry cannot enter by wiring; a non-positive ceiling is a derivation bug, refused before
// anything is sent.
type CreditReserveBoundEvidence struct {
	Kind          string `json:"kind"`
	MaximumTokens int64  `json:"maximumTokens"`
}

// CreditReserveConfig is the deployment's reserve door: where the credit protocol's Reserve
// surface answers, the trusted service identity 06.1 issued for calling it, and the request's
// stable basis - the access grant, the payment account, the locked price and rate versions,
// the input ceiling the per-request evidence enforces and the hold ceiling.
//
// The grant and the versions ride configuration until the tickets that resolve them per
// request land (05.x's per-authorization key issuance, the synced price basis): the seam
// reads them from here, so those tickets replace a config read with a request read
// without the funnel changing shape. The bound evidence already has (07.6): it is
// derived per request in boundevidence.go, and the deployment contributes only the
// input ceiling below. What the seam itself owns - and what the tests hold it to - is
// the wire: authenticate, ask, and answer only to a held reservation.
type CreditReserveConfig struct {
	// The Reserve surface's URL, e.g. http://openbot:3000/api/credit-protocol/reserve.
	Endpoint string `json:"endpoint"`
	// The trusted billing service secret (06.1), presented as Bearer credentials.
	ServiceSecret string `json:"serviceSecret"`
	// The model access grant this call runs under; re-proved live server-side, never trusted.
	GrantID string `json:"grantId"`
	// The payment account the hold bills to, as a claim the server verifies against the
	// account the grant itself resolves to (06.4). "personal" is today's kind.
	PayerKind string `json:"payerKind"`
	PayerID   string `json:"payerId"`
	// The price basis and conversion-rate versions this request locks (06.2's contract;
	// persisting them for settlement is 06.5's version locking).
	SnapshotVersion int `json:"snapshotVersion"`
	RateVersion     int `json:"rateVersion"`
	// The deployment's conservative input ceiling in tokens (ADR 0001), enforced per request
	// by self-executed truncation. Evidence itself is derived per request; this is the one
	// bound parameter the mechanism needs.
	InputCeilingTokens int64 `json:"inputCeilingTokens"`
	// The hold ceiling in scaled credit units (×100), a positive integer.
	MaximumCredits int64 `json:"maximumCredits"`
	// How long the seam waits for the answer before treating the reserve as refused.
	// Zero or negative means the default (5s).
	TimeoutMs int `json:"timeoutMs"`
}

// wiringProblem names the first way the door's wiring fails the Reserve contract's shape, or
// "" when a request could honestly be built. It runs before anything is sent: a door that
// cannot produce a contract-shaped request is a deployment misassembly, refused on posture.
func (cfg *CreditReserveConfig) wiringProblem() string {
	switch {
	case strings.TrimSpace(cfg.Endpoint) == "":
		return "no endpoint"
	case strings.TrimSpace(cfg.ServiceSecret) == "":
		return "no service secret"
	case strings.TrimSpace(cfg.GrantID) == "":
		return "no grant id"
	case strings.TrimSpace(cfg.PayerKind) == "" || strings.TrimSpace(cfg.PayerID) == "":
		return "no payment account"
	case cfg.SnapshotVersion <= 0:
		return "no price basis version"
	case cfg.RateVersion < 0:
		return "invalid conversion rate version"
	case cfg.InputCeilingTokens <= 0:
		return "no input ceiling"
	case cfg.MaximumCredits <= 0:
		return "no positive hold ceiling"
	}
	return ""
}

// The wire shapes the credit protocol's Reserve carries (06.2's request contract): the payer
// claim is exactly {kind,id}, the model cell the catalog identity triple, one bound per entry.
type creditReservePayer struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type creditReserveModel struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Capability string `json:"capability"`
}

type creditReserveEvidence struct {
	Kind          string `json:"kind"`
	MaximumTokens int64  `json:"maximumTokens"`
}

type creditReserveRequest struct {
	GrantID         string                  `json:"grantId"`
	Payer           creditReservePayer      `json:"payer"`
	Model           creditReserveModel      `json:"model"`
	SnapshotVersion int                     `json:"snapshotVersion"`
	RateVersion     int                     `json:"rateVersion"`
	BoundEvidence   []creditReserveEvidence `json:"boundEvidence"`
	MaximumCredits  int64                   `json:"maximumCredits"`
	IdempotencyKey  string                  `json:"idempotencyKey"`
}

// requestFor builds the Reserve request this attempt sends: the door's configured basis, the
// model cell the request names, the bound evidence derived from this request (07.6), and the
// gateway attempt identity as the idempotency key a replay answers with.
func (cfg *CreditReserveConfig) requestFor(evaluationRequest *EvaluationRequest, attemptID string, evidence []CreditReserveBoundEvidence) creditReserveRequest {
	carried := make([]creditReserveEvidence, 0, len(evidence))
	for _, entry := range evidence {
		carried = append(carried, creditReserveEvidence{Kind: entry.Kind, MaximumTokens: entry.MaximumTokens})
	}
	return creditReserveRequest{
		GrantID:         cfg.GrantID,
		Payer:           creditReservePayer{Kind: cfg.PayerKind, ID: cfg.PayerID},
		Model:           creditReserveModel{Provider: string(evaluationRequest.Provider), Model: evaluationRequest.Model, Capability: creditCapabilityForRequestType(evaluationRequest.RequestType)},
		SnapshotVersion: cfg.SnapshotVersion,
		RateVersion:     cfg.RateVersion,
		BoundEvidence:   carried,
		MaximumCredits:  cfg.MaximumCredits,
		IdempotencyKey:  attemptID,
	}
}

// creditCapabilityForRequestType maps the request's type onto the credit protocol's closed
// capability vocabulary. A type the vocabulary has no cell for maps to "unknown": OpenBot's
// open-scope check refuses what was never opened, so an unmapped dispatch is refused by the
// billing service rather than served on the gateway's guess.
func creditCapabilityForRequestType(requestType schemas.RequestType) string {
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest,
		schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest,
		schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		return "chat"
	case schemas.EmbeddingRequest:
		return "embedding"
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest,
		schemas.ImageEditRequest, schemas.ImageEditStreamRequest,
		schemas.ImageVariationRequest:
		return "image"
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		return "speech"
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		return "transcription"
	case schemas.RealtimeRequest:
		return "realtime"
	default:
		return "unknown"
	}
}

// CreditReservation is what a held reserve records on the request context: the reservation
// the billing service holds, the gateway attempt identity it was asked under (the idempotency
// key a replay answers with), the ceiling it holds, and the locked versions the answer echoed.
// The dispatch path (07.4) and the PostLLM settlement stage read it from here.
type CreditReservation struct {
	ReservationID   string
	AttemptID       string
	MaximumCredits  int64
	SnapshotVersion int
	RateVersion     int
	// The ownership the reserve door was asked under (07.8): the grant and payment account
	// the hold bills to, recorded at admission so the settlement stage and recovery read
	// the grant-time facts rather than whatever the configuration has become since.
	GrantID   string
	PayerKind string
	PayerID   string
	// OutputCapTokens is the output cap the attempt's own evidence attested (07.6's
	// provider_max_tokens entry, 07.7's verified cell): the ceiling the post hook holds
	// booked output usage to, so a provider exceeding the cap can book no unreserved cost.
	OutputCapTokens int64
}

// CreditReservationContextKey carries the request's held reservation once the reserve door
// answered 201. Absent means nothing was held - which for dispatchable work is exactly the
// state a request must not dispatch in.
const CreditReservationContextKey schemas.BifrostContextKey = "bf-credit-reservation"

// CreditAdmissionDescriptor is the extension's declaration of itself: the identity a consumer
// negotiates against, and the switch state it would find if it looked. The name and version are
// compile-time constants of this build, so a manifest that carries them cannot disagree with
// the code that enforces the gate.
type CreditAdmissionDescriptor struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

// CreditAdmissionExtension returns the extension's declaration: the name and version this build
// enforces the gate under, and whether the extension is enabled right now.
func (p *GovernancePlugin) CreditAdmissionExtension() CreditAdmissionDescriptor {
	p.cfgMutex.RLock()
	defer p.cfgMutex.RUnlock()
	return CreditAdmissionDescriptor{
		Name:    CreditAdmissionExtensionName,
		Version: CreditAdmissionExtensionVersion,
		Enabled: p.creditAdmissionEnabledLocked(),
	}
}

// creditAdmissionEnabledLocked reads the switch. Caller holds cfgMutex.
func (p *GovernancePlugin) creditAdmissionEnabledLocked() bool {
	return p.openBotCreditAdmission != nil &&
		p.openBotCreditAdmission.Enabled != nil &&
		*p.openBotCreditAdmission.Enabled
}

// pricingKnowledge is what the catalog knows about a model's price: nothing, that it is
// explicitly free, or that serving it costs money.
type pricingKnowledge int

const (
	pricingUnknown pricingKnowledge = iota
	pricingExplicitlyFree
	pricingPaid
)

// classifyPricing reads the catalog's pricing entry for the model. A model with no entry, a
// request that names no model, and a deployment without a catalog are all unknown here: this
// gate does not guess. So is a rate below zero: a negative rate is the catalog's "unpriced"
// marker (review-264 L1), not a refund and never free, so an entry carrying one is as unknown
// as one carrying nothing.
func (p *GovernancePlugin) classifyPricing(provider schemas.ModelProvider, model string) pricingKnowledge {
	if p.modelCatalog == nil || model == "" {
		return pricingUnknown
	}
	entry := p.modelCatalog.GetPricingEntryForModel(model, provider)
	if entry == nil {
		return pricingUnknown
	}
	if optionsHaveNegativeRate(entry.Options) {
		return pricingUnknown
	}
	if optionsHavePositiveRate(entry.Options) {
		return pricingPaid
	}
	return pricingExplicitlyFree
}

// optionsHaveNegativeRate reports whether any rate the pricing entry carries is set below
// zero. The scan runs over every rate field of the entry, so a rate added to the catalog later
// is covered without this gate changing.
func optionsHaveNegativeRate(options datasheet.Options) bool {
	value := reflect.ValueOf(options)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Ptr || field.Type().Elem().Kind() != reflect.Float64 {
			continue
		}
		if field.IsNil() {
			continue
		}
		if field.Elem().Float() < 0 {
			return true
		}
	}
	return false
}

// optionsHavePositiveRate reports whether any rate the pricing entry carries is set above zero.
// Negative rates are the catalog's "unpriced" marker, not a refund, so the comparison is strict:
// only a rate that charges anything makes the model paid. The scan runs over every rate field of
// the entry, so a rate added to the catalog later is covered without this gate changing.
func optionsHavePositiveRate(options datasheet.Options) bool {
	value := reflect.ValueOf(options)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Ptr || field.Type().Elem().Kind() != reflect.Float64 {
			continue
		}
		if field.IsNil() {
			continue
		}
		if field.Elem().Float() > 0 {
			return true
		}
	}
	return false
}

// evaluateCreditAdmission is the gate. Nil means the request may proceed; a result means the
// deployment may not dispatch this work: either the admission extension is not enabled and the
// request names a model whose service provably costs money, or the extension is enabled and
// this dispatchable work's reserve did not hold (refused, unanswered, unreachable, or an
// answer that cannot be read as a hold).
//
// The check runs after identity: a caller whose credential is dead is told that first, the way
// every other step of Evaluate orders its answers. It runs before any grant or limit machinery:
// the caller's access does not change the deployment's posture, and a refusal here must not
// leak which budgets exist behind a gate that was never open.
func (p *GovernancePlugin) evaluateCreditAdmission(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest) *EvaluationResult {
	p.cfgMutex.RLock()
	enabled := p.creditAdmissionEnabledLocked()
	var reserve *CreditReserveConfig
	if enabled && p.openBotCreditAdmission != nil {
		reserve = p.openBotCreditAdmission.Reserve
	}
	p.cfgMutex.RUnlock()
	if enabled {
		// The audio path capability gate (14.4): an unverified audio entrance is not
		// callable at all, so it refuses BEFORE the free/unknown pricing classification —
		// a free model does not make an unverifiable path verifiable — and before the
		// reserve door is asked. Non-audio request types return from the switch inside the
		// gate without touching the configured records, so the common case costs one
		// switch.
		// The speech tail-constraint gate (13.4): a synthesis call mode whose response format
		// the server cannot measure cannot have its tail bounded, so it refuses before the
		// per-entrance capability records, before the pricing classification and before the
		// reserve door - a verified entrance record does not make an unmeasurable format
		// measurable. Non-speech request types return from the switch inside the gate, so the
		// common case costs one switch.
		if problem := p.speechTailProblem(evaluationRequest); problem != "" {
			return &EvaluationResult{
				Decision:   DecisionCreditReserveNotHeld,
				ReasonCode: CreditRefusalCapNotProvable,
				Reason:     problem,
			}
		}
		if problem := p.audioCapabilityProblem(evaluationRequest.RequestType); problem != "" {
			return &EvaluationResult{
				Decision:   DecisionCreditReserveNotHeld,
				ReasonCode: CreditRefusalCapNotProvable,
				Reason:     problem,
			}
		}
		// The extension is live, and the free/unknown distinction now carries dispatch
		// semantics: explicitly free work costs nothing, reserves nothing, and proceeds;
		// everything else - a known-paid model, or pricing this catalog cannot name - is
		// dispatched only over a held reservation (07.2). Unknown pricing goes through the
		// reserve door too, where OpenBot's price snapshot is the authority that refuses it:
		// the gateway does not guess, either way.
		if p.classifyPricing(evaluationRequest.Provider, evaluationRequest.Model) == pricingExplicitlyFree {
			return nil
		}
		return p.reserveBeforeDispatch(ctx, evaluationRequest, reserve)
	}
	if p.classifyPricing(evaluationRequest.Provider, evaluationRequest.Model) != pricingPaid {
		// Not known paid: explicitly free work costs nothing, and unknown pricing is the
		// price-lock gate's question, not this one.
		return nil
	}
	return &EvaluationResult{
		Decision:   DecisionCreditAdmissionNotEnabled,
		ReasonCode: CreditRefusalAdmissionUnreachable,
		Reason: fmt.Sprintf("credit admission is not enabled: the %s extension is not mounted and enabled, so paid model %q is refused and no provider is called",
			CreditAdmissionExtensionName, evaluationRequest.Model),
	}
}

// The default wait for a reserve answer before the seam treats the door as unanswered.
const defaultReserveTimeout = 5 * time.Second

// reserveBeforeDispatch is the PreLLM seam: ask the deployment's reserve door to atomically
// hold the request's ceiling before any provider is touched, and let the request past only on
// a held reservation. Every other ending - refused, timed out, unreachable, an answer that
// cannot be read as a hold - is the same refusal: the request is marked refused and dispatched
// to no provider. There is no optimistic branch: a deployment whose billing service cannot
// answer does not dispatch paid work (AC05).
func (p *GovernancePlugin) reserveBeforeDispatch(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, reserve *CreditReserveConfig) *EvaluationResult {
	if reserve == nil {
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("credit admission is enabled but no reserve door is wired: model %q is refused and no provider is called",
				evaluationRequest.Model),
		}
	}
	if problem := reserve.wiringProblem(); problem != "" {
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the credit reserve wiring is incomplete (%s): model %q is refused and no provider is called",
				problem, evaluationRequest.Model),
		}
	}

	// The model cell's output-cap enforcement evidence (07.7): before this request may ask
	// the door to hold anything, the deployment must hold a verified record that THIS model's
	// provider actually enforces the output cap on THIS call method. The per-request half
	// (below) proves the call carries a cap; this half proves the provider honors it. A cell
	// without that proof is refused here, and the door is never asked - on this attempt or
	// any fallback after it, since the funnel runs both through here.
	if problem := p.outputCapEnforcementProblem(evaluationRequest.Provider, evaluationRequest.Model, evaluationRequest.RequestType); problem != "" {
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalCapNotProvable,
			Reason:     fmt.Sprintf("%s: model %q is refused and no provider is called", problem, evaluationRequest.Model),
		}
	}

	// The bound evidence is derived from THIS request before anything is asked of the door
	// (07.6): the complete serialized input, truncated in place to the deployment's input
	// ceiling, and the output cap the request itself carries. Insufficiency - an input that
	// cannot be counted in full or brought under the ceiling, a call that caps nothing -
	// refuses the call method here, and the door is never asked: an unprovable ceiling is
	// exactly what a reservation cannot be (ADR 0003), and the estimate that judged the fit
	// is a rejection aid only, never evidence, never a price (ADR 0001).
	evidence, evidenceProblem := deriveBoundEvidence(ctx, evaluationRequest, reserve)
	if evidenceProblem != "" {
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalCapNotProvable,
			Reason: fmt.Sprintf("the call's cost ceiling cannot be proven (%s): model %q is refused and no provider is called",
				evidenceProblem, evaluationRequest.Model),
		}
	}

	// The attempt identity: minted here, stable for this attempt of this request, and sent as
	// the reserve's idempotency key - so a replayed reserve for the same attempt answers the
	// original reservation, and a lost answer can be recovered by asking under the same
	// identity (06.7) rather than by paying twice.
	attemptID := creditAttemptID(ctx, evaluationRequest.Provider, evaluationRequest.Model)
	payload, err := json.Marshal(reserve.requestFor(evaluationRequest, attemptID, evidence))
	if err != nil {
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the credit reserve request could not be built (%s): model %q is refused and no provider is called",
				err, evaluationRequest.Model),
		}
	}

	status, body, transportErr := p.callReserveDoor(ctx, reserve, payload)
	if transportErr != nil {
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the credit reserve %s (%s): treated as refused, model %q is dispatched to no provider",
				reserveTransportFailureClass(transportErr), transportErr, evaluationRequest.Model),
		}
	}

	switch {
	case status == http.StatusCreated:
		var answer creditReserveAnswer
		if err := json.Unmarshal(body, &answer); err != nil || answer.ReservationID == "" || answer.SnapshotVersion <= 0 {
			// A 201 that cannot be read as a held reservation proves nothing. The request does
			// not dispatch on it; what the service did with the call is recovery's question
			// (06.7/06.8), asked under the same attempt identity, never a second reserve.
			return &EvaluationResult{
				Decision:   DecisionCreditReserveNotHeld,
				ReasonCode: CreditRefusalAdmissionUnreachable,
				Reason: fmt.Sprintf("the credit reserve answer could not be read as a held reservation: treated as refused, model %q is dispatched to no provider",
					evaluationRequest.Model),
			}
		}
		// The dispatch commitment (07.4): claimed before the request may leave for a provider,
		// so every dispatch has its record already persisted, and a replayed dispatch of one
		// attempt claims nothing new and sends nothing new. A refusal here - duplicate claim,
		// unwritable store - is the same short-circuit ending as every other credit refusal.
		if result := p.commitDispatch(ctx, evaluationRequest, attemptID, reserve, answer); result != nil {
			return result
		}
		// The attested output cap rides the reservation (07.7): the post hook holds what it
		// books to this ceiling, so the cost that settles never exceeds what the evidence
		// proved enforceable.
		outputCapTokens := int64(0)
		for _, entry := range evidence {
			if entry.Kind == "provider_max_tokens" {
				outputCapTokens = entry.MaximumTokens
				break
			}
		}
		ctx.SetValue(CreditReservationContextKey, &CreditReservation{
			ReservationID:   answer.ReservationID,
			AttemptID:       attemptID,
			MaximumCredits:  answer.MaximumCredits,
			SnapshotVersion: answer.SnapshotVersion,
			RateVersion:     answer.RateVersion,
			OutputCapTokens: outputCapTokens,
			GrantID:         reserve.GrantID,
			PayerKind:       reserve.PayerKind,
			PayerID:         reserve.PayerID,
		})
		return nil
	case status == http.StatusUnprocessableEntity:
		var refused creditReserveRefusal
		_ = json.Unmarshal(body, &refused)
		if refused.Code == "" {
			refused.Code = "unspecified"
		}
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: creditRefusalReasonForDoorCode(refused.Code),
			Reason: fmt.Sprintf("the credit reserve was refused (%s): model %q is dispatched to no provider",
				refused.Code, evaluationRequest.Model),
		}
	default:
		return &EvaluationResult{
			Decision:   DecisionCreditReserveNotHeld,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the credit reserve endpoint answered %d: treated as refused, model %q is dispatched to no provider",
				status, evaluationRequest.Model),
		}
	}
}

// creditReserveAnswer is the wire shape of a held reservation (06.2's response contract). The
// versions come back echoed: what the answer carries is the locked basis there is until 06.5's
// version locking persists its own.
type creditReserveAnswer struct {
	AttemptID       string `json:"attemptId"`
	ReservationID   string `json:"reservationId"`
	MaximumCredits  int64  `json:"maximumCredits"`
	SnapshotVersion int    `json:"snapshotVersion"`
	RateVersion     int    `json:"rateVersion"`
}

// creditReserveRefusal is the wire shape of a refusal: one closed reason code, displayable.
type creditReserveRefusal struct {
	Code string `json:"code"`
}

// callReserveDoor asks the deployment's reserve door, following the transports' outbound
// client pattern: one client with a hard timeout, the request bound to the request's own
// context so a cancelled dispatch stops waiting with it. Any transport-level ending - timeout,
// connection refused, reset - comes back as an error for the caller to treat as a refusal.
func (p *GovernancePlugin) callReserveDoor(ctx *schemas.BifrostContext, reserve *CreditReserveConfig, payload []byte) (int, []byte, error) {
	timeout := defaultReserveTimeout
	if reserve.TimeoutMs > 0 {
		timeout = time.Duration(reserve.TimeoutMs) * time.Millisecond
	}
	client := http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reserve.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+reserve.ServiceSecret)
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

// reserveTransportFailureClass names a transport-level ending the way the refusal message
// reports it: the reserve timed out, or the door could not be reached at all. Both are the
// same decision; the distinction is for whoever reads the trail.
func reserveTransportFailureClass(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "unreachable"
}

// creditAttemptID derives the attempt identity of this attempt of this request: a SHA-256
// over the attempt's own coordinates - the request id, the billing nonce, the fallback index,
// and the provider and model the attempt dispatches - never over the clock. The same attempt
// derives the same identity, which is what makes a replayed reserve idempotent; a different
// attempt (the next fallback) derives a different one, which is what keeps a new admission a
// new reservation. The bfat- prefix makes the identity recognisable on any surface that logs
// it, and the whole string is identifier-shaped for OpenBot's idempotency-key validation.
func creditAttemptID(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) string {
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	nonce := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyBillingNonce)
	fallbackIndex, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	sum := sha256.Sum256([]byte(string(provider) + "\x1f" + model + "\x1f" + requestID + "\x1f" + nonce + "\x1f" + strconv.Itoa(fallbackIndex)))
	return "bfat-" + hex.EncodeToString(sum[:16])
}
