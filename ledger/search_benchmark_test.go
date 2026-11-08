package ledger_test

import (
	"testing"
	"time"

	"github.com/vult-ai/vult/ledger"
)

// BenchmarkBookSearch measures reads over a large synthetic history.
func BenchmarkBookSearch(t *testing.B) {
	b := newSearchBook(t)
	ctx, admin := b.db.Context, b.db.Admin
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	must := func(sql string, args ...any) {
		start := time.Now()
		defer func() { t.Logf("load step %q took %v", sql[:30], time.Since(start)) }()
		if _, e := tx.Exec(ctx, sql, args...); e != nil {
			t.Fatal(sql[:40], e)
		}
	}
	must("SET LOCAL session_replication_role=replica")
	must(`INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 SELECT $1,$2,gen_random_uuid(),$3,'bulk-'||g,'post_journal',convert_to('{"request_key":"bulk-'||g||'"}','UTF8'),'\x7b7d'::bytea FROM generate_series(1,70000) g`, b.book.TenantID, b.book.ID, b.scope.ActorID)
	must(`WITH r AS (SELECT id, row_number() OVER (ORDER BY request_key) n FROM public.requests WHERE book_id=$2 AND request_key LIKE 'bulk-%')
 INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description)
 SELECT $1,$2,gen_random_uuid(),1000+n,r.id,$3,d,d,
  (ARRAY['Amazon','Uber Eats','Uber Trip','GODADDY.COM','Google','Google One','Stripe payout','WISE US INC WISE transfer','ADP wage pay','Spectrum internet','Delta Air','Jet Blue','Office Depot','Shell gas','Apple.com bill'])[1+(n%15)]||' '||(n%997)
 FROM r, LATERAL (SELECT DATE '2006-01-01' + (n*7919 % 7600)::int AS d) dd`, b.book.TenantID, b.book.ID, b.scope.ActorID)
	must(`INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,memo)
 SELECT tenant_id,book_id,gen_random_uuid(),id,1,$2::uuid,'debit',100+(sequence*37%250000),'Synthetic memo '||(sequence%113) FROM public.entries WHERE book_id=$1 AND sequence>=1000
 UNION ALL SELECT tenant_id,book_id,gen_random_uuid(),id,2,$3::uuid,'credit',100+(sequence*37%250000),'' FROM public.entries WHERE book_id=$1 AND sequence>=1000`,
		b.book.ID, b.account["6000"].ID, b.account["2000"].ID)
	must(`INSERT INTO public.search_words(tenant_id,book_id,entry_id,word)
 SELECT e.tenant_id,e.book_id,e.id,w FROM public.entries e, unnest(public.search_tokens(e.description)) w WHERE e.book_id=$1
 UNION SELECT l.tenant_id,l.book_id,l.entry_id,w FROM public.lines l, unnest(public.search_tokens(l.memo)) w WHERE l.book_id=$1`, b.book.ID)
	must(`INSERT INTO public.search_totals(tenant_id,book_id,entry_id,debit_minor) SELECT tenant_id,book_id,entry_id,sum(amount_minor) FROM public.lines WHERE book_id=$1 AND side='debit' GROUP BY 1,2,3`, b.book.ID)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, "ANALYZE"); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{"amazon", "uber eats", "google one", "goog", "amazn", "zzqq", "xyzzyplugh", "office expense", "2020", "Oct 24", "october", "1502.55", "500", `"wise transfer"`, "synthetic memo 42", "a", "uber eats oct"} {
		t.Run(q, func(t *testing.B) {
			var searchTime, listTime time.Duration
			t.ReportAllocs()
			for i := 0; i < t.N; i++ {
				start := time.Now()
				r, e := b.owner.SearchBook(ctx, b.book.ID, ledger.SearchQuery{Query: q})
				searchTime += time.Since(start)
				start = time.Now()
				page, e2 := b.owner.Activity(ctx, b.book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{Search: q}, Limit: 25})
				ids := page.Coverage.Count
				listTime += time.Since(start)
				if e != nil || e2 != nil || ids != r.Transactions.Total {
					t.Fatal(q, e, e2)
				}
			}
			t.ReportMetric(float64(searchTime.Nanoseconds())/float64(t.N), "search-ns/op")
			t.ReportMetric(float64(listTime.Nanoseconds())/float64(t.N), "list-ns/op")
		})
	}
}
