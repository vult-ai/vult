package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/ledger"
)

// outsideAgent is one agent connected to the Book over MCP at an access
// level, as an agent's app outside this program is.
type outsideAgent struct {
	f      *documentServerFixture
	token  string
	scope  ledger.Scope
	client *mcp.ClientSession
}

func connectOutsideAgent(t *testing.T, f *documentServerFixture, level string) outsideAgent {
	t.Helper()
	// An agent connects to read; the owner then sets the level it works at.
	token := f.token(t, "read")
	scope, err := f.sessions.ResolveAgent(f.db.Context, token)
	if err != nil {
		t.Fatal(err)
	}
	if level != "read" {
		if err = f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
			return o.ChangeAccess(f.db.Context, scope.ConnectionID, level)
		}); err != nil {
			t.Fatal(err)
		}
		if scope, err = f.sessions.ResolveAgent(f.db.Context, token); err != nil || scope.Access != level {
			t.Fatal("the agent's level did not change", err)
		}
	}
	return outsideAgent{f: f, token: token, scope: scope, client: f.client(t, token)}
}

// call uses one tool and returns its text, or the refusal the agent reads.
func (a outsideAgent) call(method string, args any) (json.RawMessage, error) {
	result, err := a.client.CallTool(a.f.db.Context, &mcp.CallToolParams{Name: method, Arguments: args})
	if err != nil {
		return nil, err
	}
	text := ""
	if len(result.Content) > 0 {
		if c, ok := result.Content[0].(*mcp.TextContent); ok {
			text = c.Text
		}
	}
	if result.IsError {
		return nil, errors.New(text)
	}
	return json.RawMessage(text), nil
}

// ledgerCall is the same request as the ledger answers it, before the
// server words the refusal for the agent.
func (a outsideAgent) ledgerCall(method string, args any) (json.RawMessage, error) {
	input, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return remoteStore(a.f.db.Runtime, a.f.sessions, a.token, nil, a.f.vault).Call(a.f.db.Context, a.scope, method, input)
}

func (a outsideAgent) prepare(key, operation string, args any) (ledger.OperationProposal, error) {
	var p ledger.OperationProposal
	data, err := json.Marshal(args)
	if err != nil {
		return p, err
	}
	out, err := a.call("prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: operation, Arguments: data})
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(out, &p)
}

func (a outsideAgent) offers(t *testing.T, tool string) bool {
	t.Helper()
	list, err := a.client.ListTools(a.f.db.Context, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list.Tools {
		if v.Name == tool {
			return true
		}
	}
	return false
}

// outsideAgentBook is a Book with a few accounts, made by a connection that
// may write.
func outsideAgentBook(t *testing.T) (f *documentServerFixture, writer ledger.Scope, checking, card, expense ledger.Account) {
	t.Helper()
	f = documentServer(t)
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(f.db.Context, f.book.ID, "Example bookkeeper", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	writer = ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	account := func(code, class, role string) (a ledger.Account) {
		if json.Unmarshal(f.db.Call(t, writer, "create_account", ledger.CreateAccount{RequestKey: "create-" + code, Class: class, Code: code, Name: "Example " + code, Role: role}), &a) != nil || a.ID == "" {
			t.Fatal("invalid account result")
		}
		return a
	}
	return f, writer, account("1000", "asset", "cash"), account("2100", "liability", "card"), account("6100", "expense", "")
}

// outsideAgentBank connects a synthetic bank with a checking account and a
// card, links them to the Book's accounts and reports the given activity.
func outsideAgentBank(t *testing.T, f *documentServerFixture, checking, card ledger.Account, added []ledger.BankSyncTransaction) {
	t.Helper()
	ctx, book := f.db.Context, f.book.ID
	var login string
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, book, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: "item-" + ledger.ID(),
			InstitutionName: "Example Bank", TokenSealed: make([]byte, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: book, ConnectionID: login}
	if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{NextCursor: "c1", Complete: true, Accounts: []ledger.BankSyncAccount{
		{ProviderAccountID: "checking", Name: "Example Checking", Mask: "0001", Type: "depository", Subtype: "checking", Kind: "cash", Currency: f.book.Currency, Scale: f.book.Scale},
		{ProviderAccountID: "card", Name: "Example Card", Mask: "0002", Type: "credit", Subtype: "credit card", Kind: "card", Currency: f.book.Currency, Scale: f.book.Scale},
	}}); err != nil {
		t.Fatal(err)
	}
	var views []ledger.BankConnectionView
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		views, err = o.BankConnections(ctx, book)
		return err
	}); err != nil || len(views) != 1 {
		t.Fatal("bank connections", err)
	}
	for _, a := range views[0].Accounts {
		account := checking.ID
		if a.Kind == "card" {
			account = card.ID
		}
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			return o.LinkBankAccount(ctx, book, who, a.ID, account)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{FromCursor: "c1", NextCursor: "c2", Complete: true, Added: added}); err != nil {
		t.Fatal(err)
	}
}

func outsideBankLine(f *documentServerFixture, account, id, date string, amount int64, description string) ledger.BankSyncTransaction {
	return ledger.BankSyncTransaction{ProviderAccountID: account, ProviderTransactionID: id, Date: date, AmountMinor: amount,
		Currency: f.book.Currency, Scale: f.book.Scale, Description: description}
}

// ownerDecides records the owner's decision on a proposal from their
// signed-in session.
func ownerDecides(f *documentServerFixture, p ledger.OperationProposal, decision string) (ledger.OperationDecisionResult, error) {
	return f.sessions.DecideOperation(f.db.Context, f.session, f.tenant, ledger.DecideOperation{BookID: f.book.ID, ProposalID: p.ID, SHA256: p.SHA256,
		RequestKey: decision + "-" + p.ID, Decision: decision, Reason: "Example owner decision"}, nil)
}

func ownerReads(t *testing.T, f *documentServerFixture, read func(ledger.TenantAdmin) error) {
	t.Helper()
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, false, read); err != nil {
		t.Fatal(err)
	}
}

func outsideJournal(key, debit, credit, amount, description string) ledger.PostJournal {
	return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-03-02", AccountingDate: "2026-03-02", Description: description,
		Lines: []ledger.PostLine{{AccountID: debit, Side: "debit", AmountMinor: amount}, {AccountID: credit, Side: "credit", AmountMinor: amount}}}
}

func outsideInbox(t *testing.T, a outsideAgent, status string) ledger.BankActivityPage {
	t.Helper()
	var page ledger.BankActivityPage
	out, err := a.call("list_bank_activity", ledger.BankActivityQuery{Status: status})
	if err != nil || json.Unmarshal(out, &page) != nil {
		t.Fatal("list_bank_activity", err)
	}
	return page
}

func outsideItem(page ledger.BankActivityPage, description string) ledger.BankTransaction {
	for _, v := range page.Items {
		if v.Description == description {
			return v
		}
	}
	return ledger.BankTransaction{}
}

// An agent's waiting changes are limited per agent: one agent holds at most
// 128 undecided proposals in a Book. The limit is its own, so another agent
// still prepares work, and the owner decides every one of them as usual. A
// decision frees a place.
func TestOutsideAgentWaitingChangesAreLimitedPerAgent(t *testing.T) {
	f, _, checking, _, expense := outsideAgentBook(t)
	first, second := connectOutsideAgent(t, f, "propose"), connectOutsideAgent(t, f, "propose")
	if first.scope.ActorID == second.scope.ActorID || first.scope.ConnectionID == second.scope.ConnectionID {
		t.Fatal("two agents share an identity")
	}
	var held []ledger.OperationProposal
	var refusal error
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("lunch-%03d", i)
		p, err := first.prepare("prepare-"+key, "post_journal", outsideJournal(key, expense.ID, checking.ID, "1200", "Example lunch"))
		if err != nil {
			refusal = err
			break
		}
		held = append(held, p)
	}
	if len(held) != 128 || refusal == nil {
		t.Fatalf("an agent held %d waiting proposals before a refusal (%v), want 128", len(held), refusal)
	}
	// The refusal reaches the agent as the limit it is, not as a failure
	// that invites a retry.
	if refusal.Error() != ledger.ErrLimit.Error() {
		t.Fatalf("refusal = %q", refusal)
	}
	// The ledger's own refusal is its record limit.
	args, _ := json.Marshal(outsideJournal("direct", expense.ID, checking.ID, "1200", "Example lunch"))
	if _, err := first.ledgerCall("prepare_operation", ledger.PrepareOperation{RequestKey: "prepare-direct", Operation: "post_journal", Arguments: args}); !errors.Is(err, ledger.ErrLimit) {
		t.Fatalf("ledger refusal = %v", err)
	}
	// Retrying a preparation already held returns it instead of a refusal.
	again, err := first.prepare("prepare-lunch-000", "post_journal", outsideJournal("lunch-000", expense.ID, checking.ID, "1200", "Example lunch"))
	if err != nil || again.ID != held[0].ID {
		t.Fatalf("a retried preparation = %q, %v", again.ID, err)
	}
	// Another agent in the same Book isn't held back by the first one's queue.
	other, err := second.prepare("prepare-other", "post_journal", outsideJournal("other", expense.ID, checking.ID, "900", "Example parking"))
	if err != nil {
		t.Fatalf("a second agent was refused: %v", err)
	}
	// The owner sees all of it waiting.
	ownerReads(t, f, func(o ledger.TenantAdmin) error {
		page, err := o.OperationProposals(f.db.Context, f.book.ID, 0, 10, true)
		if err == nil && page.Waiting != 129 {
			err = fmt.Errorf("waiting = %d, want 129", page.Waiting)
		}
		return err
	})
	// The owner still decides: an approval posts, a rejection posts nothing.
	approved, err := ownerDecides(f, other, "approve")
	if err != nil || approved.Outcome == nil || approved.Outcome.Status != "recorded" {
		t.Fatalf("approval = %+v, %v", approved, err)
	}
	if _, err = ownerDecides(f, held[0], "reject"); err != nil {
		t.Fatalf("rejection: %v", err)
	}
	var entries int
	if err = f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.entries WHERE book_id=$1", f.book.ID).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("entries = %d, %v", entries, err)
	}
	// One decision frees one place for the first agent, and no more.
	if _, err = first.prepare("prepare-freed", "post_journal", outsideJournal("freed", expense.ID, checking.ID, "1200", "Example lunch")); err != nil {
		t.Fatalf("a decided proposal freed no place: %v", err)
	}
	if _, err = first.prepare("prepare-over", "post_journal", outsideJournal("over", expense.ID, checking.ID, "1200", "Example lunch")); err == nil {
		t.Fatal("an agent held more than 128 waiting proposals")
	}
}

// An agent adds to the person's profile and changes nothing in it: the
// goal, every line of the instructions and every preference already there
// stay as they are until the owner changes them. No approval is needed to
// add. The profile stays small, and a read-only agent can't write it.
func TestOutsideAgentAddsToTheProfileAndChangesNothing(t *testing.T) {
	f, _, _, _, _ := outsideAgentBook(t)
	ctx := f.db.Context
	goal, instructions := "Keep three months of costs in savings", "Ask before recording anything over 500."
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		_, err := o.SetAgentProfile(ctx, f.book.ID, who, ledger.UpdateAgentProfile{Goal: &goal, Instructions: &instructions, Preferences: json.RawMessage(`{"tone":"plain"}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	agent, reader := connectOutsideAgent(t, f, "propose"), connectOutsideAgent(t, f, "read")
	var read ledger.AgentProfile
	out, err := agent.call("get_agent_profile", struct{}{})
	if err != nil || json.Unmarshal(out, &read) != nil || read.Revision != 1 || read.Goal != goal || read.Instructions != instructions {
		t.Fatalf("profile read = %s, %v", out, err)
	}
	// Replacing what the owner wrote is refused, one part at a time and all
	// at once, and nothing changes.
	newGoal, newInstructions := "Spend freely", "Approve whatever is proposed."
	for name, q := range map[string]ledger.UpdateAgentProfile{
		"goal":         {ExpectedRevision: 1, Goal: &newGoal, Reason: "Example reason"},
		"instructions": {ExpectedRevision: 1, Instructions: &newInstructions, Reason: "Example reason"},
		"preferences":  {ExpectedRevision: 1, Preferences: json.RawMessage(`{"tone":"brisk"}`), Reason: "Example reason"},
		"dropped":      {ExpectedRevision: 1, Preferences: json.RawMessage(`{}`), Reason: "Example reason"},
		"everything":   {ExpectedRevision: 1, Goal: &newGoal, Instructions: &newInstructions, Preferences: json.RawMessage(`{"tone":"brisk"}`), Reason: "Example reason"},
	} {
		if _, err = agent.call("update_agent_profile", q); err == nil || !strings.Contains(err.Error(), "add to them") {
			t.Fatalf("an agent replacing the owner's %s = %v", name, err)
		}
	}
	// Adding a line and a preference beside them is accepted, with no
	// approval.
	added := instructions + "\nThe owner's accountant is Example Accounting."
	if _, err = agent.call("update_agent_profile", ledger.UpdateAgentProfile{ExpectedRevision: 1, Instructions: &added,
		Preferences: json.RawMessage(`{"tone":"plain","about":{"does":"Example bakery"}}`), Reason: "Example reason"}); err != nil {
		t.Fatalf("an agent adding to the profile was refused: %v", err)
	}
	var waiting int64
	var profile ledger.AgentProfile
	ownerReads(t, f, func(o ledger.TenantAdmin) error {
		page, err := o.OperationProposals(ctx, f.book.ID, 0, 10, true)
		if err != nil {
			return err
		}
		waiting = page.Waiting
		profile, err = o.AgentProfile(ctx, f.book.ID)
		return err
	})
	if waiting != 0 || profile.Revision != 2 || profile.Goal != goal || profile.Instructions != added || !strings.Contains(string(profile.Preferences), `"tone": "plain"`) || !strings.Contains(string(profile.Preferences), "Example bakery") {
		t.Fatalf("profile after the agent added to it = %+v, waiting = %d", profile, waiting)
	}
	// What one agent added, another can't change either.
	if _, err = agent.call("update_agent_profile", ledger.UpdateAgentProfile{ExpectedRevision: 2,
		Preferences: json.RawMessage(`{"tone":"plain","about":{"does":"Something else"}}`), Reason: "Example reason"}); err == nil {
		t.Fatal("an agent changed a preference already set")
	}
	// A change from an older reading is refused, in words that say why.
	_, err = agent.call("update_agent_profile", ledger.UpdateAgentProfile{ExpectedRevision: 1, Goal: &goal, Reason: "Example reason"})
	if err == nil || !strings.Contains(err.Error(), "changed since you read it") {
		t.Fatalf("a stale profile change = %v", err)
	}
	// Size limits: a goal of 280 characters, instructions of 1024 and
	// preferences of 2048 bytes, as one JSON object. More is refused and
	// changes nothing.
	fits, long := added+"\n"+strings.Repeat("a", 1024-len(added)-1), added+"\n"+strings.Repeat("a", 1024-len(added))
	longGoal := strings.Repeat("g", 281)
	bigPreferences, _ := json.Marshal(map[string]any{"tone": "plain", "about": map[string]string{"does": "Example bakery"}, "note": strings.Repeat("p", 2100)})
	for name, q := range map[string]ledger.UpdateAgentProfile{
		"instructions": {ExpectedRevision: 2, Instructions: &long, Reason: "Example reason"},
		"goal":         {ExpectedRevision: 2, Goal: &longGoal, Reason: "Example reason"},
		"preferences":  {ExpectedRevision: 2, Preferences: bigPreferences, Reason: "Example reason"},
		"list":         {ExpectedRevision: 2, Preferences: json.RawMessage(`["plain"]`), Reason: "Example reason"},
	} {
		if _, err = agent.call("update_agent_profile", q); err == nil || strings.Contains(err.Error(), "relation") || strings.Contains(err.Error(), "constraint") {
			t.Fatalf("an oversize or malformed %s = %v", name, err)
		}
	}
	if _, err = agent.call("update_agent_profile", ledger.UpdateAgentProfile{ExpectedRevision: 2, Instructions: &fits, Reason: "Example reason"}); err != nil {
		t.Fatalf("instructions at the limit were refused: %v", err)
	}
	// A read-only agent isn't offered the tool, and the ledger denies it.
	if reader.offers(t, "update_agent_profile") || !agent.offers(t, "update_agent_profile") {
		t.Fatal("the profile tool isn't offered by access level")
	}
	if _, err = reader.ledgerCall("get_agent_profile", struct{}{}); err != nil {
		t.Fatalf("a read-only agent's profile read = %v", err)
	}
	if _, err = reader.ledgerCall("update_agent_profile", ledger.UpdateAgentProfile{ExpectedRevision: 3, Goal: &goal, Reason: "Example reason"}); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("a read-only agent's profile change = %v", err)
	}
}

// A proposal shows the owner each account's type beside its name when the
// account is created, and by type alone when money is sent to it: a new
// account named like an expense but typed equity says "equity" in both
// previews. Spending recorded against equity draws no warning.
func TestOutsideAgentProposalsShowAccountTypesNotNames(t *testing.T) {
	f, _, checking, _, _ := outsideAgentBook(t)
	agent := connectOutsideAgent(t, f, "propose")
	created, err := agent.prepare("prepare-meals", "create_account", ledger.CreateAccount{RequestKey: "meals", Code: "3900", Name: "Meals", Class: "equity"})
	if err != nil {
		t.Fatal(err)
	}
	changes := created.Preview.AccountChanges
	if len(changes) != 1 || changes[0].Change != "create" || changes[0].Name != "Meals" || changes[0].Class != "equity" || len(created.Preview.Warnings) != 0 {
		t.Fatalf("account preview = %+v, warnings = %v", changes, created.Preview.Warnings)
	}
	done, err := ownerDecides(f, created, "approve")
	if err != nil || done.Outcome == nil {
		t.Fatalf("approval = %+v, %v", done, err)
	}
	var meals ledger.Account
	if json.Unmarshal(done.Outcome.Result, &meals) != nil || meals.Class != "equity" || meals.Name != "Meals" {
		t.Fatalf("created account = %s", done.Outcome.Result)
	}
	spend, err := agent.prepare("prepare-lunch", "post_journal", outsideJournal("lunch", meals.ID, checking.ID, "4500", "Team lunch"))
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]string{}
	for _, a := range spend.Preview.Accounts {
		classes[a.AccountID] = a.Class
	}
	if classes[meals.ID] != "equity" || classes[checking.ID] != "asset" || len(spend.Preview.Warnings) != 0 {
		t.Fatalf("spending preview accounts = %+v, warnings = %v", spend.Preview.Accounts, spend.Preview.Warnings)
	}
	// The stored preview names accounts by ID and type only; the account's
	// name is read separately.
	if strings.Contains(string(mustJSON(spend.Preview)), "Meals") {
		t.Fatal("the stored preview carries the account's name")
	}
	var state ledger.ProposalState
	ownerReads(t, f, func(o ledger.TenantAdmin) error {
		var err error
		state, err = o.OperationProposal(f.db.Context, f.book.ID, spend.ID)
		return err
	})
	if len(state.Proposal.Preview.Warnings) != 0 {
		t.Fatalf("owner warnings = %v", state.Proposal.Preview.Warnings)
	}
	if posted, err := ownerDecides(f, spend, "approve"); err != nil || posted.Outcome == nil || posted.Outcome.Status != "recorded" {
		t.Fatalf("approval = %+v, %v", posted, err)
	}
}

// Statement lines an agent typed in reach the inbox only with the owner's
// approval of the import. Once approved they wait there as unrecorded
// activity, marked as coming from a statement rather than the bank, with the
// statement and the document they cite.
func TestOutsideAgentStatementLinesNeedApprovalAndSayTheirSource(t *testing.T) {
	f, writer, checking, card, _ := outsideAgentBook(t)
	ctx := f.db.Context
	outsideAgentBank(t, f, checking, card, []ledger.BankSyncTransaction{outsideBankLine(f, "checking", "b-1", "2026-04-02", -4500, "EXAMPLE OFFICE SUPPLY")})
	f.db.Store.Documents = f.vault
	data := []byte("%PDF-1.4\n% synthetic statement\n")
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(f.db.Call(t, writer, "add_document", ledger.AddDocument{RequestKey: "statement", Name: "statement.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid document intake")
	}
	if _, err := f.db.Store.Upload(ctx, writer, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	agent := connectOutsideAgent(t, f, "propose")
	// The document says nothing of these lines; the agent supplies them.
	statement := ledger.ImportStatement{RequestKey: "april", AccountID: checking.ID, DocumentID: intake.Document.ID,
		PeriodStart: "2026-04-01", PeriodEnd: "2026-04-30", OpeningMinor: "0", ClosingMinor: "-95000", Rows: []ledger.StatementRowInput{
			{RowNumber: 1, Date: "2026-04-12", AmountMinor: "-95000", Description: "Example Consulting invoice", Raw: "2026-04-12,Example Consulting invoice,-950.00"},
		}}
	if _, err := agent.call("import_statement", statement); err == nil {
		t.Fatal("a proposing agent imported a statement directly")
	}
	p, err := agent.prepare("prepare-april", "import_statement", statement)
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Preview.StatementChange; c == nil || c.Change != "import" || c.Rows != 1 || c.AccountID != checking.ID {
		t.Fatalf("statement preview = %+v", c)
	}
	if got := outsideInbox(t, agent, "all"); len(got.Items) != 1 {
		t.Fatalf("a prepared import reached the inbox: %d items", len(got.Items))
	}
	if done, err := ownerDecides(f, p, "approve"); err != nil || done.Outcome == nil || done.Outcome.Status != "recorded" {
		t.Fatalf("approval = %+v, %v", done, err)
	}
	page := outsideInbox(t, agent, "")
	typed, reported := outsideItem(page, "Example Consulting invoice"), outsideItem(page, "EXAMPLE OFFICE SUPPLY")
	if len(page.Items) != 2 || page.Summary.Unrecorded != 2 {
		t.Fatalf("inbox = %d items, %d unrecorded", len(page.Items), page.Summary.Unrecorded)
	}
	if typed.Source != "statement" || typed.Status != "unrecorded" || typed.AccountID != checking.ID || typed.AmountMinor != "-95000" ||
		typed.StatementID == "" || typed.StatementRowID == "" || typed.DocumentID != intake.Document.ID {
		t.Fatalf("typed line = %+v", typed)
	}
	if reported.Source != "bank" || reported.Status != "unrecorded" || reported.StatementID != "" || reported.DocumentID != "" {
		t.Fatalf("bank line = %+v", reported)
	}
}

// A transfer between the Book's own accounts is two items of the same
// money within a month of each other. Money that left one account and an
// equal amount that reached another months later are two things: an agent
// naming both is refused, and each is recorded on its own.
func TestOutsideAgentTransferNeedsItemsCloseInTime(t *testing.T) {
	f, _, checking, card, _ := outsideAgentBook(t)
	outsideAgentBank(t, f, checking, card, []ledger.BankSyncTransaction{
		outsideBankLine(f, "checking", "out", "2026-01-05", -25000, "Example payment out"),
		outsideBankLine(f, "card", "in", "2026-09-20", -25000, "Example payment received"),
	})
	agent := connectOutsideAgent(t, f, "propose")
	page := outsideInbox(t, agent, "")
	out, in := outsideItem(page, "Example payment out"), outsideItem(page, "Example payment received")
	if out.ID == "" || in.ID == "" {
		t.Fatalf("inbox = %+v", page.Items)
	}
	var suggestions []ledger.BankSuggestion
	data, err := agent.call("suggest_bank_matches", struct{}{})
	if err != nil || json.Unmarshal(data, &suggestions) != nil || len(suggestions) != 0 {
		t.Fatalf("suggestions = %s, %v", data, err)
	}
	transfer := ledger.RecordBankTransfer{RequestKey: "transfer", TransactionIDs: []string{out.ID, in.ID}}
	if _, err = agent.call("record_bank_transfer", transfer); err == nil || err.Error() != ledger.ErrDenied.Error() {
		t.Fatalf("a proposing agent's direct transfer without the owner's grant = %v", err)
	}
	if _, err = agent.prepare("prepare-transfer", "record_bank_transfer", transfer); err == nil || !strings.Contains(err.Error(), "days apart") {
		t.Fatalf("a transfer of items months apart = %v", err)
	}
	if left := outsideInbox(t, agent, ""); left.Summary.Unrecorded != 2 {
		t.Fatalf("unrecorded after the refused transfer = %d", left.Summary.Unrecorded)
	}
}

// Each agent's notes are its own to manage. An agent has at most 50
// questions waiting for the owner, so it can't push another agent's off the
// owner's list; a question is withdrawn, and a loose end closed, only by
// the agent that made it or by the owner. Loose ends are limited to 50 open
// in a Book, shared by all its agents.
func TestOutsideAgentNotesAreSharedAcrossTheBook(t *testing.T) {
	f, _, _, _, _ := outsideAgentBook(t)
	ctx := f.db.Context
	first, second := connectOutsideAgent(t, f, "propose"), connectOutsideAgent(t, f, "propose")
	var earlier ledger.AgentQuestion
	out, err := second.call("ask_question", ledger.AskQuestion{SubjectKind: "other", SubjectKey: "rent", Question: "Is the March rent a business cost?"})
	if err != nil || json.Unmarshal(out, &earlier) != nil || earlier.ID == "" {
		t.Fatalf("question = %s, %v", out, err)
	}
	for i := 0; i < 50; i++ {
		if _, err = first.call("ask_question", ledger.AskQuestion{SubjectKind: "other", SubjectKey: fmt.Sprintf("subject-%03d", i), Question: "Example question?"}); err != nil {
			t.Fatalf("question %d was refused: %v", i+1, err)
		}
	}
	_, err = first.call("ask_question", ledger.AskQuestion{SubjectKind: "other", SubjectKey: "one-too-many", Question: "Example question?"})
	if err == nil || !strings.Contains(err.Error(), "already waiting for the owner") {
		t.Fatalf("a question over the agent's limit = %v", err)
	}
	var open int
	if err = f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.agent_questions WHERE book_id=$1 AND status='open'", f.book.ID).Scan(&open); err != nil || open != 51 {
		t.Fatalf("open questions = %d, %v", open, err)
	}
	var listed []ledger.AgentQuestion
	ownerReads(t, f, func(o ledger.TenantAdmin) error {
		var err error
		listed, err = o.AgentQuestions(ctx, f.book.ID, ledger.QuestionQuery{})
		return err
	})
	shown := false
	for _, q := range listed {
		shown = shown || q.ID == earlier.ID
	}
	if len(listed) != 51 || !shown {
		t.Fatalf("the owner's list holds %d questions; the earliest shown = %v", len(listed), shown)
	}
	// The same subject is never asked twice, by any agent.
	_, err = first.call("ask_question", ledger.AskQuestion{SubjectKind: "other", SubjectKey: "rent", Question: "Again?"})
	if err == nil || !strings.Contains(err.Error(), "Already asked") {
		t.Fatalf("a repeated question = %v", err)
	}
	// Only its asker or the owner withdraws a question.
	_, err = first.call("withdraw_question", ledger.WithdrawQuestion{QuestionID: earlier.ID, Reason: "Example reason"})
	if err == nil || !strings.Contains(err.Error(), "withdrawn by whoever asked it") {
		t.Fatalf("withdrawing another agent's question = %v", err)
	}
	if _, err = second.call("withdraw_question", ledger.WithdrawQuestion{QuestionID: earlier.ID, Reason: "Example reason"}); err != nil {
		t.Fatalf("the asker's withdrawal was refused: %v", err)
	}
	// One agent's loose ends use up the Book's 50 for every agent.
	var reminder ledger.AgentLoop
	out, err = second.call("open_loop", ledger.OpenLoop{RequestKey: "reminder", Kind: "reminder", Text: "Remind the owner about the tax deadline"})
	if err != nil || json.Unmarshal(out, &reminder) != nil || reminder.ID == "" {
		t.Fatalf("reminder = %s, %v", out, err)
	}
	for i := 0; i < 49; i++ {
		if _, err = first.call("open_loop", ledger.OpenLoop{RequestKey: fmt.Sprintf("loop-%02d", i), Kind: "follow_up", Text: "Example follow-up"}); err != nil {
			t.Fatalf("loose end %d was refused: %v", i+1, err)
		}
	}
	_, err = second.call("open_loop", ledger.OpenLoop{RequestKey: "one-more", Kind: "reminder", Text: "Example reminder"})
	if err == nil || !strings.Contains(err.Error(), "50 open loops") {
		t.Fatalf("a loose end over the limit = %v", err)
	}
	// An agent can't close another agent's loose end: the reminder stays on
	// the owner's list until the agent that opened it closes it.
	if _, err = first.call("close_loop", ledger.CloseLoop{LoopID: reminder.ID, Status: "dropped", Reason: "Example reason"}); err == nil {
		t.Fatal("an agent closed another agent's loose end")
	}
	var reminders []ledger.AgentLoop
	ownerReads(t, f, func(o ledger.TenantAdmin) error {
		var err error
		reminders, err = o.OwnerReminders(ctx, f.book.ID)
		return err
	})
	if len(reminders) != 1 || reminders[0].ID != reminder.ID {
		t.Fatalf("reminders after another agent tried to close one = %+v", reminders)
	}
	if _, err = second.call("close_loop", ledger.CloseLoop{LoopID: reminder.ID, Status: "done", Reason: "Example reason"}); err != nil {
		t.Fatalf("the agent that opened a loose end couldn't close it: %v", err)
	}
}
