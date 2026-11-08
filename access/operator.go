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
		"GRANT EXECUTE ON FUNCTION access.passkey_profile(text,text),access.begin_passkey(text,text,bigint,text,text,text,text,jsonb),access.take_passkey(text,text,text,text),access.finish_passkey(text,text,text,text,text,jsonb,jsonb,text,text),access.recover_passkey(text,text,uuid,text,text,text) TO " + a,
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
		"GRANT UPDATE(revoked_at) ON public.connections TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_lock(uuid,uuid),public.ledger_context(uuid,uuid),public.ledger_import_owner(),public.ledger_allowed(uuid,uuid,boolean) TO " + r,
		"GRANT INSERT ON public.requests,public.entries,public.lines,public.source_citations,public.operation_consumptions,public.documents,public.document_uploads TO " + r,
		"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at,used_at) ON public.documents TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_posting_allowed(uuid,uuid),access.owner_posting_consumer(uuid,uuid,uuid),access.hold_operation_owner(uuid,uuid,uuid,bigint) TO " + r,
		"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid),public.ledger_validate_balances(uuid,uuid,uuid),public.ledger_native_position(uuid,uuid,uuid,text,bigint,bigint) TO " + r,
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
