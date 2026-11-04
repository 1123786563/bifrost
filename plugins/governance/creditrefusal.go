// The refusal semantics of the credit system (07.3): what a refusal is, what it names, and
// what it must never carry.
//
// A refusal is a short circuit. It reaches the pipeline as an LLMPluginShortCircuit whose
// error carries AllowFallbacks=false - core returns a pre-hook short circuit without
// dispatching, and the flag says the same thing at every other layer that reads the error:
// there is no configuration in which a request the credit system refused is retried against
// another provider, because what was refused is the dispatch itself, not one provider's
// answer.
//
// A refusal names one of five reasons, in a closed vocabulary the gateway owns:
//
//	insufficient_balance  余额不足 - the payment account cannot cover the hold
//	authorization_invalid 授权失效 - the grant, or the payer it resolves to, cannot be used
//	price_unknown         价格未知 - the billing service cannot price the model strictly
//	cap_not_provable      上限不可证明 - the call carries no enforceable cost-ceiling evidence
//	admission_unreachable 准入不可达 - the admission machinery itself was not available
//
// The vocabulary is the gateway's, not the billing service's: OpenBot's reserve surface
// answers with its own closed refusal codes (the credit protocol's), and the seam maps each
// onto exactly one category here. A code the vocabulary has never carried maps to
// admission_unreachable rather than opening a sixth category - an unrecognized answer is a
// deployment-posture answer, and five categories are what a caller or operator can act on.
//
// A refusal commits to nothing. No reservation is stamped on the request context (there is
// nothing held to settle against), and the refused mark on the context keeps the post hooks'
// accounting from writing any record: a request that never dispatched owes nothing.
//
// A refusal leaks nothing. Its message carries the reason and the model; it never carries the
// trusted service secret, the Authorization credential, the billing service's answer body
// (whose fields are OpenBot account data that belongs to no bot client), or the deployment's
// own account identifiers.
package governance

// The five refusal reason codes. The tests in creditrefusal_test.go hold these exact strings
// to be the vocabulary, pairwise distinct and closed.
const (
	// CreditRefusalInsufficientBalance (余额不足): the payment account cannot cover the hold.
	CreditRefusalInsufficientBalance = "insufficient_balance"
	// CreditRefusalAuthorizationInvalid (授权失效): the grant, or the payer it resolves to,
	// cannot be used for this call.
	CreditRefusalAuthorizationInvalid = "authorization_invalid"
	// CreditRefusalPriceUnknown (价格未知): the billing service cannot price the model
	// strictly, so it cannot be billed and is not dispatched.
	CreditRefusalPriceUnknown = "price_unknown"
	// CreditRefusalCapNotProvable (上限不可证明): the call carries no enforceable
	// cost-ceiling evidence; estimates are not evidence (ADR 0001).
	CreditRefusalCapNotProvable = "cap_not_provable"
	// CreditRefusalAdmissionUnreachable (准入不可达): the admission machinery itself was not
	// available - the extension is not enabled, no reserve door is wired, the door is
	// miswired, unreachable, unanswered, answered with something that holds no reservation,
	// or answered with a refusal this vocabulary has never carried.
	CreditRefusalAdmissionUnreachable = "admission_unreachable"
)

// creditRefusalReasonForDoorCode maps the credit protocol's closed refusal vocabulary onto the
// gateway's five categories. The mapping is total: every code the door can answer with lands
// in exactly one category, and a code it has never answered with lands in
// admission_unreachable, the category that owns every ending whose meaning the gateway cannot
// vouch for.
func creditRefusalReasonForDoorCode(code string) string {
	switch code {
	case "insufficient_credits":
		// The account is real, the grant is good, the price is known: the hold simply
		// does not fit under the available balance.
		return CreditRefusalInsufficientBalance
	case "grant_invalid", "payer_unresolved", "payer_conflict", "model_not_open":
		// The authorization relationship behind the call - the grant, the payer it
		// resolves to, or the model's openness to new work under it - cannot be used.
		return CreditRefusalAuthorizationInvalid
	case "price_unknown", "price_basis_not_found", "catalog_never_synced", "catalog_stale":
		// OpenBot cannot vouch for a complete, current price for this model.
		return CreditRefusalPriceUnknown
	case "bound_evidence_not_enforceable":
		// The call carries no ceiling that can be enforced; a reservation without one
		// would be an estimate wearing a hold's clothes (ADR 0001).
		return CreditRefusalCapNotProvable
	default:
		// request_invalid and reservation_conflict, and anything unrecognized: the
		// gateway cannot read the ending as one of the four caller-actionable
		// categories, so it reports the admission machinery as unavailable.
		return CreditRefusalAdmissionUnreachable
	}
}
