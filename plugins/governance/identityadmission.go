// T06.12 (#584, original task P16): the online identity admission the OpenBot gateway asks
// before every new model dispatch — new request, retry, or the start of a stream — before
// the reserve door and before any provider is called, free models included.
//
// WHAT THIS GATE IS. The credit-admission extension's identity half. OpenBot's T06.11 door
// (POST /api/gateway/identity-admission) re-proves, live and server-side, that the personal
// key this gateway authenticated still belongs to an identity that may dispatch: it holds a
// short transaction on the principal→grant→key rows and compares User/Principal/Grant/Key
// statuses and identity generations plus the original accounting rule. Its answers are
// one-dispatch facts — never cached, never replayed — so this gateway asks it on EVERY new
// model request and EVERY retry of one, at the start of every stream attempt, exactly as
// the reserve seam is asked.
//
// WHAT THE GATEWAY SENDS. The frozen closed request {requestId, keyId, valueHash,
// operation:"model.dispatch", modelRef} over the credit_service Bearer credential T06.11
// reuses (06.1's service authentication): the gateway's own virtual-key row id of the key it
// authenticated, the sha256 hex of the value it authenticated (the same digest shape
// OpenBot's key table stores), the fixed operation, and the model cell being dispatched.
// The gateway never sends a user, principal, generation, grant or payer — key ownership is
// resolved ONLY on OpenBot's side, which is the whole point of the door.
//
// FAIL-CLOSED, EVERY ANSWER BUT ONE. Only a 200 that reads as {allowed:true} admits the
// attempt. A 403 KEY_IDENTITY_INVALID (and a 401 service-auth failure) refuses the dispatch
// as an identity refusal — no reserve is asked, no provider is called, no fallback is tried.
// A 503, a timeout, an unreachable door or an unreadable answer refuses the dispatch as the
// admission machinery being unavailable: an identity that cannot be proved is not admitted,
// and free work is no exception — free models answer who is calling too, which is why the
// gate sits BEFORE the free/unknown pricing split rather than riding the charging branch.
//
// COST ON THE REQUEST FLOW. The common case — the extension off, or no identity door wired
// — adds one nil check to Evaluate's existing admission step. With the door armed, the cost
// is one HTTP ask per dispatch attempt over the same callReserveDoor transport the reserve
// uses, and no per-request allocation beyond the frozen request body. Nothing is retained
// beyond the attempt: no cache, no session state, no context key.
package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// IdentityAdmissionConfig is the deployment's identity door: where T06.11's online identity
// admission surface answers, and the trusted service identity 06.1 issued for calling it.
// Nil inside an enabled CreditAdmissionConfig means no door is wired — an enabled extension
// without an identity door refuses dispatchable work rather than dispatching on an identity
// it cannot prove; explicitly free work is refused the same way, because free models answer
// who is calling too.
type IdentityAdmissionConfig struct {
	// The identity admission surface's URL, e.g.
	// http://openbot:3000/api/gateway/identity-admission.
	Endpoint string `json:"endpoint"`
	// The trusted credit_service secret (06.1), presented as Bearer credentials — the same
	// credential family the reserve door uses; user sessions and Bot credentials both 401.
	ServiceSecret string `json:"serviceSecret"`
	// How long the gate waits for the answer before treating the door as unavailable.
	// Zero or negative means the default (5s).
	TimeoutMs int `json:"timeoutMs"`
}

// identityWiringProblem names the first way the door's wiring fails, or "" when the door
// could honestly be asked. It runs before anything is sent.
func (cfg *IdentityAdmissionConfig) identityWiringProblem() string {
	switch {
	case strings.TrimSpace(cfg.Endpoint) == "":
		return "no endpoint"
	case strings.TrimSpace(cfg.ServiceSecret) == "":
		return "no service secret"
	}
	return ""
}

// The default wait for an identity admission answer.
const defaultIdentityAdmissionTimeout = 5 * time.Second

// identityAdmissionOperation is the one operation this gate asks about; a new name is a
// contract change on T06.11's side.
const identityAdmissionOperation = "model.dispatch"

// identityAdmissionRequest is the frozen wire body (interfaces §5.3): exactly five keys,
// additionalProperties:false on the server, no client-authored identity anywhere in it.
type identityAdmissionRequest struct {
	RequestID string                    `json:"requestId"`
	KeyID     string                    `json:"keyId"`
	ValueHash string                    `json:"valueHash"`
	Operation string                    `json:"operation"`
	ModelRef  identityAdmissionModelRef `json:"modelRef"`
}

type identityAdmissionModelRef struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Capability string `json:"capability"`
}

// identityAdmissionAnswer is the 200 that admits: allowed must be true, and the echo of the
// request id ties the admission to this attempt. The admissionId is accepted only from a
// well-formed allowed answer and is not reused for anything — the fact is the answer itself.
type identityAdmissionAnswer struct {
	Allowed     bool   `json:"allowed"`
	RequestID   string `json:"requestId"`
	AdmissionID string `json:"admissionId"`
}

// identityAdmissionFor is the identity half of the admission step: nil means the attempt's
// identity is admitted (or the gate is not armed — see the caller), a result means the
// attempt is refused with a credit-refusal shape and nothing downstream runs.
//
// The key source is the request's OWN authenticated key: the presented value the transport
// settled on the grant, resolved to its row id and digest through the store the permits were
// resolved from. A request that presented no key has no identity to prove and is not this
// gate's to refuse — the funnel's identity step owns that answer.
func (p *GovernancePlugin) identityAdmissionFor(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest) *EvaluationResult {
	p.cfgMutex.RLock()
	var identity *IdentityAdmissionConfig
	if p.creditAdmissionEnabledLocked() && p.openBotCreditAdmission != nil {
		identity = p.openBotCreditAdmission.Identity
	}
	p.cfgMutex.RUnlock()
	if identity == nil {
		// The extension is enabled and dispatchable work is on the table (the caller asks
		// only there); no identity door wired is a deployment misassembly, refused on
		// posture like a missing reserve door.
		return &EvaluationResult{
			Decision:   DecisionCreditIdentityNotAdmitted,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: "the identity admission door is not wired: an enabled gateway that cannot prove the key's identity " +
				"dispatches nothing, on this attempt or any fallback after it",
		}
	}
	if problem := identity.identityWiringProblem(); problem != "" {
		return &EvaluationResult{
			Decision:   DecisionCreditIdentityNotAdmitted,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the identity admission wiring is incomplete (%s): the dispatch is refused and no provider is called",
				problem),
		}
	}

	// The key source. What the request authenticated is the presented value the transport
	// settled on its grant; its row and digest come from the store the permits were
	// resolved from — the same resolution the funnel already trusts, never the body.
	virtualKeyValue := PresentedVirtualKey(ctx)
	if virtualKeyValue == "" {
		// No key presented: no personal identity is on the table. This gate governs the
		// personal-key source only; a keyless request is the funnel's identity step's
		// answer (and a mandatory-key deployment already refused it there).
		return nil
	}
	virtualKey, ok := p.store.GetVirtualKey(ctx, virtualKeyValue)
	if !ok || virtualKey == nil {
		// The key did not resolve; the funnel's identity step refuses it before the
		// admission gates. Nothing to ask the door about.
		return nil
	}

	payload, err := json.Marshal(identityAdmissionRequest{
		RequestID: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID),
		KeyID:     virtualKey.ID,
		ValueHash: sha256Hex(virtualKeyValue),
		Operation: identityAdmissionOperation,
		ModelRef: identityAdmissionModelRef{
			Provider:   string(evaluationRequest.Provider),
			Model:      evaluationRequest.Model,
			Capability: creditCapabilityForRequestType(evaluationRequest.RequestType),
		},
	})
	if err != nil {
		return &EvaluationResult{
			Decision:   DecisionCreditIdentityNotAdmitted,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason:     "the identity admission request could not be built: treated as refused, no provider is called",
		}
	}

	status, body, transportErr := callIdentityDoor(ctx, identity, payload)
	if transportErr != nil {
		return &EvaluationResult{
			Decision:   DecisionCreditIdentityNotAdmitted,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the identity admission %s (%s): treated as refused, no provider is called",
				reserveTransportFailureClass(transportErr), transportErr),
		}
	}

	switch {
	case status == http.StatusOK:
		var answer identityAdmissionAnswer
		if err := json.Unmarshal(body, &answer); err != nil || !answer.Allowed {
			// An answer that cannot be read as an admission proves nothing, and an
			// explicit allowed:false is the door refusing the identity: both refuse the
			// dispatch; only the code differs.
			code := CreditRefusalIdentityInvalid
			if err != nil {
				code = CreditRefusalAdmissionUnreachable
			}
			return &EvaluationResult{
				Decision:   DecisionCreditIdentityNotAdmitted,
				ReasonCode: code,
				Reason: fmt.Sprintf("the identity admission answer could not be read as an admission: model %q is dispatched to no provider",
					evaluationRequest.Model),
			}
		}
		// One synchronous dispatch, one fresh fact: nothing is cached, nothing rides the
		// context — the next attempt asks the door again by construction.
		return nil
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		// 401: the gateway's own service credential failed — a wiring answer, but the
		// identity is not proved, so the dispatch stands refused. 403: the identity
		// behind the key is not admittable (disabled, revoked, wrong source). Neither
		// leaks anything about whose key it was.
		return &EvaluationResult{
			Decision:   DecisionCreditIdentityNotAdmitted,
			ReasonCode: CreditRefusalIdentityInvalid,
			Reason: fmt.Sprintf("the identity admission refused the key's identity (%d): model %q is dispatched to no provider",
				status, evaluationRequest.Model),
		}
	default:
		// 503 and everything else: the admission machinery is unavailable — fail closed.
		return &EvaluationResult{
			Decision:   DecisionCreditIdentityNotAdmitted,
			ReasonCode: CreditRefusalAdmissionUnreachable,
			Reason: fmt.Sprintf("the identity admission is unavailable (%d): model %q is dispatched to no provider",
				status, evaluationRequest.Model),
		}
	}
}

// callIdentityDoor performs the ask over the same transport the reserve door uses: one
// POST under the request's context, the service credential as Bearer, a bounded wait.
func callIdentityDoor(ctx *schemas.BifrostContext, identity *IdentityAdmissionConfig, payload []byte) (int, []byte, error) {
	timeout := defaultIdentityAdmissionTimeout
	if identity.TimeoutMs > 0 {
		timeout = time.Duration(identity.TimeoutMs) * time.Millisecond
	}
	client := http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, identity.Endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+identity.ServiceSecret)
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

// sha256Hex is the digest shape OpenBot's key table stores: sha256 of the key value, hex.
func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
