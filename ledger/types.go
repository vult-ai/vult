// Package ledger implements a scoped, exact, append-only general journal.
package ledger

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid request")
	ErrDenied       = errors.New("access denied")
	ErrConflict     = errors.New("request key already used with different input")
	ErrNotFound     = errors.New("record not found")
	ErrUncertain    = errors.New("outcome unresolved; look up the original request key")
	ErrLimit        = errors.New("result exceeds the supported record limit")
	idPattern       = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	codePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,31}$`)
	amountPattern   = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func ValidID(s string) bool { return idPattern.MatchString(s) }
func ValidDate(s string) bool {
	t, e := time.Parse("2006-01-02", s)
	return e == nil && t.Year() >= 1 && t.Year() <= 9999 && t.Format("2006-01-02") == s
}
func positiveAmount(s string) (int64, error) {
	if !amountPattern.MatchString(s) {
		return 0, ErrInvalid
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n <= 0 {
		return 0, ErrInvalid
	}
	return n, nil
}
func validKey(s string) bool { return len(s) > 0 && len(s) <= 128 }
func raw(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

type Scope struct{ TenantID, BookID, ActorID, ConnectionID, Access string }

// ValidAccess reports whether access is a connection's access level: read,
// propose (read and prepare entries for the owner's approval) or write.
func ValidAccess(access string) bool {
	return access == "read" || access == "propose" || access == "write"
}

type Book struct {
	TenantID             string    `json:"tenant_id"`
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Currency             string    `json:"currency"`
	Scale                int       `json:"scale"`
	CreatedAt            time.Time `json:"created_at"`
	MetadataRevision     *int64    `json:"metadata_revision,omitempty"`
	Logo                 *BookLogo `json:"logo,omitempty"`
	Founder              string    `json:"founder,omitempty"`
	Profile              string    `json:"profile,omitempty"`
	StartDate            string    `json:"start_date,omitempty"`
	FiscalYearStartMonth *int      `json:"fiscal_year_start_month,omitempty"`
	FiscalYearStartDay   *int      `json:"fiscal_year_start_day,omitempty"`
	AccountingTimezone   string    `json:"accounting_timezone,omitempty"`
}
type Account struct {
	ID                   string `json:"id"`
	Class                string `json:"class"`
	Code                 string `json:"code"`
	Name                 string `json:"name"`
	Revision             int64  `json:"revision"`
	Currency             string `json:"currency,omitempty"`
	Scale                *int   `json:"scale,omitempty"`
	CurrencyDefinition   string `json:"currency_definition,omitempty"`
	Role                 string `json:"role,omitempty"`
	CurrencyRevision     *int64 `json:"currency_revision,omitempty"`
	NativeOriginRevision *int64 `json:"native_origin_revision,omitempty"`
	RoleRevision         *int64 `json:"role_revision,omitempty"`
	// ParentAccountID is the display parent, when one is recorded. Parents
	// never move a balance and are never inferred from names or codes.
	ParentAccountID string `json:"parent_account_id,omitempty"`
	// Status is "retired" for a retired account and omitted while active.
	Status string `json:"status,omitempty"`
	// Heading marks an account that groups others and takes no postings.
	Heading bool `json:"heading,omitempty"`
}
type Line struct {
	ID             string               `json:"id,omitempty"`
	AccountID      string               `json:"account_id"`
	Side           string               `json:"side"`
	AmountMinor    string               `json:"amount_minor"`
	Memo           string               `json:"memo,omitempty"`
	ReversesLineID string               `json:"reverses_line_id,omitempty"`
	Native         *NativeMovement      `json:"native,omitempty"`
	Contexts       []ContextMeasurement `json:"contexts,omitempty"`
	Valuation      *Valuation           `json:"valuation,omitempty"`
}
type Entry struct {
	ID                string             `json:"id"`
	Sequence          int64              `json:"sequence"`
	ReceiptID         string             `json:"receipt_id"`
	ActorID           string             `json:"actor_id"`
	EconomicDate      string             `json:"economic_date"`
	AccountingDate    string             `json:"accounting_date"`
	Description       string             `json:"description"`
	ReversalOf        string             `json:"reversal_of,omitempty"`
	RecordedAt        time.Time          `json:"recorded_at"`
	Lines             []Line             `json:"lines"`
	MeasurementPolicy *MeasurementPolicy `json:"measurement_policy,omitempty"`
}
type CreateAccount struct {
	RequestKey   string `json:"request_key"`
	Class        string `json:"class"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Currency     string `json:"currency,omitempty"`
	Scale        *int   `json:"scale,omitempty"`
	Role         string `json:"role,omitempty"`
	NativeOrigin string `json:"native_origin,omitempty"`
}
type ReviseAccount struct {
	RequestKey       string  `json:"request_key"`
	AccountID        string  `json:"account_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	Currency         *string `json:"currency,omitempty"`
	Scale            *int    `json:"scale,omitempty"`
	Role             *string `json:"role,omitempty"`
	// ParentAccountID sets the display parent; an empty string clears it and
	// omission keeps the current parent.
	ParentAccountID *string `json:"parent_account_id,omitempty"`
	// Heading makes the account a heading that takes no new postings, or
	// lets it take postings again; omission keeps the current setting.
	Heading *bool `json:"heading,omitempty"`
}
type PostJournal struct {
	RequestKey        string             `json:"request_key"`
	EconomicDate      string             `json:"economic_date"`
	AccountingDate    string             `json:"accounting_date"`
	Description       string             `json:"description"`
	Lines             []PostLine         `json:"lines"`
	Sources           []SourceInput      `json:"sources,omitempty"`
	MeasurementPolicy *MeasurementPolicy `json:"measurement_policy,omitempty"`
}
type PostLine struct {
	AccountID   string               `json:"account_id"`
	Side        string               `json:"side"`
	AmountMinor string               `json:"amount_minor"`
	Memo        string               `json:"memo,omitempty"`
	Native      *NativeMovement      `json:"native,omitempty"`
	Contexts    []ContextMeasurement `json:"contexts,omitempty"`
	Valuation   *Valuation           `json:"valuation,omitempty"`
}
type ReverseJournal struct {
	RequestKey     string `json:"request_key"`
	EntryID        string `json:"entry_id"`
	AccountingDate string `json:"accounting_date"`
	Reason         string `json:"reason"`
}
type EntryQuery struct {
	EntryID string `json:"entry_id"`
}
type AccountQuery struct {
	AccountID string `json:"account_id"`
	Through   string `json:"through,omitempty"`
}
type ListQuery struct {
	After int64 `json:"after,omitempty"`
	Limit int   `json:"limit,omitempty"`
}
type TrialQuery struct {
	Through string `json:"through,omitempty"`
}
type OutcomeQuery struct {
	RequestKey string `json:"request_key"`
}
type Balance struct {
	RecordedState
	AccountID          string          `json:"account_id"`
	Code               string          `json:"code"`
	Name               string          `json:"name"`
	DebitPositiveMinor string          `json:"debit_positive_minor"`
	Native             *NativePosition `json:"native,omitempty"`
}
type Trial struct {
	RecordedState
	Currency string    `json:"currency"`
	Scale    int       `json:"scale"`
	Through  string    `json:"through,omitempty"`
	Balances []Balance `json:"balances"`
}
type Outcome struct {
	Status    string          `json:"status"`
	Operation string          `json:"operation,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}
type Connection struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	BookID    string     `json:"book_id"`
	ActorID   string     `json:"actor_id"`
	Name      string     `json:"name"`
	Access    string     `json:"access"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
