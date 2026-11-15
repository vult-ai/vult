package ledger

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
	"sort"
	"strings"
)

//go:embed migrations/*.sql migrations/bridge/*.sql
var migrations embed.FS

// squashedMigration is 0.8 in one file, for a new Vult. An existing Vult
// reaches the same schema through the steps in migrations/bridge, as they
// were released, and then records the squashed migration as applied; it
// never runs it on top of them.
//
// The bridge can go once a release requires 0.8 first: every Vult then
// records the squashed migration, and migrations holds just 0001 to 0007
// and it. Vults that came through the bridge keep the steps' receipts, so
// that release must still accept them beside the squashed one.
const squashedMigration = "0008_bank_activity_agents_and_linked_books.sql"

// An earlier build of 0.8 recorded 0.8 as one file, 0008_v08.sql: run on a
// new Vult, or recorded without running once the steps through
// 0032_carried_costs.sql were in. Its schema is exactly those steps', so its
// receipt, with this checksum, stands for them.
const (
	earlierSquash        = "0008_v08.sql"
	earlierSquashSum     = "4f422fb916fd12abfec1c133d22f0b5c91b2fdf6b0622bad5a1d12bc0c7ccd27"
	earlierSquashThrough = "0032_carried_costs.sql"
)

// withEarlierSquash reads that receipt as the steps it stands for. Other
// receipts are returned as they are; a receipt with another checksum stays,
// so it doesn't match.
func withEarlierSquash(got map[string]string) map[string]string {
	if got[earlierSquash] != earlierSquashSum {
		return got
	}
	out := make(map[string]string, len(got))
	for name, sum := range got {
		if name != earlierSquash {
			out[name] = sum
		}
	}
	for _, name := range bridgeNames() {
		if name > earlierSquashThrough {
			break
		}
		if _, ok := out[name]; !ok {
			out[name] = bridgeSum(name)
		}
	}
	return out
}

// bridgeNames are the steps 0.8's release candidates applied one by one.
func bridgeNames() []string {
	entries, e := migrations.ReadDir("migrations/bridge")
	if e != nil {
		panic(e)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func bridgeSum(name string) string {
	b, _ := migrations.ReadFile("migrations/bridge/" + name)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const recordedBookMigration = "0004_recorded_book.sql"
const foundationsMigration = "0007_accounting_foundations.sql"

func SchemaSHA256() string {
	b, e := migrations.ReadFile("migrations/0001_ledger.sql")
	if e != nil {
		panic(e)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func migrationNames() []string {
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		panic(e)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func migrationSum(name string) string {
	b, _ := migrations.ReadFile("migrations/" + name)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Apply verifies installed receipts and applies only missing forward migrations.
func Apply(ctx context.Context, admin *pgxpool.Pool, runtimeRole string) error {
	_, err := apply(ctx, admin, runtimeRole, false)
	return err
}

// Upgrade applies forward migrations only to a recognized, initialized ledger.
// Missing or invalid schema receipts require recovery instead of initialization.
func Upgrade(ctx context.Context, admin *pgxpool.Pool, runtimeRole string) error {
	_, err := apply(ctx, admin, runtimeRole, true)
	return err
}

// Migrate is the maintenance step: it initializes an empty database when
// initialize is true, or applies the missing forward migrations to an
// initialized one, all in one transaction. When it changes an existing
// schema it compares the fact fingerprint before and after inside that
// transaction and rolls everything back if they differ.
func Migrate(ctx context.Context, admin *pgxpool.Pool, runtimeRole string, initialize bool) (MigrationResult, error) {
	return apply(ctx, admin, runtimeRole, !initialize)
}

// beforeFactCheck lets tests stand in for a migration that changes facts.
var beforeFactCheck func(context.Context, pgx.Tx) error

func apply(ctx context.Context, admin *pgxpool.Pool, runtimeRole string, initialized bool) (result MigrationResult, err error) {
	if runtimeRole == "" || strings.ContainsAny(runtimeRole, "\x00") {
		return result, ErrInvalid
	}
	tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return result, e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(769305013)"); e != nil {
		return result, e
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('public.schema_receipts') IS NOT NULL").Scan(&exists); e != nil {
		return result, e
	}
	names := migrationNames()
	applied := 0
	var bridgeLeft []string
	if exists {
		if applied, bridgeLeft, e = readReceipts(ctx, tx, names, false); e != nil {
			return result, e
		}
		if applied < len(names) {
			if result.Fingerprint, e = Fingerprint(ctx, tx); e != nil {
				return result, e
			}
		}
	} else {
		if initialized {
			return result, errors.New("stored schema is missing; recovery is required")
		}
		var occupied bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public')").Scan(&occupied); e != nil || occupied {
			return result, errors.New("a fresh database is required")
		}
		if _, e = tx.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); e != nil {
			return result, e
		}
		if _, e = tx.Exec(ctx, "REVOKE ALL ON public.schema_receipts FROM PUBLIC"); e != nil {
			return result, e
		}
		result.Initialized = true
	}
	result.From, result.To = applied, len(names)
	run := func(file, name, sum string) error {
		b, _ := migrations.ReadFile(file)
		if _, e := tx.Exec(ctx, string(b)); e != nil {
			var p *pgconn.PgError
			if errors.As(e, &p) && p.Code == "23514" {
				return &UpgradeRefusal{Message: p.Message, Hint: p.Hint}
			}
			return e
		}
		_, e := tx.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, sum)
		return e
	}
	for _, name := range names[applied:] {
		// An existing Vult takes 0.8's steps as they were released, then
		// records the squashed migration; only a new Vult runs it.
		if name == squashedMigration && exists {
			if bridgeLeft == nil && applied <= slices.Index(names, squashedMigration) {
				bridgeLeft = bridgeNames()
			}
			for _, step := range bridgeLeft {
				if e = run("migrations/bridge/"+step, step, bridgeSum(step)); e != nil {
					return result, e
				}
			}
			if _, e = tx.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, migrationSum(name)); e != nil {
				return result, e
			}
			continue
		}
		if e = run("migrations/"+name, name, migrationSum(name)); e != nil {
			return result, e
		}
	}
	role := pgx.Identifier{runtimeRole}.Sanitize()
	statements := []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC", "GRANT USAGE ON SCHEMA public TO " + role,
		"GRANT SELECT ON public.books,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods,public.connections,public.schema_receipts,public.book_revisions TO " + role,
		"GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + role,
		"GRANT UPDATE(write_epoch) ON public.books TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_set_book_logo(uuid,uuid,text,text,text,bytea) TO " + role,
		"GRANT UPDATE(id) ON public.connections TO " + role,
		"GRANT SELECT,INSERT ON public.documents,public.document_uploads TO " + role,
		"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at) ON public.documents TO " + role,
		"GRANT UPDATE(used_at) ON public.documents TO " + role,
		"GRANT SELECT,INSERT ON public.source_citations TO " + role,
		"GRANT SELECT,INSERT ON public.evidence_links TO " + role,
		"GRANT SELECT ON public.search_words,public.search_totals TO " + role,
		"GRANT EXECUTE ON FUNCTION public.search_text(text),public.search_tokens(text) TO " + role,
		"GRANT SELECT,INSERT ON public.operation_proposals,public.operation_consumptions TO " + role,
		"GRANT SELECT ON public.operation_decisions TO " + role,
		"GRANT EXECUTE ON FUNCTION access.operation_owner_allowed(uuid,uuid,uuid,bigint),access.operation_executor_active(uuid,uuid,uuid,uuid),access.hold_operation_owner(uuid,uuid,uuid,bigint) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.operation_dependencies(uuid,uuid,text,jsonb,uuid),public.operation_dependencies_settled(jsonb) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_posting_consumer(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_validate_balances(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_native_position(uuid,uuid,uuid,text,bigint,bigint) TO " + role,
		"GRANT USAGE ON SCHEMA access TO " + role,
		// Accounting foundations. Row security applies each connection's
		// level; the period guard keeps reopening and closing with open
		// review items for the owner.
		"GRANT SELECT,INSERT ON public.payees,public.payee_revisions,public.entry_payees,public.statements,public.statement_rows,public.statement_duplicates,public.statement_matches,public.statement_match_parts,public.statement_match_revocations,public.statement_adjustments,public.statement_reviews,public.accounting_periods,public.accounting_period_events TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_restoring(uuid,uuid),public.ledger_role_fits(text,text),public.ledger_period_closed(uuid,uuid,date),public.statement_line_amount(uuid,uuid,uuid,text,integer),public.statement_row_check(uuid,uuid,uuid),public.statement_line_check(uuid,uuid,uuid,text,integer),public.ledger_owner_decision(uuid,uuid),public.period_open_items(uuid,uuid,date,date),public.statement_entry_reversed(uuid,uuid,uuid),public.ledger_written_here(xid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.statement_row_coverages(uuid,uuid,uuid,timestamptz),public.statement_explained(uuid,uuid,uuid,timestamptz),public.statement_current_reconciled(uuid,uuid,uuid),public.statement_line_allocated(uuid,uuid,uuid,text,integer,date,uuid[]),public.statement_line_allocations(uuid,uuid,uuid[],text,integer,date,uuid[]),public.bank_entry_matches(uuid,uuid,uuid[],integer) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_decision_allowed(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.accounting_period_preview(uuid,uuid,text,jsonb) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_allowed(uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_write_allowed(uuid),access.owner_document_allowed(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.delegation_allowed(uuid,boolean),access.require_agent(text,text,uuid,uuid,uuid,uuid,text) TO " + role,
		// Bank activity. Row security decides who reads and writes each row:
		// the sync its own login, the owner settings and links, and agents
		// links at their connection level.
		"GRANT SELECT,INSERT,UPDATE ON public.bank_provider_settings,public.vult_bank_provider_settings,public.bank_connections,public.bank_accounts,public.bank_transactions,public.bank_transaction_links TO " + role,
		"GRANT SELECT,INSERT ON public.bank_transaction_duplicates,public.bank_transaction_moves,public.bank_account_aliases,public.bank_transaction_aliases TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON public.recording_rules,public.rule_recording_grants,public.recording_rule_parts TO " + role,
		"GRANT EXECUTE ON FUNCTION public.rule_owner_allowed(uuid,uuid),public.ledger_rule_recording(),public.rule_key(text),public.rule_owner_narrowing(uuid,uuid),public.agent_change_narrows(text,text,jsonb,jsonb),public.agent_owner_change_allowed(uuid,uuid,text,text,jsonb,jsonb) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.bank_login_disconnected(uuid,uuid,uuid) TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON public.agent_settings TO " + role,
		"GRANT EXECUTE ON FUNCTION public.agent_secret_for(uuid,uuid),public.vult_agent_secret(uuid),public.vult_agent_key_checked(uuid,bytea,text,text),public.agent_books(),public.vult_agent_reseal(uuid,bytea,bytea,integer) TO " + role,
		// Linked Books: the owner links a business and a personal Book; pairs
		// are joined and read through the ledger's own functions.
		"GRANT SELECT,INSERT,UPDATE,DELETE ON public.book_links TO " + role,
		"GRANT SELECT,INSERT,UPDATE,DELETE ON public.book_link_terms TO " + role,
		"GRANT SELECT ON public.proposal_pairs TO " + role,
		"GRANT SELECT ON public.allocations TO " + role,
		"GRANT SELECT ON public.claim_obligations,public.claim_obligation_adjustments TO " + role,
		"GRANT SELECT,INSERT,DELETE ON public.assignment_rules TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON public.document_groups TO " + role,
		"GRANT SELECT,INSERT,UPDATE,DELETE ON public.projects,public.project_labels,public.project_rules,public.project_promotions,public.project_carried TO " + role,
		"GRANT EXECUTE ON FUNCTION public.carry_cost(uuid,text,uuid,uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.document_group_writer(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.project_writer(uuid,uuid) TO " + role,
		"GRANT SELECT,INSERT ON public.document_checks,public.document_readings TO " + role,
		"GRANT SELECT,INSERT ON public.entry_pairs TO " + role,
		"GRANT EXECUTE ON FUNCTION public.book_link_owner(uuid),public.book_profile(uuid,uuid),public.book_taxed_as(uuid,uuid),public.book_links_for(uuid,uuid),public.active_link_terms(uuid,uuid,uuid),public.pair_proposal(uuid,uuid,uuid),public.proposal_pair_of(uuid,uuid,uuid),public.entry_pair_of(uuid,uuid,uuid),public.keeps_expense_reports(uuid,uuid),public.suggested_beneficiaries(uuid,uuid,uuid),public.settle_pair(uuid,text,uuid,uuid,uuid,uuid,jsonb),public.settlement_preview(uuid,uuid,uuid,jsonb),public.linked_balances(uuid,uuid),public.book_amount(uuid,uuid,bigint),public.opening_equity_account(uuid,uuid),public.open_link_balance(uuid,text,uuid,uuid,uuid,uuid),public.link_owner_narrowing(uuid) TO " + role,
		// Earned autonomy: the ledger keeps limits and trust; agents read them.
		"GRANT SELECT,INSERT,UPDATE ON public.agent_policy TO " + role,
		"GRANT SELECT,INSERT,DELETE ON public.agent_tax_sensitive_accounts TO " + role,
		"GRANT SELECT ON public.agent_kinds,public.agent_kind_events TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON public.agent_questions,public.agent_loops,public.agent_profile,public.agent_seen TO " + role,
		"GRANT EXECUTE ON FUNCTION public.agent_note_allowed(uuid,uuid),public.agent_note_owner(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_confident_recording(),public.agent_amount_cap(uuid,uuid),public.agent_default_trust(uuid,uuid),public.agent_record_skills(uuid,uuid,text,text,integer,integer),public.agent_kind_rule(uuid,uuid,uuid,text,text,uuid) TO " + role,
		// Erasure of a deleted Book, once its waiting period is over.
		"GRANT EXECUTE ON FUNCTION public.book_erasures_due(integer),public.begin_book_erasure(uuid,uuid),public.finish_book_erasure(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.bank_sync_allowed(uuid,uuid,uuid),public.bank_read_allowed(uuid,uuid),public.bank_owner_allowed(uuid,uuid),public.vult_bank_owner_allowed(uuid),public.bank_connection_for_item(text,text),public.bank_connections_due(interval,integer),public.bank_connections_to_release(integer),public.bank_request_sync(uuid),public.operation_dependencies_journal(uuid,uuid,text,jsonb,uuid) TO " + role,
	}
	for _, name := range []string{"ledger_context(uuid,uuid)", "ledger_allowed(uuid,uuid,boolean)", "ledger_lock(uuid,uuid)", "ledger_import_owner()"} {
		statements = append(statements, "GRANT EXECUTE ON FUNCTION public."+name+" TO "+role)
	}
	if _, e = tx.Exec(ctx, strings.Join(statements, ";\n")); e != nil {
		return result, e
	}
	if beforeFactCheck != nil {
		if e = beforeFactCheck(ctx, tx); e != nil {
			return result, e
		}
	}
	if exists && applied < len(names) {
		after, e := Fingerprint(ctx, tx)
		if e != nil {
			return result, e
		}
		if after.SHA256 != result.Fingerprint.SHA256 {
			return result, ErrFactsChanged
		}
	}
	return result, tx.Commit(ctx)
}

// UpgradeRefusal is a schema change the database declined on purpose
// because stored records don't fit it yet. Nothing was changed; the hint says
// what to change before upgrading again.
type UpgradeRefusal struct{ Message, Hint string }

func (e *UpgradeRefusal) Error() string {
	if e.Hint == "" {
		return "the upgrade was refused and nothing was changed: " + e.Message
	}
	return "the upgrade was refused and nothing was changed: " + e.Message + ". " + e.Hint
}

func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := verifyReceipts(ctx, pool, migrationNames(), true)
	return err
}

type receiptReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// verifyReceipts reads the installed migrations and says how many of this
// release's are applied. Receipts are this release's migrations in order;
// or, for a Vult that took 0.8's steps one by one, 0001 to 0007 then those
// steps in order, with the squashed migration recorded once all are in.
// A Vult part way through the steps counts the migrations before the
// squashed one as applied, and bridgeLeft says which steps remain.
func verifyReceipts(ctx context.Context, db receiptReader, expected []string, complete bool) (int, error) {
	n, _, err := readReceipts(ctx, db, expected, complete)
	return n, err
}

func readReceipts(ctx context.Context, db receiptReader, expected []string, complete bool) (applied int, bridgeLeft []string, err error) {
	rows, e := db.Query(ctx, "SELECT filename,sha256 FROM public.schema_receipts ORDER BY filename")
	if e != nil {
		return 0, nil, errors.New("schema verification failed")
	}
	got := map[string]string{}
	for rows.Next() {
		var name, sum string
		if rows.Scan(&name, &sum) != nil {
			rows.Close()
			return 0, nil, errors.New("schema verification failed")
		}
		got[name] = sum
	}
	rows.Close()
	if rows.Err() != nil || len(got) == 0 {
		return 0, nil, errors.New("schema verification failed")
	}
	got = withEarlierSquash(got)
	mismatch := errors.New("installed schema does not match this release")
	squashed := slices.Index(expected, squashedMigration)
	steps := bridgeNames()
	took := 0
	for _, name := range steps {
		if _, ok := got[name]; ok {
			took++
		}
	}
	// This release's migrations, in order, from the first.
	for applied < len(expected) {
		sum, ok := got[expected[applied]]
		if !ok {
			break
		}
		if sum != migrationSum(expected[applied]) {
			return 0, nil, mismatch
		}
		applied++
	}
	switch {
	case took == 0:
		// A new Vult's receipts: nothing else may be there.
		if len(got) != applied {
			return 0, nil, mismatch
		}
	case squashed < 0 || applied < squashed:
		return 0, nil, mismatch
	default:
		// The steps, in order from the first, each as released. None left
		// is an empty list, not none known.
		bridgeLeft = []string{}
		for i, name := range steps {
			sum, ok := got[name]
			if !ok {
				if took != i {
					return 0, nil, mismatch
				}
				bridgeLeft = steps[i:]
				break
			}
			if sum != bridgeSum(name) {
				return 0, nil, mismatch
			}
		}
		if len(bridgeLeft) > 0 && applied > squashed {
			return 0, nil, mismatch
		}
		if len(got) != applied+took {
			return 0, nil, mismatch
		}
	}
	if complete && applied != len(expected) {
		return 0, nil, errors.New("schema verification failed")
	}
	return applied, bridgeLeft, nil
}
