package ledger

import "errors"

// PlanLimitError is a refusal by a server's plan limits: the change would take
// the workspace past what its plan allows. It is not a permission problem, so
// the person keeps working and sees what the limit is and how much is used.
type PlanLimitError struct {
	// Kind is what ran out: books, accounts, entries or storage.
	Kind string `json:"kind"`
	Used int64  `json:"used"`
	Max  int64  `json:"max"`
	// Offer names a plan with more room, when the server offers one, and
	// OfferHref is the path on this server where the person can take it.
	Offer     string `json:"offer,omitempty"`
	OfferHref string `json:"offer_href,omitempty"`
	// Message says the limit in plain words, ready to show as written.
	Message string `json:"-"`
}

func (e *PlanLimitError) Error() string { return e.Message }

// PlanLimitResponse is how a plan limit is answered over HTTP.
func PlanLimitResponse(e *PlanLimitError) map[string]any {
	out := map[string]any{"error": "plan_limit_reached", "message": e.Message,
		"limit": map[string]any{"kind": e.Kind, "used": e.Used, "max": e.Max}}
	if e.Offer != "" {
		out["offer"] = e.Offer
	}
	if e.Offer != "" && len(e.OfferHref) > 1 && e.OfferHref[0] == '/' && e.OfferHref[1] != '/' && e.OfferHref[1] != '\\' {
		out["offer_href"] = e.OfferHref
	}
	return out
}

// AdmissionRefusal is how a refused admission is returned: a plan limit
// keeps its words; any other refusal becomes denied, revealing nothing more.
func AdmissionRefusal(e, denied error) error {
	var limit *PlanLimitError
	if errors.As(e, &limit) {
		return limit
	}
	return denied
}
