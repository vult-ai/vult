package access_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerJournalReadsRecheckCompletionAuthorization(t *testing.T) {
	for _, documents := range []bool{false, true} {
		name, prefix := "entry", "WITH observed AS MATERIALIZED (SELECT clock_timestamp() captured_at)"
		if documents {
			name, prefix = "documents", "SELECT EXISTS(SELECT 1 FROM public.entries"
		}
		t.Run(name, func(t *testing.T) {
			f := fresh(t)
			tenant, session, _ := f.owner(t, "example")
			book := f.book(t, tenant, session)
			var c ledger.Connection
			if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
				var err error
				c, err = owner.CreateConnection(f.db.Context, book.ID, "Example writer", "write")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
			a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
			var entry ledger.Entry
			q := ledger.PostJournal{RequestKey: "example", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example journal", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "100"}, {AccountID: b.ID, Side: "credit", AmountMinor: "100"}}}
			if json.Unmarshal(f.db.Call(t, scope, "post_journal", q), &entry) != nil {
				t.Fatal("invalid fixture")
			}
			var journal ledger.JournalRead
			var links ledger.JournalDocumentsRead
			read := func(owner ledger.TenantAdmin) error {
				var err error
				if documents {
					links, err = owner.JournalDocuments(f.db.Context, book.ID, ledger.EntryDocumentsQuery{EntryID: entry.ID})
				} else {
					journal, err = owner.Journal(f.db.Context, book.ID, entry.ID)
				}
				return err
			}
			if err := f.service.WithOwner(f.db.Context, session, tenant, false, read); err != nil {
				t.Fatal("live read control failed", err)
			}
			trace := &expireOwnerRead{queryPrefix: prefix}
			if err := f.db.Admin.QueryRow(f.db.Context, "UPDATE access.sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1 RETURNING expires_at", access.Hash(session)).Scan(&trace.expires); err != nil {
				t.Fatal(err)
			}
			config := f.owners.Config()
			config.ConnConfig.Tracer = trace
			owners, err := pgxpool.NewWithConfig(f.db.Context, config)
			if err != nil {
				t.Fatal(err)
			}
			defer owners.Close()
			service, err := access.New(f.auth, owners, "https://ledger.example.com")
			if err != nil {
				t.Fatal(err)
			}
			err = service.WithOwner(f.db.Context, session, tenant, false, read)
			if !errors.Is(err, ledger.ErrDenied) || trace.reads.Load() != 1 || documents && links.Documents != nil || !documents && journal.Entry.ID != "" {
				t.Fatal("inner journal read returned partial facts after expiry", err, trace.reads.Load())
			}
		})
	}
}

func TestOwnerJournalReadDeniesHiddenNativeDefinition(t *testing.T) {
	f := fresh(t)
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	var c ledger.Connection
	if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	zero := 0
	var cash ledger.Account
	if json.Unmarshal(f.db.Call(t, scope, "create_account", ledger.CreateAccount{RequestKey: "native", Class: "asset", Name: "Example native account", Currency: "JPY", Scale: &zero, NativeOrigin: "empty_ledger"}), &cash) != nil {
		t.Fatal("invalid account fixture")
	}
	equity := f.db.Account(t, scope, "3000", "equity")
	var entry ledger.Entry
	q := ledger.PostJournal{RequestKey: "example", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example manual valuation", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "100", Native: &ledger.NativeMovement{Currency: "JPY", Scale: &zero, AmountMinor: "100"}}, {AccountID: equity.ID, Side: "credit", AmountMinor: "100"}}}
	if json.Unmarshal(f.db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("invalid entry fixture")
	}
	owner, err := f.db.Owner.ForTenant(f.db.Context, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if control, err := owner.Journal(f.db.Context, book.ID, entry.ID); err != nil || control.Entry.Lines[0].Native == nil {
		t.Fatal("valid native control failed", err)
	}
	role := f.owners.Config().ConnConfig.User
	if _, err = f.db.Admin.Exec(f.db.Context, "CREATE POLICY hidden_native_definition ON public.account_revisions AS RESTRICTIVE FOR SELECT TO "+pgx.Identifier{role}.Sanitize()+" USING(currency IS NULL)"); err != nil {
		t.Fatal(err)
	}
	tx, err := f.owners.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.db.Context)
	if _, err = tx.Exec(f.db.Context, "SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),set_config('vult.tenant_id',$3,true),set_config('vult.book_id',$4,true)", access.Hash(session), "https://ledger.example.com", tenant, book.ID); err != nil {
		t.Fatal(err)
	}
	var native string
	var definition *string
	if err = tx.QueryRow(f.db.Context, `SELECT l.native_currency,r.currency_definition FROM public.lines l
 LEFT JOIN public.account_revisions r ON r.tenant_id=l.tenant_id AND r.book_id=l.book_id AND r.account_id=l.account_id AND r.revision=l.native_account_revision
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=$3 AND l.account_id=$4`, tenant, book.ID, entry.ID, cash.ID).Scan(&native, &definition); err != nil || native != "JPY" || definition != nil {
		t.Fatal("fixture did not hide only the joined native definition", err)
	}
	if err = tx.Rollback(f.db.Context); err != nil {
		t.Fatal(err)
	}
	var result ledger.JournalRead
	err = f.service.WithOwner(f.db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		result, err = owner.Journal(f.db.Context, book.ID, entry.ID)
		return err
	})
	if !errors.Is(err, ledger.ErrDenied) || result.Entry.ID != "" {
		t.Fatal("hidden native definition was returned or panicked", err)
	}
	if control, err := owner.Journal(f.db.Context, book.ID, entry.ID); err != nil || control.Entry.Lines[0].Native == nil {
		t.Fatal("negative visibility control changed the actual native record", err)
	}
}

func TestOwnerDocumentRelationshipReadsStayTenantScoped(t *testing.T) {
	f := fresh(t)
	type sample struct {
		tenant, session string
		book            ledger.Book
		entry           ledger.Entry
	}
	samples := []sample{}
	for _, subject := range []string{"first", "second"} {
		tenant, session, _ := f.owner(t, subject)
		book := f.book(t, tenant, session)
		var c ledger.Connection
		if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
			var err error
			c, err = owner.CreateConnection(f.db.Context, book.ID, "Example writer", "write")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
		path := t.TempDir()
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
		disk, err := documents.OpenDisk(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { disk.Close() })
		key, err := documents.NewKeyFile(tenant)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := documents.ParseKeyring(key)
		if err != nil {
			t.Fatal(err)
		}
		store := f.db.Store
		store.Documents = &documents.Vault{Storage: disk, Keys: keys}
		data := []byte("date,amount\n2026-01-01,100\n")
		sum := sha256.Sum256(data)
		var intake ledger.DocumentIntake
		prepared, err := store.Call(f.db.Context, scope, "add_document", mustJSON(t, ledger.AddDocument{RequestKey: "original", Name: "example.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}))
		if err != nil || json.Unmarshal(prepared, &intake) != nil {
			t.Fatal("original preparation failed", err)
		}
		if _, err = store.Upload(f.db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
		var entry ledger.Entry
		q := ledger.PostJournal{RequestKey: "example", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example cited journal", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "100"}, {AccountID: b.ID, Side: "credit", AmountMinor: "100"}}, Sources: []ledger.SourceInput{{DocumentID: intake.Document.ID}}}
		if json.Unmarshal(f.db.Call(t, scope, "post_journal", q), &entry) != nil {
			t.Fatal("invalid fixture")
		}
		f.db.Call(t, scope, "attach_evidence", ledger.AttachEvidence{RequestKey: "evidence", EntryID: entry.ID, DocumentID: intake.Document.ID, Role: "statement", Reason: "Example statement evidence"})
		samples = append(samples, sample{tenant, session, book, entry})
	}
	for _, table := range []string{"source_citations", "evidence_links"} {
		var read, insert, update, remove, authRead bool
		if err := f.db.Admin.QueryRow(f.db.Context, "SELECT has_table_privilege($1,$3,'SELECT'),has_table_privilege($1,$3,'INSERT'),has_table_privilege($1,$3,'UPDATE'),has_table_privilege($1,$3,'DELETE'),has_table_privilege($2,$3,'SELECT')", f.owners.Config().ConnConfig.User, f.auth.Config().ConnConfig.User, "public."+table).Scan(&read, &insert, &update, &remove, &authRead); err != nil || !read || insert != (table == "source_citations") || update || remove || authRead {
			t.Fatal("relationship read widened other privileges", table, err)
		}
	}
	for i, selected := range samples {
		err := f.service.WithOwner(f.db.Context, selected.session, selected.tenant, false, func(owner ledger.TenantAdmin) error {
			got, err := owner.JournalDocuments(f.db.Context, selected.book.ID, ledger.EntryDocumentsQuery{EntryID: selected.entry.ID})
			var facts struct {
				Sources  []ledger.SourceCitation `json:"sources"`
				Evidence []ledger.EvidenceLink   `json:"evidence"`
			}
			if err != nil || json.Unmarshal(got.Documents, &facts) != nil || len(facts.Sources) != 1 || len(facts.Evidence) != 1 || facts.Evidence[0].Role != "statement" {
				t.Fatal("live owner could not read exact relationship facts", err)
			}
			foreign := samples[1-i]
			if got, err := owner.JournalDocuments(f.db.Context, selected.book.ID, ledger.EntryDocumentsQuery{EntryID: foreign.entry.ID}); !errors.Is(err, ledger.ErrNotFound) || got.Documents != nil {
				t.Fatal("foreign relationship escaped Book scope", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.owners.Begin(f.db.Context)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(f.db.Context, "SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),set_config('vult.tenant_id',$3,true)", access.Hash(selected.session), "https://ledger.example.com", selected.tenant); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"source_citations", "evidence_links"} {
			var rows int
			if err = tx.QueryRow(f.db.Context, "SELECT count(*)FROM public."+pgx.Identifier{table}.Sanitize()).Scan(&rows); err != nil || rows != 1 {
				t.Fatal("unfiltered relationship read crossed tenant policy", table, rows, err)
			}
		}
		if err = tx.Rollback(f.db.Context); err != nil {
			t.Fatal(err)
		}
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
