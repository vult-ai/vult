package server

import (
	"context"
	"net/url"
	"strconv"

	"github.com/vult-ai/vult/ledger"
)

// BankLimitSetting limits how many bank logins a Vult holds through its
// connection provider, across all its Books. A provider bills for each
// login, so whoever runs the server can decide how many it pays for.
// Statements and banks read with the owner's own token are never limited.
type BankLimitSetting struct {
	// Connections is the most logins a Vult may hold, 1 to 100.
	Connections int `json:"connections"`
	// Title and Note are what the owner reads at the limit, in the server's
	// own words. Without them Vult says how many banks this Vult connects.
	Title string `json:"title,omitempty"`
	Note  string `json:"note,omitempty"`
	// MoreLabel and MoreURL name where the owner gets more, when there is
	// such a place. They are set together.
	MoreLabel string `json:"more_label,omitempty"`
	MoreURL   string `json:"more_url,omitempty"`
}

func (l *BankLimitSetting) validate() error {
	if l == nil {
		return nil
	}
	if l.Connections < 1 || l.Connections > 100 || len(l.Title) > 80 || len(l.Note) > 160 || len(l.MoreLabel) > 40 || (l.MoreLabel == "") != (l.MoreURL == "") {
		return ErrConfiguration
	}
	if l.MoreURL != "" {
		u, err := url.Parse(l.MoreURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return ErrConfiguration
		}
	}
	return nil
}

// BankLimit is a Vult's limit as the owner sees it: how many logins it may
// hold and how many it holds now.
type BankLimit struct {
	Max   int            `json:"max"`
	Used  int            `json:"used"`
	Title string         `json:"title,omitempty"`
	Note  string         `json:"note,omitempty"`
	More  *BankLimitMore `json:"more,omitempty"`
	// Reuse says a Vult at its limit may still link a bank it already has
	// elsewhere, as when one person's login can feed several Books.
	Reuse bool `json:"reuse,omitempty"`
}

// BankLimitMore is where the owner gets more connections.
type BankLimitMore struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// BankLimiter is a BankOperator that limits how many banks a Vult connects
// by its own count, such as one across everything a person runs. It returns
// nil for a Vult with no limit.
type BankLimiter interface {
	BankLimit(ctx context.Context, o ledger.TenantAdmin, tenant, book string) (*BankLimit, error)
}

// reason is the sentence a refused connection gives the owner.
func (l *BankLimit) reason() string {
	if l.Note != "" {
		return l.Note
	}
	if l.Max == 1 {
		return "This Vult connects one bank."
	}
	return "This Vult connects " + strconv.Itoa(l.Max) + " banks."
}

// bankLimitFor reads the Vult's limit: the operator's own, or this server's
// setting counted across the Vult's Books. It is nil when there is none.
func (a *webAPI) bankLimitFor(ctx context.Context, o ledger.TenantAdmin, tenant, book string) (*BankLimit, error) {
	if limiter, ok := a.bankOperator.(BankLimiter); ok {
		return limiter.BankLimit(ctx, o, tenant, book)
	}
	set := a.bankLimit
	if set == nil {
		return nil, nil
	}
	books, err := o.BooksBounded(ctx, 200)
	if err != nil {
		return nil, err
	}
	out := &BankLimit{Max: set.Connections, Title: set.Title, Note: set.Note}
	if set.MoreURL != "" {
		out.More = &BankLimitMore{Label: set.MoreLabel, Href: set.MoreURL}
	}
	for _, b := range books {
		logins, err := o.BankConnections(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range logins {
			// Only logins made through the provider's screen count; a
			// disconnected one is over.
			if a.bankScreen != nil && c.Provider == a.bankScreen.Provider && c.Status != "disconnected" {
				out.Used++
			}
		}
	}
	return out, nil
}

// bankRoomFor refuses a new login when the Vult is at this server's own
// limit, before the provider's screen opens. An operator decides for itself
// when it starts or finishes a link: it may let a person at their limit link
// a bank they already have.
func (a *webAPI) bankRoomFor(ctx context.Context, o ledger.TenantAdmin, tenant, book string) error {
	if a.bankOperator != nil {
		return nil
	}
	limit, err := a.bankLimitFor(ctx, o, tenant, book)
	if err != nil || limit == nil || limit.Used < limit.Max {
		return err
	}
	return &ledger.RefusedError{Reason: limit.reason()}
}
