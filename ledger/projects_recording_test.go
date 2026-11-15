package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func listedProjects(t *testing.T, f *bankFixture) []ledger.Project {
	t.Helper()
	var listed struct{ Projects []ledger.Project }
	if json.Unmarshal(f.db.Call(t, f.s, "list_projects", struct{}{}), &listed) != nil {
		t.Fatal("list_projects")
	}
	return listed.Projects
}

// A proposal naming a new project creates it when the approved work runs,
// not when it is prepared, and labels the entry's spending in full.
func TestApprovedBankActivityCreatesItsNewProject(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "t1", "2026-03-02", -4500, "Example Office Supply"),
		f.tx("acc-checking", "t2", "2026-03-03", 120000, "Example Customer payment"),
	}, nil)
	page := f.inbox(t, f.s, "")
	proposer := connectionScope(t, f.db, f.book.ID, "propose")
	q := ledger.RecordBankActivity{RequestKey: "record", TransactionID: byDescription(page, "Example Office Supply").ID, Description: "Office supplies",
		OtherAccountID: f.office.ID, Reason: "The owner said this is for Acme", NewProjectName: "  Acme "}
	p := prepareOperation(t, f.db, proposer, "prepare", "record_bank_activity", q)
	change := p.Preview.BankChange
	if change == nil || len(change.Projects) != 1 || change.Projects[0] != (ledger.EntryProject{Name: "Acme", AmountMinor: "4500", New: true}) || len(p.Preview.Lines) != 2 {
		t.Fatalf("preview = %+v", p.Preview)
	}
	if len(listedProjects(t, f)) != 0 {
		t.Fatal("preparing a proposal created its project")
	}
	approveOperation(t, f.db, proposer, p, f.s)
	var out ledger.BankLinkResult
	if json.Unmarshal(executeOperation(t, f.db, f.s, p), &out) != nil || out.Entry == nil || len(out.Projects) != 1 {
		t.Fatalf("recorded = %+v", out)
	}
	projects := listedProjects(t, f)
	if len(projects) != 1 || projects[0].Name != "Acme" || projects[0].ID != out.Projects[0].ProjectID {
		t.Fatalf("projects = %+v", projects)
	}
	var labelled, expense int64
	succeeded(t, f.db.Admin.QueryRow(f.db.Context, `SELECT p.amount_minor,(SELECT count(*) FROM public.accounts a WHERE a.id=l.account_id AND a.class='expense')
 FROM public.project_labels p JOIN public.lines l ON l.id=p.line_id WHERE p.entry_id=$1 AND p.project_id=$2`, out.Entry.ID, projects[0].ID).Scan(&labelled, &expense))
	if labelled != 4500 || expense != 1 {
		t.Fatal("the label isn't the whole spending line", labelled, expense)
	}

	// Money coming in takes no project, recorded directly or prepared.
	income := ledger.Account{}
	for _, a := range listedAccounts(t, f.db, f.s) {
		if a.Class == "income" && !a.Heading {
			income = a
			break
		}
	}
	in := ledger.RecordBankActivity{RequestKey: "in", TransactionID: byDescription(page, "Example Customer payment").ID, Description: "Customer payment",
		OtherAccountID: income.ID, ProjectID: projects[0].ID}
	_, err := f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(in))
	bankRefused(t, err, "money coming in")
	_, err = f.db.Store.Call(f.db.Context, proposer, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "prepare-in", Operation: "record_bank_activity", Arguments: value(in)}))
	bankRefused(t, err, "money coming in")
	if page := f.inbox(t, f.s, ""); page.Summary.Unrecorded != 1 {
		t.Fatal("a refused request recorded the activity")
	}
}
