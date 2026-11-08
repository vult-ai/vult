// Package reporting calculates read-only live reports from an authorized ledger
// population. It performs no storage, accounting, network or provider writes.
package reporting

type Kind string

const (
	BalanceSheet    Kind = "balance_sheet"
	ProfitLoss      Kind = "profit_loss"
	TrialBalance    Kind = "trial_balance"
	GeneralLedger   Kind = "general_ledger"
	Journal         Kind = "journal"
	ChangesInEquity Kind = "changes_in_equity"
	CashFlows       Kind = "cash_flows"
	CashMovement    Kind = "cash_movement"
)

// Book is the supported reporting currency and preserved policy metadata, not a
// client-selected conversion or a standards-compliance assertion.
type Book struct {
	AccountingTimezone string `json:"accounting_timezone,omitempty"`
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Currency           string `json:"currency"`
	Scale              int    `json:"scale"`
	StartDate          string `json:"start_date,omitempty"`
	FiscalStartMonth   int    `json:"fiscal_year_start_month"`
	FiscalStartDay     int    `json:"fiscal_year_start_day"`
	RecognitionBasis   string `json:"recognition_basis"`
	ReportingFramework string `json:"reporting_framework"`
}

// Account identity is ID, never a name or leaf label. ParentID is an explicit
// presentation edge supplied by the adapter. Virtual groups carry no postings.
type Account struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	GLCode   string `json:"gl_code,omitempty"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Role     string `json:"role,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Virtual  bool   `json:"virtual,omitempty"`
}

// Treatment comes only from an established reporting treatment supplied by the
// authorized adapter. Economic purpose is descriptive and never excludes money.
// Names, memos and an equity counterpart cannot establish an opening or close.
// Unknown treatment retains recorded effects and an explicit limitation.
type Posting struct {
	ID              string  `json:"id"`
	EntryID         string  `json:"entry_id"`
	EntryReference  string  `json:"entry_reference,omitempty"`
	BookID          string  `json:"book_id"`
	AccountID       string  `json:"account_id"`
	Date            string  `json:"date"`
	Side            string  `json:"side"`
	Amount          string  `json:"amount_minor"`
	Currency        string  `json:"currency"`
	Scale           int     `json:"scale"`
	Treatment       string  `json:"reporting_treatment,omitempty"`
	EconomicPurpose string  `json:"economic_purpose,omitempty"`
	NativeAmount    *string `json:"native_amount_minor,omitempty"`
}

type Provenance struct {
	LedgerRevision  string   `json:"ledger_revision"`
	KnowledgeCutoff string   `json:"knowledge_cutoff"`
	MappingVersion  string   `json:"mapping_version"`
	HierarchyBasis  string   `json:"hierarchy_basis"`
	GeneratedAt     string   `json:"generated_at"`
	Coverage        string   `json:"coverage"`         // complete, partial, unknown; source assertion
	OpeningBalances string   `json:"opening_balances"` // confirmed, partial, unknown
	Limitations     []string `json:"limitations,omitempty"`
}

type Input struct {
	Book       Book       `json:"book"`
	Accounts   []Account  `json:"accounts"`
	Postings   []Posting  `json:"postings"`
	Provenance Provenance `json:"provenance"`
}

type Period struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

type Request struct {
	BookID      string   `json:"book_id"`
	Report      Kind     `json:"report"`
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
	AsOf        string   `json:"as_of,omitempty"`
	Currency    string   `json:"currency,omitempty"`
	Comparison  string   `json:"comparison,omitempty"` // none, previous_period, previous_year, explicit
	CompareFrom string   `json:"compare_from,omitempty"`
	CompareTo   string   `json:"compare_to,omitempty"`
	AccountIDs  []string `json:"account_ids,omitempty"` // GL selection; totals retain full-book scope otherwise
}

type Column struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"` // amount; exact minor-unit values
	Period Period `json:"period"`
}

type Drilldown struct {
	AccountIDs     []string `json:"account_ids"`
	From           string   `json:"from,omitempty"`
	To             string   `json:"to"`
	ExcludeClosing bool     `json:"exclude_closing"`
	ExcludeOpening bool     `json:"exclude_opening"`
	EntryID        string   `json:"entry_id,omitempty"`
	PostingID      string   `json:"posting_id,omitempty"`
	Comparison     *Period  `json:"comparison,omitempty"`
}

// Values include every displayed total; OwnValues separate postings made
// directly to an account from recursive descendants. Clients format, not sum.
type Row struct {
	ID             string            `json:"id"`
	Label          string            `json:"label"`
	Role           string            `json:"role"` // section, account, entry, posting, total, opening, closing
	AccountID      string            `json:"account_id,omitempty"`
	AccountCode    string            `json:"account_code,omitempty"`
	GLCode         string            `json:"gl_code,omitempty"`
	Date           string            `json:"date,omitempty"`
	EntryID        string            `json:"entry_id,omitempty"`
	EntryReference string            `json:"entry_reference,omitempty"`
	PostingID      string            `json:"posting_id,omitempty"`
	Values         map[string]string `json:"values"`
	OwnValues      map[string]string `json:"own_values,omitempty"`
	Children       []Row             `json:"children"`
	Drilldown      *Drilldown        `json:"drilldown,omitempty"`
	Allocations    []Allocation      `json:"allocations,omitempty"`
}

// Allocation is a trusted engine-produced explanation of a typed statement
// component. It never grants a caller permission to classify a ledger posting.
type Allocation struct {
	PostingID          string `json:"posting_id"`
	ColumnID           string `json:"column_id"`
	Amount             string `json:"amount_minor"`
	Category           string `json:"category"`
	TreatmentReference string `json:"treatment_reference"`
}

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // pass, fail, unknown, not_applicable
	Message string `json:"message"`
}

type Scope struct {
	Mode             string  `json:"mode"` // live; no issuance claim
	Period           Period  `json:"period"`
	Comparison       *Period `json:"comparison,omitempty"`
	FiscalStart      string  `json:"fiscal_start"`
	LedgerRevision   string  `json:"ledger_revision"`
	KnowledgeCutoff  string  `json:"knowledge_cutoff"`
	MappingVersion   string  `json:"mapping_version"`
	HierarchyBasis   string  `json:"hierarchy_basis"`
	GeneratedAt      string  `json:"generated_at"`
	PopulationDigest string  `json:"population_digest"`
	Coverage         string  `json:"coverage"`
	OpeningBalances  string  `json:"opening_balances"`
}

type Result struct {
	SchemaVersion  int               `json:"schema_version"`
	Report         Kind              `json:"report"`
	Title          string            `json:"title"`
	Book           Book              `json:"book"`
	Request        Request           `json:"request"`
	Scope          Scope             `json:"scope"`
	Columns        []Column          `json:"columns"`
	Rows           []Row             `json:"rows"`
	Checks         []Check           `json:"checks"`
	Findings       []Finding         `json:"findings"`
	Limitations    []string          `json:"limitations"`
	Reconciliation map[string]string `json:"reconciliation"`
	Status         string            `json:"status"` // ready, partial, unsupported; never a compliance claim
	Digest         string            `json:"digest"`
}

type Finding struct {
	Code        string   `json:"code"`
	EntryIDs    []string `json:"entry_ids"`
	AmountMinor string   `json:"amount_minor"`
	Message     string   `json:"message"`
}

type Contribution struct {
	PostingID          string `json:"posting_id"`
	EntryID            string `json:"entry_id"`
	EntryReference     string `json:"entry_reference,omitempty"`
	AccountID          string `json:"account_id"`
	Date               string `json:"date"`
	Side               string `json:"side"`
	Amount             string `json:"amount_minor"`
	SignedAmount       string `json:"signed_amount_minor"` // debit positive, credit negative
	Treatment          string `json:"reporting_treatment"`
	ColumnID           string `json:"column_id"`
	ReportAmount       string `json:"report_amount_minor"`
	Category           string `json:"category,omitempty"`
	TreatmentReference string `json:"treatment_reference,omitempty"`
}

type ContributionResult struct {
	ResultDigest  string         `json:"result_digest"`
	RowID         string         `json:"row_id"`
	ColumnID      string         `json:"column_id"`
	Contributions []Contribution `json:"contributions"`
	Debit         string         `json:"debit_minor"`
	Credit        string         `json:"credit_minor"`
	Total         string         `json:"total_minor"`
}

type Error struct{ Code string }

func (e *Error) Error() string { return "financial report: " + e.Code }
