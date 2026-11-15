package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// projectBook is a personal Book with a card, two expense accounts and card
// activity to record: charges, and one refund.
type projectBook struct {
	*linkedTenant
	kit                 string
	card, software, ads ledger.Account
	writer, proposer    ledger.Scope
}

func newProjectBook(t *testing.T) *projectBook {
	lt := newLinkedTenant(t)
	b := &projectBook{linkedTenant: lt, kit: lt.books["kit"].ID}
	b.card = lt.account("kit", "2100", "liability", "card", "Rewards card")
	b.software = lt.account("kit", "6100", "expense", "", "Software")
	b.ads = lt.account("kit", "6200", "expense", "", "Advertising")
	lt.bank("kit", "card", b.card,
		ledger.BankSyncTransaction{ProviderTransactionID: "d1", Date: "2026-03-02", AmountMinor: 1298, Description: "EXAMPLE DOMAINS ONE", Counterparty: "Example Domains"},
		ledger.BankSyncTransaction{ProviderTransactionID: "d2", Date: "2026-03-03", AmountMinor: 1298, Description: "EXAMPLE DOMAINS TWO", Counterparty: "Example Domains"},
		ledger.BankSyncTransaction{ProviderTransactionID: "d3", Date: "2026-03-04", AmountMinor: 2500, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "d4", Date: "2026-03-05", AmountMinor: 4000, Description: "EXAMPLE HOSTING", Counterparty: "Example Hosting"},
		ledger.BankSyncTransaction{ProviderTransactionID: "d5", Date: "2026-03-06", AmountMinor: -500, Description: "EXAMPLE CLOUD REFUND", Counterparty: "Example Cloud"})
	b.writer, b.proposer = lt.scope(lt.writers["kit"]), lt.scope(lt.agents["kit"])
	return b
}

func (b *projectBook) create(name string) ledger.Project {
	b.t.Helper()
	body, _ := json.Marshal(map[string]any{"book_id": b.kit, "request_key": "project-" + name, "name": name})
	w := bankCall(b.t, b.s, b.f.session, http.MethodPost, projectsPath, string(body))
	var p ledger.Project
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.ID == "" {
		b.t.Fatal("project", w.Code, w.Body.String())
	}
	return p
}

func (b *projectBook) charge(key, description string, account ledger.Account) ledger.RecordBankActivity {
	b.t.Helper()
	return ledger.RecordBankActivity{RequestKey: key, TransactionID: b.item("kit", description).ID, Description: "Example charge", OtherAccountID: account.ID,
		Reason: "The owner said this is for the project"}
}

func (b *projectBook) projects() []ledger.Project {
	b.t.Helper()
	var listed struct{ Projects []ledger.Project }
	if json.Unmarshal(b.f.db.Call(b.t, b.writer, "list_projects", struct{}{}), &listed) != nil {
		b.t.Fatal("list_projects")
	}
	return listed.Projects
}

// labels reads an entry's labels as project name to amount.
func (b *projectBook) labels(entry string) map[string]int64 {
	b.t.Helper()
	rows, err := b.f.db.Admin.Query(b.f.db.Context, `SELECT j.name,p.amount_minor FROM public.project_labels p JOIN public.projects j ON j.id=p.project_id WHERE p.entry_id=$1`, entry)
	if err != nil {
		b.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var amount int64
		if err := rows.Scan(&name, &amount); err != nil {
			b.t.Fatal(err)
		}
		out[name] = amount
	}
	return out
}

func (b *projectBook) decide(p ledger.OperationProposal, decision string) (ledger.OperationDecisionResult, error) {
	return b.f.sessions.DecideOperation(b.f.db.Context, b.f.session, b.f.tenant, ledger.DecideOperation{BookID: b.kit, ProposalID: p.ID, SHA256: p.SHA256,
		RequestKey: decision + "-" + p.ID, Decision: decision, Reason: "Example decision"}, nil)
}

func recorded(t *testing.T, result json.RawMessage) ledger.BankLinkResult {
	t.Helper()
	var out ledger.BankLinkResult
	if json.Unmarshal(result, &out) != nil || out.Entry == nil || out.Entry.ID == "" {
		t.Fatal("no recorded entry", string(result))
	}
	return out
}

// An agent that may record gives a charge its project as it records it. The
// entry is the one it would post without a project, and only money spent
// takes one.
func TestRecordingBankActivityLabelsItsProject(t *testing.T) {
	b := newProjectBook(t)
	app, shop := b.create("Side app"), b.create("Example shop idea")

	// A second open project of the same name is refused, whatever its case
	// or surrounding spaces.
	body, _ := json.Marshal(map[string]any{"book_id": b.kit, "request_key": "again", "name": "  SIDE app "})
	if w := bankCall(t, b.s, b.f.session, http.MethodPost, projectsPath, string(body)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "already has a project") {
		t.Fatal("a second project of the same name", w.Code, w.Body.String())
	}

	// Two charges of the same amount: one plain, one for a project.
	plain := recorded(t, b.f.db.Call(t, b.writer, "record_bank_activity", b.charge("plain", "EXAMPLE DOMAINS ONE", b.software)))
	q := b.charge("labelled", "EXAMPLE DOMAINS TWO", b.software)
	q.ProjectID = app.ID
	first := b.f.db.Call(t, b.writer, "record_bank_activity", q)
	labelled := recorded(t, first)
	if len(labelled.Projects) != 1 || labelled.Projects[0] != (ledger.EntryProject{ProjectID: app.ID, Name: "Side app", AmountMinor: "1298"}) {
		t.Fatalf("projects = %+v", labelled.Projects)
	}
	if len(plain.Entry.Lines) != 2 || len(labelled.Entry.Lines) != 2 {
		t.Fatal("lines", plain.Entry.Lines, labelled.Entry.Lines)
	}
	for i, l := range labelled.Entry.Lines {
		if p := plain.Entry.Lines[i]; l.AccountID != p.AccountID || l.Side != p.Side || l.AmountMinor != p.AmountMinor {
			t.Fatalf("a label changed line %d: %+v, without it %+v", i, l, p)
		}
	}
	if got := b.labels(labelled.Entry.ID); len(got) != 1 || got["Side app"] != 1298 {
		t.Fatal("labels", got)
	}
	if got := b.labels(plain.Entry.ID); len(got) != 0 {
		t.Fatal("an entry recorded without a project is labelled", got)
	}
	if b.balance("kit", "6100") != 2596 || b.balance("kit", "2100") != -2596 || b.balance("kit", "6200") != 0 {
		t.Fatal("balances", b.balance("kit", "6100"), b.balance("kit", "2100"), b.balance("kit", "6200"))
	}
	if again := b.f.db.Call(t, b.writer, "record_bank_activity", q); !bytes.Equal(first, again) {
		t.Fatal("the same request answered differently")
	}

	refusal := func(q ledger.RecordBankActivity, words string) {
		t.Helper()
		entries := b.balance("kit", "2100")
		_, err := b.f.db.Store.Call(b.f.db.Context, b.writer, "record_bank_activity", mustJSON(q))
		refusedWith(t, err, words)
		if b.balance("kit", "2100") != entries {
			t.Fatal("a refused request recorded something")
		}
	}
	// At most one of the three.
	q = b.charge("both", "EXAMPLE CLOUD", b.software)
	q.ProjectID, q.NewProjectName = app.ID, "Another"
	refusal(q, "not several")
	// Parts are of the entry's one spending line, and fit it.
	q = b.charge("line", "EXAMPLE CLOUD", b.software)
	q.ProjectSplits = []ledger.ProjectSplit{{ProjectID: app.ID, LineID: ledger.ID()}}
	refusal(q, "line_id")
	q.ProjectSplits = []ledger.ProjectSplit{{ProjectID: app.ID, AmountMinor: "2000"}, {ProjectID: shop.ID, AmountMinor: "501"}}
	refusal(q, "more than the line")
	// Money coming in takes no project.
	q = b.charge("refund", "EXAMPLE CLOUD REFUND", b.software)
	q.ProjectID = app.ID
	refusal(q, "money coming in")
	if _, err := b.prepare("kit", "prepare-refund", "record_bank_activity", q); err == nil {
		t.Fatal("a project on money coming in was prepared")
	} else {
		refusedWith(t, err, "money coming in")
	}
	// A project of another Book isn't this Book's.
	q = b.charge("foreign", "EXAMPLE CLOUD", b.software)
	q.ProjectID = ledger.ID()
	refusal(q, "isn't one of this Book's")
	// An agent doesn't name a new project on its own, whatever it may record.
	q = b.charge("new", "EXAMPLE CLOUD", b.software)
	q.NewProjectName = "Made up"
	refusal(q, "owner's approval")
	if len(b.projects()) != 2 {
		t.Fatal("a refused request created a project")
	}

	// Parts of the spending go to two projects; the rest stays unlabelled.
	q = b.charge("split", "EXAMPLE CLOUD", b.software)
	q.ProjectSplits = []ledger.ProjectSplit{{ProjectID: app.ID, AmountMinor: "2000"}, {ProjectID: shop.ID, AmountMinor: "300"}}
	split := recorded(t, b.f.db.Call(t, b.writer, "record_bank_activity", q))
	if got := b.labels(split.Entry.ID); len(got) != 2 || got["Side app"] != 2000 || got["Example shop idea"] != 300 {
		t.Fatal("split labels", got)
	}
	// Naming an open project as new labels that project.
	q = b.charge("named", "EXAMPLE HOSTING", b.ads)
	q.NewProjectName = " side APP "
	named := recorded(t, b.f.db.Call(t, b.writer, "record_bank_activity", q))
	if got := b.labels(named.Entry.ID); len(got) != 1 || got["Side app"] != 4000 || len(b.projects()) != 2 {
		t.Fatal("an open project named as new", got, len(b.projects()))
	}
}

// A proposal can name a project that doesn't exist yet. Nothing is created
// until the owner approves, several proposals naming it create it once, and
// a rejected one creates nothing.
func TestProposalsNamingANewProjectCreateItOnce(t *testing.T) {
	b := newProjectBook(t)
	named := func(key, description, name string) ledger.RecordBankActivity {
		q := b.charge(key, description, b.software)
		q.NewProjectName = name
		return q
	}
	// A proposing agent never records directly.
	if _, err := b.f.db.Store.Call(b.f.db.Context, b.proposer, "record_bank_activity", mustJSON(named("direct", "EXAMPLE DOMAINS ONE", "Acme"))); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("a proposing agent recorded directly", err)
	}
	prepare := func(key string, q ledger.RecordBankActivity) ledger.OperationProposal {
		t.Helper()
		p, err := b.prepare("kit", key, "record_bank_activity", q)
		if err != nil {
			t.Fatal(key, err)
		}
		return p
	}
	one := prepare("p1", named("r1", "EXAMPLE DOMAINS ONE", "Acme"))
	two := prepare("p2", named("r2", "EXAMPLE DOMAINS TWO", "  acme "))
	rejected := prepare("p3", named("r3", "EXAMPLE CLOUD", "Never made"))
	change := one.Preview.BankChange
	if change == nil || len(change.Projects) != 1 || change.Projects[0] != (ledger.EntryProject{Name: "Acme", AmountMinor: "1298", New: true}) {
		t.Fatalf("preview = %+v", change)
	}
	if two.Preview.BankChange.Projects[0].Name != "acme" || !two.Preview.BankChange.Projects[0].New {
		t.Fatalf("preview = %+v", two.Preview.BankChange)
	}
	if again := prepare("p1", named("r1", "EXAMPLE DOMAINS ONE", "Acme")); again.ID != one.ID || again.SHA256 != one.SHA256 {
		t.Fatal("the same request key prepared a second proposal")
	}
	if len(b.projects()) != 0 || b.balance("kit", "2100") != 0 {
		t.Fatal("preparing recorded something")
	}

	if out, err := b.decide(rejected, "reject"); err != nil || out.Status != "rejected" {
		t.Fatal("reject", err, out.Status)
	}
	if len(b.projects()) != 0 || b.balance("kit", "2100") != 0 {
		t.Fatal("a rejected proposal recorded something")
	}

	approve := func(p ledger.OperationProposal) ledger.BankLinkResult {
		t.Helper()
		out, err := b.decide(p, "approve")
		if err != nil || out.Status != "committed" || out.Outcome == nil {
			t.Fatal("approve", err, out.Status)
		}
		return recorded(t, out.Outcome.Result)
	}
	first, second := approve(one), approve(two)
	projects := b.projects()
	if len(projects) != 1 || projects[0].Name != "Acme" || projects[0].Archived {
		t.Fatalf("projects = %+v", projects)
	}
	for _, r := range []ledger.BankLinkResult{first, second} {
		if len(r.Projects) != 1 || r.Projects[0].ProjectID != projects[0].ID {
			t.Fatalf("recorded projects = %+v", r.Projects)
		}
		if got := b.labels(r.Entry.ID); len(got) != 1 || got["Acme"] != 1298 {
			t.Fatal("labels", got)
		}
	}
	// The entries are the ones recorded without a project.
	if b.balance("kit", "6100") != 2596 || b.balance("kit", "2100") != -2596 {
		t.Fatal("balances", b.balance("kit", "6100"), b.balance("kit", "2100"))
	}

	// Once the project exists, a proposal names it by its ID.
	_, err := b.prepare("kit", "p4", "record_bank_activity", named("r4", "EXAMPLE HOSTING", "ACME"))
	refusedWith(t, err, projects[0].ID)
	q := b.charge("r5", "EXAMPLE HOSTING", b.ads)
	q.ProjectID = projects[0].ID
	existing := prepare("p5", q)
	if got := existing.Preview.BankChange.Projects; len(got) != 1 || got[0] != (ledger.EntryProject{ProjectID: projects[0].ID, Name: "Acme", AmountMinor: "4000"}) {
		t.Fatalf("preview = %+v", got)
	}
	if got := approve(existing); b.labels(got.Entry.ID)["Acme"] != 4000 || len(b.projects()) != 1 {
		t.Fatal("a proposal for an existing project")
	}
}
