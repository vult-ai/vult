package access

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

// Operator is offline installation authority, never an HTTP dependency.
type Operator struct{ DB *pgxpool.Pool }

// BindOwner creates a tenant when tenant is empty, or binds an existing tenant
// explicitly. A tenant can have one owner; replacing it requires recovery.
func (o Operator) BindOwner(ctx context.Context, tenant string, id Identity) (string, error) {
	if o.DB == nil || !validIdentity(id) || tenant != "" && !ledger.ValidID(tenant) {
		return "", ErrDenied
	}
	tx, e := o.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return "", ErrDenied
	}
	defer tx.Rollback(context.Background())
	tenant, _, e = bindOwner(ctx, tx, tenant, id)
	if e != nil {
		return "", e
	}
	if tx.Commit(ctx) != nil {
		return "", ErrDenied
	}
	return tenant, nil
}

func bindOwner(ctx context.Context, tx pgx.Tx, tenant string, id Identity) (string, string, error) {
	if !validIdentity(id) || tenant != "" && !ledger.ValidID(tenant) {
		return "", "", ErrDenied
	}
	if tenant == "" {
		tenant = ledger.ID()
		if _, e := tx.Exec(ctx, "INSERT INTO public.tenants(id) VALUES($1)", tenant); e != nil {
			return "", "", ErrDenied
		}
	}
	var user string
	e := tx.QueryRow(ctx, `INSERT INTO access.identities(id,issuer,subject) VALUES($1,$2,$3)
 ON CONFLICT(issuer,subject) DO UPDATE SET issuer=excluded.issuer RETURNING id::text`, ledger.ID(), id.Issuer, id.Subject).Scan(&user)
	if e != nil {
		return "", "", ErrDenied
	}
	if _, e = tx.Exec(ctx, "INSERT INTO access.owners(tenant_id,identity_id) VALUES($1,$2)", tenant, user); e != nil {
		return "", "", ErrDenied
	}
	return tenant, user, nil
}

// RevokeIdentity invalidates sessions and delegated credentials through its epoch.
func (o Operator) RevokeIdentity(ctx context.Context, id Identity) error {
	if o.DB == nil || !validIdentity(id) {
		return ErrDenied
	}
	tag, e := o.DB.Exec(ctx, "UPDATE access.identities SET epoch=epoch+1 WHERE issuer=$1 AND subject=$2", id.Issuer, id.Subject)
	if e != nil || tag.RowsAffected() != 1 {
		return ErrDenied
	}
	return nil
}

// ConfigureRoles grants only distinct restricted LOGIN roles. The ledger runtime
// role is configured by ledger.Apply and must remain separate from both roles.
func (o Operator) ConfigureRoles(ctx context.Context, runtimeRole, authRole, ownerRole string) error {
	if o.DB == nil || runtimeRole == "" || authRole == "" || ownerRole == "" || authRole == ownerRole || runtimeRole == authRole || runtimeRole == ownerRole || strings.ContainsAny(runtimeRole+authRole+ownerRole, "\x00") {
		return ErrDenied
	}
	for _, role := range []string{runtimeRole, authRole, ownerRole} {
		var safe bool
		e := o.DB.QueryRow(ctx, `SELECT rolcanlogin AND NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
 AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid)
 FROM pg_roles r WHERE rolname=$1`, role).Scan(&safe)
		if e != nil || !safe {
			return ErrDenied
		}
	}
	tx, e := o.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return ErrDenied
	}
	defer tx.Rollback(context.Background())
	a, r := pgx.Identifier{authRole}.Sanitize(), pgx.Identifier{ownerRole}.Sanitize()
	statements := []string{
		"GRANT USAGE ON SCHEMA access,public TO " + a + "," + r,
		"GRANT SELECT ON public.schema_receipts TO " + a + "," + r,
		"GRANT EXECUTE ON FUNCTION access.issue_session(text,text,text,text,timestamptz),access.session_identity(text,text),access.session_tenants(text,text),access.end_session(text,text) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.owner_resource_tenant(text,text,text,uuid) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.owner_book_views(text,text,uuid[]) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.builtin_ready(text,text),access.reserve_builtin(text),access.finish_builtin(uuid,bigint,text,text,bigint,text),access.start_login(text,text,text,text,text,text),access.consume_login(text,text,text,text) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.passkey_profile(text,text),access.begin_passkey(text,text,bigint,text,text,text,text,jsonb),access.take_passkey(text,text,text,text),access.finish_passkey(text,text,text,text,text,jsonb,jsonb,text,text),access.recover_passkey(text,text,uuid,text,text,text),access.issue_recovery_codes(text,text,text[]) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.local_authenticator_ready(text,text,text),access.begin_local_authenticator(text,text,text,text,text,bytea,text),access.reserve_local_authenticator(text,text,text,text),access.finish_local_authenticator(uuid,text,text,bigint,text,bytea,bigint,text) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.register_client(text,text,text[]),access.start_oauth(text,text,text,text,text,text,text,text),access.pending_oauth(text,text),access.consent_oauth(text,text,text,uuid,uuid,text,uuid,text,boolean),access.exchange_oauth(text,text,text,text,text,text,text,text,text),access.resolve_agent(text,text) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.require_owner(uuid,boolean),access.owner_allowed(uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.report_postings(uuid,uuid,date,timestamptz,date,date,date,date) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.require_account_owner(),access.book_archive_tenant(uuid) TO " + r,
		"GRANT SELECT,INSERT ON access.user_settings TO " + r,
		"GRANT UPDATE(revision,display_name,preferences,appearance,avatar_id,avatar_sha256,avatar_bytes) ON access.user_settings TO " + r,
		"GRANT SELECT,INSERT ON access.book_view_preferences TO " + r,
		"GRANT UPDATE(period,range_from,range_to) ON access.book_view_preferences TO " + r,
		"GRANT SELECT ON public.book_addresses,public.book_identity_revisions,public.book_logo_images TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_set_book_logo(uuid,uuid,text,text,text,bytea) TO " + r,
		"GRANT UPDATE(slug,revision,aliases) ON public.book_addresses TO " + r,
		"GRANT INSERT ON public.book_identity_revisions TO " + r,
		"GRANT EXECUTE ON FUNCTION access.book_conflicting_aliases(uuid,uuid,text[]) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.delegation_allowed(uuid,boolean) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.consent_books(text,text),access.consent_connections(text,text) TO " + a,
		// Archiving and deleting a Book: passkey proofs for the sign-in role,
		// the owner's steps for the owner's role.
		"GRANT EXECUTE ON FUNCTION access.begin_book_proof(text,text,bigint,text,text,uuid,uuid,text,jsonb),access.take_book_proof(text,text,text),access.finish_book_proof(text,text,text,text,jsonb,jsonb,text) TO " + a,
		"GRANT EXECUTE ON FUNCTION access.begin_report_proof(text,text,bigint,text,text,uuid,uuid,text,text,jsonb) TO " + a,
		"GRANT EXECUTE ON FUNCTION public.archive_book(uuid,uuid,boolean,text,text),public.book_deletion_summary(uuid,uuid),public.request_book_deletion(uuid,uuid,text,text,text,text),public.confirm_book_deletion(uuid,uuid,text,text,text),public.cancel_book_deletion(uuid,uuid,text,text),public.book_lifecycle(uuid),public.book_deletion_history(uuid,integer) TO " + r,
		"GRANT SELECT ON public.archived_books,public.book_deletions TO " + r,
		"GRANT SELECT,INSERT ON access.owner_requests,public.book_revisions TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_identity(uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_write_allowed(uuid),access.owner_document_allowed(uuid,uuid) TO " + r,
		"GRANT SELECT ON public.books,public.connections,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods TO " + r,
		"GRANT SELECT ON public.source_citations,public.evidence_links,public.document_uploads TO " + r,
		"GRANT SELECT ON public.documents,public.operation_proposals,public.operation_decisions,public.operation_consumptions TO " + r,
		// Book search: its index and folding. Row policies (owner_read) still
		// limit every read to the owner's tenant.
		"GRANT SELECT ON public.search_words,public.search_totals TO " + r,
		"GRANT EXECUTE ON FUNCTION public.search_text(text),public.search_tokens(text) TO " + r,
		"GRANT INSERT ON public.operation_decisions TO " + r,
		"GRANT EXECUTE ON FUNCTION access.operation_owner_epoch(uuid),access.operation_owner_allowed(uuid,uuid,uuid,bigint),access.operation_executor_active(uuid,uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.operation_dependencies(uuid,uuid,text,jsonb,uuid),public.operation_dependencies_settled(jsonb) TO " + r,
		"GRANT INSERT ON public.books,public.connections,public.closed_periods TO " + r,
		"GRANT UPDATE(write_epoch) ON public.books TO " + r,
		"GRANT UPDATE(revoked_at,access) ON public.connections TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_lock(uuid,uuid),public.ledger_context(uuid,uuid),public.ledger_import_owner(),public.ledger_allowed(uuid,uuid,boolean) TO " + r,
		"GRANT INSERT ON public.requests,public.entries,public.lines,public.source_citations,public.operation_consumptions,public.documents,public.document_uploads TO " + r,
		"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at,used_at) ON public.documents TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_posting_allowed(uuid,uuid),access.owner_posting_consumer(uuid,uuid,uuid),access.hold_operation_owner(uuid,uuid,uuid,bigint) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid),public.ledger_validate_balances(uuid,uuid,uuid),public.ledger_native_position(uuid,uuid,uuid,text,bigint,bigint) TO " + r,
		// Accounts the owner changes directly or approves from an agent.
		"GRANT INSERT ON public.accounts,public.account_revisions TO " + r,
		// Accounting foundations: the owner records decisions directly and
		// runs approved agent work in every table.
		"GRANT SELECT,INSERT ON public.payees,public.payee_revisions,public.entry_payees,public.statements,public.statement_rows,public.statement_duplicates,public.statement_matches,public.statement_match_parts,public.statement_match_revocations,public.statement_adjustments,public.statement_reviews,public.accounting_periods,public.accounting_period_events TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_restoring(uuid,uuid),public.ledger_role_fits(text,text),public.ledger_period_closed(uuid,uuid,date),public.statement_line_amount(uuid,uuid,uuid,text,integer),public.statement_row_check(uuid,uuid,uuid),public.statement_line_check(uuid,uuid,uuid,text,integer),public.ledger_owner_decision(uuid,uuid),public.period_open_items(uuid,uuid,date,date),public.statement_entry_reversed(uuid,uuid,uuid),public.ledger_written_here(xid) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_decision_allowed(uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.accounting_period_preview(uuid,uuid,text,jsonb) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.statement_row_coverages(uuid,uuid,uuid,timestamptz),public.statement_explained(uuid,uuid,uuid,timestamptz),public.statement_current_reconciled(uuid,uuid,uuid),public.statement_line_allocated(uuid,uuid,uuid,text,integer,date,uuid[]),public.statement_line_allocations(uuid,uuid,uuid[],text,integer,date,uuid[]),public.bank_entry_matches(uuid,uuid,uuid[],integer) TO " + r,
		// Bank activity: the owner stores provider keys, links logins and
		// accounts, records and links activity, and runs approved agent work.
		"GRANT SELECT,INSERT,UPDATE ON public.bank_provider_settings,public.vult_bank_provider_settings,public.bank_connections,public.bank_accounts,public.bank_transaction_links TO " + r,
		// Statement lines the owner imports, or approves an agent importing,
		// become inbox items; row security limits the owner to those.
		"GRANT SELECT,INSERT,UPDATE ON public.bank_transactions TO " + r,
		"GRANT SELECT ON public.bank_account_aliases,public.bank_transaction_aliases TO " + r,
		"GRANT SELECT,INSERT ON public.bank_transaction_duplicates,public.bank_transaction_moves TO " + r,
		"GRANT SELECT,INSERT,UPDATE ON public.recording_rules,public.rule_recording_grants,public.recording_rule_parts TO " + r,
		"GRANT EXECUTE ON FUNCTION public.rule_owner_allowed(uuid,uuid),public.ledger_rule_recording(),public.rule_key(text),public.rule_owner_narrowing(uuid,uuid),public.agent_change_narrows(text,text,jsonb,jsonb),public.agent_owner_change_allowed(uuid,uuid,text,text,jsonb,jsonb) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.bank_login_disconnected(uuid,uuid,uuid) TO " + r,
		"GRANT SELECT,INSERT,UPDATE ON public.agent_settings TO " + r,
		// The Vult's agent model and key: the owner's role alone, never an agent's.
		"GRANT SELECT,INSERT,UPDATE ON public.vult_agent_settings TO " + r,
		"GRANT EXECUTE ON FUNCTION public.vult_agent_owner_allowed(uuid),public.vult_agent_remove_key(uuid,uuid) TO " + r,
		// Linked Books: the owner links a business and a personal Book; pairs
		// are joined and read through the ledger's own functions.
		"GRANT SELECT,INSERT,UPDATE,DELETE ON public.book_links TO " + r,
		"GRANT SELECT,INSERT,UPDATE,DELETE ON public.book_link_terms TO " + r,
		"GRANT SELECT ON public.proposal_pairs TO " + r,
		"GRANT SELECT ON public.allocations TO " + r,
		// Expense reports: the owner's role writes them; agents only propose.
		"GRANT SELECT,INSERT,UPDATE ON public.people,public.report_policies,public.expense_reports,public.claims,public.claim_evidence,public.report_revisions,public.report_lines,public.report_approvals,public.report_events,public.recoverables,public.recoverable_events,public.settlement_statements,public.retired_report_references,public.report_notes,public.recoverable_reviews,public.payment_residuals TO " + r,
		"GRANT SELECT ON public.person_revisions TO " + r,
		"GRANT SELECT ON public.claim_obligations,public.claim_obligation_adjustments TO " + r,
		"GRANT DELETE ON public.report_lines,public.claims TO " + r,
		// Reports read what is open of each cost and what was paid on it.
		"GRANT EXECUTE ON FUNCTION public.open_costs(uuid,uuid,uuid,uuid,uuid),public.cost_amount(uuid,uuid,uuid),public.cost_repaid_amount(uuid,uuid,uuid,uuid),public.allocation_live(uuid,public.allocations),public.entry_reversed(uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.expense_writer(uuid,uuid),public.report_spend_proof(uuid,uuid,text,text,text),public.report_settle(uuid,uuid,uuid,uuid,jsonb,boolean),public.remove_allocation(uuid,uuid,uuid,text),public.unapplied_amount(uuid,uuid,uuid,uuid),public.accept_claim(uuid,uuid,uuid,uuid),public.record_person_side(uuid,uuid,uuid,text,uuid,uuid),public.adjust_claim_obligation(uuid,uuid,uuid,integer,integer,bigint,uuid,uuid),public.record_person_adjustment(uuid,uuid,uuid,uuid),public.delete_draft_report(uuid,uuid,uuid),public.person_capacity_on(uuid,uuid,date),public.policy_version_on(uuid,uuid,date) TO " + r,
		"GRANT SELECT,INSERT,DELETE ON public.assignment_rules TO " + r,
		"GRANT SELECT,INSERT,UPDATE ON public.document_groups TO " + r,
		"GRANT SELECT,INSERT,UPDATE,DELETE ON public.projects,public.project_labels,public.project_rules,public.project_promotions,public.project_carried TO " + r,
		"GRANT EXECUTE ON FUNCTION public.carry_cost(uuid,text,uuid,uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.document_group_writer(uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.project_writer(uuid,uuid) TO " + r,
		"GRANT SELECT,INSERT ON public.document_checks,public.document_readings TO " + r,
		"GRANT SELECT,INSERT ON public.entry_pairs TO " + r,
		"GRANT EXECUTE ON FUNCTION public.book_link_owner(uuid),public.book_profile(uuid,uuid),public.book_taxed_as(uuid,uuid),public.book_links_for(uuid,uuid),public.active_link_terms(uuid,uuid,uuid),public.pair_proposal(uuid,uuid,uuid),public.proposal_pair_of(uuid,uuid,uuid),public.entry_pair_of(uuid,uuid,uuid),public.keeps_expense_reports(uuid,uuid),public.suggested_beneficiaries(uuid,uuid,uuid),public.settle_pair(uuid,text,uuid,uuid,uuid,uuid,jsonb),public.settlement_preview(uuid,uuid,uuid,jsonb),public.linked_balances(uuid,uuid),public.book_amount(uuid,uuid,bigint),public.opening_equity_account(uuid,uuid),public.open_link_balance(uuid,text,uuid,uuid,uuid,uuid),public.link_owner_narrowing(uuid) TO " + r,
		"GRANT SELECT,INSERT,UPDATE ON public.agent_policy TO " + r,
		"GRANT SELECT,INSERT,DELETE ON public.agent_tax_sensitive_accounts TO " + r,
		"GRANT SELECT ON public.agent_kinds,public.agent_kind_events TO " + r,
		"GRANT SELECT,INSERT,UPDATE ON public.agent_questions,public.agent_loops,public.agent_profile,public.agent_seen TO " + r,
		"GRANT EXECUTE ON FUNCTION public.agent_note_allowed(uuid,uuid),public.agent_note_owner(uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_confident_recording(),public.agent_amount_cap(uuid,uuid),public.agent_default_trust(uuid,uuid),public.agent_kind_rule(uuid,uuid,uuid,text,text,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.bank_sync_allowed(uuid,uuid,uuid),public.bank_read_allowed(uuid,uuid),public.bank_owner_allowed(uuid,uuid),public.vult_bank_owner_allowed(uuid),public.bank_request_sync(uuid),public.operation_dependencies_journal(uuid,uuid,text,jsonb,uuid) TO " + r,
	}
	for _, sql := range statements {
		if _, e = tx.Exec(ctx, sql); e != nil {
			return ErrDenied
		}
	}
	if tx.Commit(ctx) != nil {
		return ErrDenied
	}
	return nil
}
