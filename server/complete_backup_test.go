package server

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// A backup is the whole Vult: restored into an empty Vult, every table
// holds the same rows, including bank activity and how each item was
// recorded, rules, agents' notes, linked Books with their pairs,
// settlements and opening balances, and document filings. Only where
// originals are kept changes, as they are sealed under the new Vult's keys.
func TestABackupRestoresAnIdenticalVult(t *testing.T) {
	lt := newLinkedTenant(t)
	f := lt.f
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	groceries := lt.account("kit", "6900", "expense", "", "Groceries")
	lt.account("kit", "3900", "equity", "opening_balance", "Opening balances")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.account("eightball", "3900", "equity", "opening_balance", "Opening balances")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-04", AmountMinor: 2200, Description: "EXAMPLE GROCER", Counterparty: "Example Grocer"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k5", Date: "2026-11-05", AmountMinor: 900, Description: "EXAMPLE CAFE", Counterparty: "Example Cafe"})
	lt.bank("kit", "cash", kitChecking, ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-13", AmountMinor: 4310, Description: "FROM EIGHTBALL", Counterparty: "Eightball Inc."})
	lt.bank("eightball", "cash", checking, ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-12", AmountMinor: -4310, Description: "TO KIT", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	post := func(path string, v any) []byte {
		t.Helper()
		body, _ := json.Marshal(v)
		w := bankCall(t, lt.s, f.session, http.MethodPost, path, string(body))
		if w.Code != http.StatusOK {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var cloud ledger.OwnerPairResult
	json.Unmarshal(post(pairsPath, ledger.OwnerPair{BankItemID: lt.item("kit", "EXAMPLE CLOUD").ID, BeneficiaryBookID: lt.books["eightball"].ID, BeneficiaryAccountID: software.ID, Description: "Cloud"}), &cloud)
	post(settlementsPath, ledger.OwnerSettlement{DebtorItemID: lt.item("eightball", "TO KIT").ID, CreditorItemID: lt.item("kit", "FROM EIGHTBALL").ID, Repays: ledger.RepayKeys(cloud.PairKey)})
	post(openingBalancePath, ledger.OwnerOpeningBalance{DebtorBookID: lt.books["eightball"].ID, CreditorBookID: lt.books["kit"].ID, AsOf: "2024-11-01", AmountMinor: "50000"})
	post(assignmentRulesPath, ledger.AssignmentRuleChange{BookID: lt.books["kit"].ID, CounterpartyKey: "Example Grocer", Target: "personal"})
	// Kit's agent records groceries, asks about a payee, and waits on a
	// pair half; the owner answers.
	lt.f.db.Call(t, lt.scope(lt.writers["kit"]), "record_bank_activity", ledger.RecordBankActivity{RequestKey: "grocer", TransactionID: lt.item("kit", "EXAMPLE GROCER").ID, Description: "Groceries", OtherAccountID: groceries.ID})
	var q ledger.AgentQuestion
	json.Unmarshal(lt.f.db.Call(t, lt.scope(lt.agents["kit"]), "ask_question", ledger.AskQuestion{SubjectKind: "other", SubjectKey: "cafe", Question: "Is the cafe a business cost?"}), &q)
	post(agentNotesPath+"/answer", map[string]any{"book_id": lt.books["kit"].ID, "question_id": q.ID, "answer": "No, personal."})
	if _, err := lt.prepare("kit", "kit-cafe", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-cafe", TransactionID: lt.item("kit", "EXAMPLE CAFE").ID,
		Description: "Cafe", OtherAccountID: kitFromEightball.ID, PairKey: "pair-cafe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lt.prepare("eightball", "eb-cafe", "post_journal", ledger.PostJournal{RequestKey: "eb-cafe", EconomicDate: "2026-11-05", AccountingDate: "2026-11-05", Description: "Cafe, paid by Kit",
		Lines:   []ledger.PostLine{{AccountID: software.ID, Side: "debit", AmountMinor: "900"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "900"}},
		PairKey: "pair-cafe", PairedTransactionID: lt.item("kit", "EXAMPLE CAFE").ID}); err != nil {
		t.Fatal(err)
	}
	r := ownerUploadRequest(f.session, lt.books["kit"].ID, "statement", "statement.pdf", "application/pdf", []byte("%PDF-1.7 example statement"))
	r.Header.Set("X-Vult-Document-Group", "tax")
	added := runOwnerUpload(lt.s, r)
	if added.Code != http.StatusCreated {
		t.Fatal("upload", added.Code, added.Body.String())
	}
	// Vult checked the file.
	uploaded := decodeOwnerUpload(t, added, http.StatusCreated).Document
	if err := f.sessions.OwnerAct(f.db.Context, f.session, f.tenant, func(o ledger.TenantAdmin, actor string) error {
		return o.WriteDocumentCheck(f.db.Context, lt.books["kit"].ID, actor, ledger.DocumentCheck{DocumentID: uploaded.ID, Check: ledger.CheckDone,
			Result: ledger.ResultReview, Reason: "Vult couldn't read the closing balance from this document.", Action: ledger.ActionConfirmReading})
	}); err != nil {
		t.Fatal(err)
	}

	// Vult's agent, turned on in one Book.
	if err := f.sessions.OwnerAct(f.db.Context, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		_, err := o.SaveAgentSettings(f.db.Context, lt.books["eightball"].ID, who, ledger.AgentSettingsChange{Enabled: true})
		return err
	}); err != nil {
		t.Fatal("turn the agent on", err)
	}
	// The studio is waiting to be erased: a backup from this week still
	// restores, and the studio comes back still waiting, with its date.
	tx, err := f.db.Admin.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, `INSERT INTO public.book_deletions(tenant_id,book_id,state,requested_at,confirm_after,erase_after,actor,via)
 VALUES($1,$2,'waiting','2026-11-10T10:00:00Z','2026-11-10T10:00:00Z','2026-11-17T10:00:00Z','Kit Blanche','browser')`, f.tenant, lt.books["studio"].ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.db.Context); err != nil {
		t.Fatal(err)
	}

	// A project in Eightball, with a rule and its software spending labelled.
	if err := f.sessions.OwnerAct(f.db.Context, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		p, err := o.CreateProject(f.db.Context, lt.books["eightball"].ID, who, ledger.ProjectChange{RequestKey: "side", Name: ptr("Example side project")})
		if err != nil {
			return err
		}
		if _, err = o.SetProjectRule(f.db.Context, lt.books["eightball"].ID, who, ledger.ProjectRule{ProjectID: p.ID, CounterpartyKey: "Example Cloud", StartsOn: "2026-01-01"}); err != nil {
			return err
		}
		var entry string
		if err := f.db.Admin.QueryRow(f.db.Context, `SELECT l.entry_id::text FROM public.lines l WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.account_id=$3 LIMIT 1`,
			f.tenant, lt.books["eightball"].ID, software.ID).Scan(&entry); err != nil {
			return err
		}
		_, err = o.LabelEntry(f.db.Context, lt.books["eightball"].ID, who, entry, []ledger.ProjectSplit{{ProjectID: p.ID}})
		return err
	}); err != nil {
		t.Fatal("project", err)
	}

	// A Book's own copy holds its bank logins and agent settings, but never
	// their sealed tokens or keys, nor the Vult's provider keys.
	var own bytes.Buffer
	if err := f.db.Owner.WriteArchive(f.db.Context, f.vault, &own, lt.books["kit"].ID); err != nil {
		t.Fatal(err)
	}
	header, err := tar.NewReader(bytes.NewReader(own.Bytes())).Next()
	if err != nil || header.Name != "manifest.json" {
		t.Fatal("book copy", err)
	}
	var manifest struct {
		Snapshot struct {
			Tables map[string][]map[string]any `json:"tables"`
		} `json:"snapshot"`
	}
	reader := tar.NewReader(bytes.NewReader(own.Bytes()))
	reader.Next()
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if checks := manifest.Snapshot.Tables["document_checks"]; len(checks) != 1 || checks[0]["action"] != ledger.ActionConfirmReading {
		t.Fatal("a Book's copy holds its document checks", checks)
	}
	logins := manifest.Snapshot.Tables["bank_connections"]
	if len(logins) == 0 || len(manifest.Snapshot.Tables["vult_bank_provider_settings"]) != 0 || len(manifest.Snapshot.Tables["bank_provider_settings"]) != 0 || len(manifest.Snapshot.Tables["vult_agent_settings"]) != 0 {
		t.Fatal("book copy tables", len(logins))
	}
	for _, l := range logins {
		if l["token_sealed"] != nil || l["key_version"] != nil {
			t.Fatal("a Book's copy carried a sealed token")
		}
	}
	// The owner's download of the Book holds the same rows.
	w := ownerAPICall(lt.s, f.session, http.MethodGet, bookExportPrefix+"book.tar?book_id="+lt.books["kit"].ID, "", false)
	if w.Code != http.StatusOK {
		t.Fatal("download", w.Code, w.Body.String())
	}
	var downloaded struct {
		Snapshot struct {
			Tables map[string][]map[string]any `json:"tables"`
		} `json:"snapshot"`
	}
	reader = tar.NewReader(bytes.NewReader(w.Body.Bytes()))
	if h, err := reader.Next(); err != nil || h.Name != "manifest.json" || json.NewDecoder(reader).Decode(&downloaded) != nil {
		t.Fatal("download manifest", err)
	}
	for table, rows := range manifest.Snapshot.Tables {
		if len(downloaded.Snapshot.Tables[table]) != len(rows) {
			t.Errorf("the owner's download holds %d rows of %s, the Book's archive %d", len(downloaded.Snapshot.Tables[table]), table, len(rows))
		}
	}
	var archive bytes.Buffer
	if err := f.db.Owner.WriteArchive(f.db.Context, f.vault, &archive, ""); err != nil {
		t.Fatal(err)
	}
	restore := func(data []byte) *testutil.Database {
		t.Helper()
		target := testutil.New(t)
		dir := filepath.Join(t.TempDir(), "restored")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		disk, err := documents.OpenDisk(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { disk.Close() })
		key, err := documents.NewKeyFile(f.tenant)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := documents.ParseKeyring(key)
		if err != nil {
			t.Fatal(err)
		}
		if err := target.Owner.RestoreArchive(target.Context, &documents.Vault{Storage: disk, Keys: keys}, bytes.NewReader(data), nil); err != nil {
			t.Fatal("restore", err)
		}
		return target
	}
	// Kit's own copy restores on its own: the bank login comes back
	// disconnected, to be reconnected.
	alone := restore(own.Bytes())
	var status, reason string
	if err := alone.Admin.QueryRow(alone.Context, "SELECT status,status_reason FROM public.bank_connections WHERE book_id=$1 LIMIT 1", lt.books["kit"].ID).Scan(&status, &reason); err != nil ||
		status != "disconnected" || !strings.Contains(reason, "Reconnect your bank") {
		t.Fatal("a Book's copy restored alone", status, reason, err)
	}
	// Eightball repaid a cost Kit paid for it. Its own copy restores without
	// Kit: it keeps its half of the pairs and the settlement, and the link to
	// a Book that isn't here is left out.
	var eightballCopy bytes.Buffer
	if err := f.db.Owner.WriteArchive(f.db.Context, f.vault, &eightballCopy, lt.books["eightball"].ID); err != nil {
		t.Fatal(err)
	}
	debtor := restore(eightballCopy.Bytes())
	var books, links, pairs, repays int
	if err := debtor.Admin.QueryRow(debtor.Context, `SELECT (SELECT count(*) FROM public.books),(SELECT count(*) FROM public.book_links),
 (SELECT count(*) FROM public.entry_pairs WHERE $1 IN (payer_book_id,beneficiary_book_id)),(SELECT count(*) FROM public.allocations WHERE source_book_id=$1 AND source='settlement' AND removed_at IS NULL)`,
		lt.books["eightball"].ID).Scan(&books, &links, &pairs, &repays); err != nil || books != 1 || links != 0 || pairs == 0 || repays != 1 {
		t.Fatal("a Book that repaid another's cost, restored alone", books, links, pairs, repays, err)
	}
	target := restore(archive.Bytes())
	// A v7 backup restores, whether it holds bank item moves and document
	// checks and readings or not: it's read by the tables it holds.
	for _, documentTables := range []bool{true, false} {
		older := asV7(t, archive.Bytes(), documentTables)
		v7 := restore(older)
		var books, checks int
		if err := v7.Admin.QueryRow(v7.Context, "SELECT (SELECT count(*) FROM public.books),(SELECT count(*) FROM public.document_checks)").Scan(&books, &checks); err != nil ||
			books != len(lt.books) || (checks > 0) != documentTables {
			t.Fatal("a v7 backup", documentTables, books, checks, err)
		}
		// Its settlement rows come back as allocations with the same
		// amounts as the current backup's.
		repaid := func(db *testutil.Database) string {
			var s string
			if err := db.Admin.QueryRow(db.Context, `SELECT coalesce(string_agg(obligation_entry_id::text||'='||amount_minor, ',' ORDER BY obligation_entry_id, amount_minor),'')
 FROM public.allocations WHERE source='settlement' AND removed_at IS NULL`).Scan(&s); err != nil {
				t.Fatal(err)
			}
			return s
		}
		if got, want := repaid(v7), repaid(target); got != want || want == "" {
			t.Fatal("a v7 backup's settlements", got, want)
		}
	}
	var state string
	var eraseAfter time.Time
	if err := target.Admin.QueryRow(target.Context, "SELECT d.state,d.erase_after FROM public.book_deletions d JOIN public.books b ON b.tenant_id=d.tenant_id AND b.id=d.book_id WHERE d.book_id=$1",
		lt.books["studio"].ID).Scan(&state, &eraseAfter); err != nil || state != "waiting" || !eraseAfter.Equal(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("the waiting Book after restore", state, eraseAfter, err)
	}

	// A deletion whose erase date passed before the restore is held for the
	// owner, never erased straight after the Book comes back.
	tx, err = f.db.Admin.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, "UPDATE public.book_deletions SET erase_after='2026-01-02T10:00:00Z' WHERE book_id=$1", lt.books["studio"].ID); err != nil {
		t.Fatal(err)
	}
	var expired bytes.Buffer
	if err := tx.Commit(f.db.Context); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Owner.WriteArchive(f.db.Context, f.vault, &expired, ""); err != nil {
		t.Fatal(err)
	}
	var held bool
	overdue := restore(expired.Bytes())
	if err := overdue.Admin.QueryRow(overdue.Context, "SELECT state='held' AND erase_after IS NULL AND confirm_after IS NOT NULL FROM public.book_deletions WHERE book_id=$1",
		lt.books["studio"].ID).Scan(&held); err != nil || !held {
		t.Fatal("an overdue deletion after restore", held, err)
	}
	tx, err = f.db.Admin.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, "UPDATE public.book_deletions SET erase_after='2026-11-17T10:00:00Z' WHERE book_id=$1", lt.books["studio"].ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.db.Context); err != nil {
		t.Fatal(err)
	}

	// Every public table but the rebuilt search index, row for row.
	var tables []string
	if err := f.db.Admin.QueryRow(f.db.Context, `SELECT array_agg(table_name::text ORDER BY table_name) FROM information_schema.tables
 WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name NOT IN ('schema_receipts','search_words','search_totals')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	rows := func(db *testutil.Database, table string) []string {
		projection := "to_jsonb(t)"
		switch table {
		case "documents":
			projection = "to_jsonb(t)-'storage_id'-'key_id'"
		case "document_uploads":
			projection = "to_jsonb(t)-'storage_id'"
		case "books":
			projection = "to_jsonb(t)-'write_epoch'"
		case "connections", "rule_recording_grants":
			// Restored, but revoked: agents connect again.
			projection = "to_jsonb(t)-'revoked_at'"
		case "agent_settings":
			projection = "to_jsonb(t)-'enabled'"
		}
		var out []string
		if err := db.Admin.QueryRow(db.Context, "SELECT coalesce(array_agg(("+projection+")::text),'{}') FROM public."+table+" t").Scan(&out); err != nil {
			t.Fatal(table, err)
		}
		sort.Strings(out)
		return out
	}
	empty := []string{}
	for _, table := range tables {
		source, restored := rows(f.db, table), rows(target, table)
		if strings.Join(source, "\n") != strings.Join(restored, "\n") {
			t.Errorf("%s differs after restore: %d rows before, %d after", table, len(source), len(restored))
		}
		if len(source) == 0 {
			empty = append(empty, table)
		}
	}
	var live int
	if err := target.Admin.QueryRow(target.Context, "SELECT count(*) FROM public.connections WHERE revoked_at IS NULL").Scan(&live); err != nil || live != 0 {
		t.Fatal("a restore brought back an agent's access", live, err)
	}
	// The scenario reaches the tables that matter here.
	for _, table := range []string{"projects", "project_labels", "project_rules", "bank_transaction_links", "book_links", "book_link_terms", "entry_pairs", "proposal_pairs", "allocations",
		"assignment_rules", "agent_questions", "document_groups", "connections", "bank_connections", "agent_settings"} {
		for _, e := range empty {
			if e == table {
				t.Errorf("the scenario left %s empty", table)
			}
		}
	}
}

// asV7 rewrites a backup as the earlier format: without the Vult's agent
// settings and the migrations after them, and with or without the bank item
// moves and document checks and readings.
func asV7(t *testing.T, data []byte, documentTables bool) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(data))
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	var digest string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		switch header.Name {
		case "manifest.json":
			var manifest map[string]json.RawMessage
			var snapshot map[string]json.RawMessage
			var tables map[string]json.RawMessage
			var migrations map[string]string
			if json.Unmarshal(body, &manifest) != nil || json.Unmarshal(manifest["snapshot"], &snapshot) != nil ||
				json.Unmarshal(snapshot["tables"], &tables) != nil || json.Unmarshal(manifest["migrations"], &migrations) != nil {
				t.Fatal("manifest")
			}
			delete(tables, "vult_agent_settings")
			// v7 kept what settlements repaid in settlement_repays, and
			// nothing for other payments.
			var allocations []map[string]any
			if json.Unmarshal(tables["allocations"], &allocations) != nil {
				t.Fatal("allocations")
			}
			repays := []map[string]any{}
			for _, a := range allocations {
				if a["source"] == "settlement" && a["removed_at"] == nil {
					repays = append(repays, map[string]any{"tenant_id": a["tenant_id"], "settlement_book_id": a["source_book_id"], "settlement_entry_id": a["source_entry_id"],
						"cost_book_id": a["obligation_book_id"], "cost_entry_id": a["obligation_entry_id"], "amount_minor": a["amount_minor"]})
				}
			}
			tables["settlement_repays"], _ = json.Marshal(repays)
			delete(tables, "allocations")
			for _, name := range ledger.ExpenseReportTables() {
				delete(tables, name)
			}
			for _, name := range []string{"projects", "project_labels", "project_rules", "project_promotions", "project_carried"} {
				delete(tables, name)
			}
			if !documentTables {
				for _, name := range []string{"bank_transaction_moves", "document_checks", "document_readings"} {
					delete(tables, name)
				}
			}
			snapshot["tables"], _ = json.Marshal(tables)
			for name := range migrations {
				if name >= "0021" {
					delete(migrations, name)
				}
			}
			snapshot["format"], _ = json.Marshal("vult-documents-backup-v7")
			manifest["format"], _ = json.Marshal("vult-archive-v7")
			manifest["snapshot"], _ = json.Marshal(snapshot)
			manifest["migrations"], _ = json.Marshal(migrations)
			body, _ = json.Marshal(manifest)
			sum := sha256.Sum256(body)
			digest = hex.EncodeToString(sum[:])
		case "complete.json":
			body, _ = json.Marshal(map[string]string{"format": "vult-archive-complete-v1", "manifest_sha256": digest})
		}
		header.Size = int64(len(body))
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func ptr[T any](v T) *T { return &v }
