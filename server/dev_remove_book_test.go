//go:build devserver && browserpreview

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
)

// TestDevRemoveBook removes whole Books from a development Vult: their
// records, documents, links, bank connections and agent state, in one
// transaction. It is a development tool, not a product feature: the
// ledger's own records can't be removed, so it sets their guards aside for
// that one transaction and checks every reference before it commits.
//
// VULT_DEV_REMOVE_BOOKS names the Books, separated by "|", exactly as they
// are named now. Without VULT_DEV_REMOVE_CONFIRM set to their ids (in the
// same order, separated by "|"), it only says what it would remove.
func TestDevRemoveBook(t *testing.T) {
	ctx := context.Background()
	names := strings.Split(os.Getenv("VULT_DEV_REMOVE_BOOKS"), "|")
	if len(names) == 0 || names[0] == "" {
		t.Fatal("VULT_DEV_REMOVE_BOOKS names the Books to remove")
	}
	e := openDevEnv(t, false)
	db := e.pool(t, "", 2)

	var tenant string
	err := db.QueryRow(ctx, `SELECT o.tenant_id::text FROM access.owners o JOIN access.identities i ON i.id=o.identity_id
 JOIN access.passkey_profiles p ON p.identity_id=i.id
 WHERE i.issuer=$1 AND i.subject=$2 AND p.origin=$3 AND p.state='ready' LIMIT 1`, access.BuiltinIssuer, devOwnerSubject, e.origin).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("this development Vult has no owner yet")
	}
	if err != nil {
		t.Fatal(err)
	}
	// Each name must be exactly one of the owner's Books, by its current name.
	var ids []string
	for _, name := range names {
		rows, err := db.Query(ctx, `SELECT r.book_id::text FROM public.book_revisions r
 WHERE r.tenant_id=$1 AND r.revision=(SELECT max(x.revision) FROM public.book_revisions x WHERE x.tenant_id=r.tenant_id AND x.book_id=r.book_id) AND r.name=$2`, tenant, name)
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			found = append(found, id)
		}
		rows.Close()
		if len(found) != 1 {
			t.Fatalf("%q names %d Books here, not one; nothing was removed", name, len(found))
		}
		ids = append(ids, found[0])
	}
	// A link or pair with a Book that stays would leave it half a record.
	var outside int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM public.book_links WHERE tenant_id=$1 AND ((book_a=ANY($2::uuid[])) <> (book_b=ANY($2::uuid[])))`, tenant, ids).Scan(&outside); err != nil {
		t.Fatal(err)
	}
	if outside > 0 {
		t.Fatal("these Books are linked to a Book that stays; remove the link first, or name both")
	}

	// Every table that holds a Book's rows, by a book column.
	type target struct{ table, column string }
	var targets []target
	rows, err := db.Query(ctx, `SELECT format('%I.%I', c.table_schema, c.table_name), c.column_name FROM information_schema.columns c
 JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name AND t.table_type='BASE TABLE'
 WHERE c.table_schema IN ('public','access') AND (c.column_name='book_id' OR c.column_name LIKE '%\_book\_id' OR (c.table_name='book_links' AND c.column_name IN ('book_a','book_b')))
 ORDER BY 1,2`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var x target
		if err := rows.Scan(&x.table, &x.column); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, x)
	}
	rows.Close()

	counts := map[string]int64{}
	for _, x := range targets {
		var n int64
		if err := db.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = ANY($1::uuid[])", x.table, pgx.Identifier{x.column}.Sanitize()), ids).Scan(&n); err != nil {
			t.Fatalf("%s: %v", x.table, err)
		}
		if n > 0 {
			counts[x.table+"."+x.column] = n
		}
	}
	var stored []string
	rows, err = db.Query(ctx, `SELECT storage_id FROM public.documents WHERE book_id=ANY($1::uuid[]) AND storage_id IS NOT NULL AND storage_id<>''`, ids)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, s)
	}
	rows.Close()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("rows %s %d\n", k, counts[k])
	}
	fmt.Printf("document files %d\n", len(stored))
	if os.Getenv("VULT_DEV_REMOVE_CONFIRM") != strings.Join(ids, "|") {
		fmt.Printf("WOULD REMOVE %s (%s)\n", strings.Join(names, ", "), strings.Join(ids, "|"))
		return
	}

	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// The ledger's records are append-only; their guards step aside for this
	// transaction alone, and every reference is checked before it commits.
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	for _, x := range targets {
		if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s = ANY($1::uuid[])", x.table, pgx.Identifier{x.column}.Sanitize()), ids); err != nil {
			t.Fatalf("%s: %v", x.table, err)
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM public.books WHERE id = ANY($1::uuid[])", ids); err != nil {
		t.Fatal(err)
	}
	// Nothing may point at a row that's gone: every foreign key, checked.
	fks, err := tx.Query(ctx, `SELECT format('%I.%I', cn.nspname, c.relname), format('%I.%I', pn.nspname, p.relname),
 (SELECT array_agg(quote_ident(a.attname) ORDER BY k.n) FROM unnest(f.conkey) WITH ORDINALITY k(att,n) JOIN pg_attribute a ON a.attrelid=f.conrelid AND a.attnum=k.att),
 (SELECT array_agg(quote_ident(a.attname) ORDER BY k.n) FROM unnest(f.confkey) WITH ORDINALITY k(att,n) JOIN pg_attribute a ON a.attrelid=f.confrelid AND a.attnum=k.att)
 FROM pg_constraint f JOIN pg_class c ON c.oid=f.conrelid JOIN pg_namespace cn ON cn.oid=c.relnamespace
 JOIN pg_class p ON p.oid=f.confrelid JOIN pg_namespace pn ON pn.oid=p.relnamespace
 WHERE f.contype='f' AND cn.nspname IN ('public','access')`)
	if err != nil {
		t.Fatal(err)
	}
	type fk struct {
		child, parent string
		from, to      []string
	}
	var checks []fk
	for fks.Next() {
		var x fk
		if err := fks.Scan(&x.child, &x.parent, &x.from, &x.to); err != nil {
			t.Fatal(err)
		}
		checks = append(checks, x)
	}
	fks.Close()
	for _, x := range checks {
		var notNull, match []string
		for i := range x.from {
			notNull = append(notNull, "c."+x.from[i]+" IS NOT NULL")
			match = append(match, "p."+x.to[i]+" = c."+x.from[i])
		}
		var orphan bool
		q := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s c WHERE %s AND NOT EXISTS(SELECT 1 FROM %s p WHERE %s))", x.child, strings.Join(notNull, " AND "), x.parent, strings.Join(match, " AND "))
		if err := tx.QueryRow(ctx, q).Scan(&orphan); err != nil {
			t.Fatalf("checking %s: %v", x.child, err)
		}
		if orphan {
			t.Fatalf("%s would point at a removed row in %s; nothing was removed", x.child, x.parent)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(filepath.Join(e.dir, "documents"))
	if err != nil {
		t.Fatal(err)
	}
	gone := 0
	for _, s := range stored {
		if err := disk.Remove(ctx, s); err == nil {
			gone++
		}
	}
	fmt.Printf("REMOVED %s; %d document files deleted\n", strings.Join(names, ", "), gone)
}
