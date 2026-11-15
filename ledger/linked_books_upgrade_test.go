package ledger_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// A database that recorded the earlier single business–personal link and
// its pairs keeps them through the upgrade to linked Books: the personal
// Book pays for the business Book, by the same mode and accounts.
func TestLinkedBooksUpgradeCarriesEarlierLinksAndPairs(t *testing.T) {
	tenant, business, personal := ledger.ID(), ledger.ID(), ledger.ID()
	equity, investment, actor := ledger.ID(), ledger.ID(), ledger.ID()
	businessEntry, personalEntry := ledger.ID(), ledger.ID()
	db := testutil.NewWithInitializer(t, func(ctx context.Context, pool *pgxpool.Pool, role string) error {
		if _, err := pool.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp()); REVOKE ALL ON public.schema_receipts FROM PUBLIC"); err != nil {
			return err
		}
		// 0.7.0's migrations, then 0.8's steps through the earlier link, as
		// a release candidate applied them.
		var names, files []string
		for _, dir := range []string{"migrations", "migrations/bridge"} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".sql") && e.Name() <= "0012_owner_paid.sql" && (dir != "migrations" || e.Name() < "0008") {
					names, files = append(names, e.Name()), append(files, dir+"/"+e.Name())
				}
			}
		}
		sort.Strings(names)
		sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
		if names[len(names)-1] != "0012_owner_paid.sql" {
			t.Fatal("the earlier link migration is missing", names)
		}
		for i, name := range names {
			data, err := os.ReadFile(files[i])
			if err != nil {
				return err
			}
			if _, err = pool.Exec(ctx, string(data)); err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			if _, err = pool.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
				return err
			}
		}
		// What the earlier release recorded, written as it stored it.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"SET LOCAL session_replication_role = replica", nil},
			{"INSERT INTO public.tenants(id) VALUES($1)", []any{tenant}},
			{"INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example Studio','USD',2),($1,$3,'Example Person','USD',2)", []any{tenant, business, personal}},
			{"INSERT INTO public.accounts(tenant_id,book_id,id,class,code,name) VALUES($1,$2,$3,'equity','3100','Owner contributions'),($1,$4,$5,'asset','1500','Investment in the studio')",
				[]any{tenant, business, equity, personal, investment}},
			{`INSERT INTO public.book_links(tenant_id,business_book_id,personal_book_id,mode,business_account_id,personal_account_id,active,revision,actor_id)
 VALUES($1,$2,$3,'contribution',$4,$5,true,3,$6)`, []any{tenant, business, personal, equity, investment, actor}},
			{`INSERT INTO public.entry_pairs(tenant_id,pair_key,business_book_id,business_entry_id,personal_book_id,personal_entry_id)
 VALUES($1,'pair-example',$2,$3,$4,$5)`, []any{tenant, business, businessEntry, personal, personalEntry}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
		t.Fatal(err)
	}
	var a, b, payer, beneficiary, treatment, payerAccount, beneficiaryAccount string
	var active bool
	if err := db.Admin.QueryRow(db.Context, `SELECT l.book_a::text,l.book_b::text,l.active,x.payer_book_id::text,x.beneficiary_book_id::text,x.treatment,x.payer_account_id::text,x.beneficiary_account_id::text
 FROM public.book_links l JOIN public.book_link_terms x ON x.tenant_id=l.tenant_id AND x.link_id=l.id WHERE l.tenant_id=$1`, tenant).Scan(
		&a, &b, &active, &payer, &beneficiary, &treatment, &payerAccount, &beneficiaryAccount); err != nil {
		t.Fatal("the link wasn't carried over", err)
	}
	if !active || payer != personal || beneficiary != business || treatment != "contribution" || payerAccount != investment || beneficiaryAccount != equity ||
		!(a < b) || a != min(business, personal) || b != max(business, personal) {
		t.Fatal("carried link", a, b, active, payer, beneficiary, treatment)
	}
	var kind, payerEntry, beneficiaryEntry string
	if err := db.Admin.QueryRow(db.Context, "SELECT kind,payer_entry_id::text,beneficiary_entry_id::text FROM public.entry_pairs WHERE tenant_id=$1 AND pair_key='pair-example'", tenant).Scan(
		&kind, &payerEntry, &beneficiaryEntry); err != nil || kind != "cost" || payerEntry != personalEntry || beneficiaryEntry != businessEntry {
		t.Fatal("carried pair", kind, payerEntry, beneficiaryEntry, err)
	}
	// The new tables keep their guards and forced row security.
	var unforced, disabled int
	if err := db.Admin.QueryRow(db.Context, `SELECT
 (SELECT count(*) FROM pg_class WHERE relname IN ('book_links','book_link_terms','proposal_pairs','entry_pairs') AND relnamespace='public'::regnamespace AND NOT relforcerowsecurity),
 (SELECT count(*) FROM pg_trigger WHERE tgrelid IN ('public.book_links'::regclass,'public.book_link_terms'::regclass,'public.entry_pairs'::regclass) AND NOT tgisinternal AND tgenabled='D')`).Scan(&unforced, &disabled); err != nil || unforced != 0 || disabled != 0 {
		t.Fatal("guards after the upgrade", unforced, disabled, err)
	}
}
