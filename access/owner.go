package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

var ownerKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// OwnerAdmission can restrict a new, already-authorized owner effect inside its
// transaction. Retained outcomes are resolved before this restriction runs.
type OwnerAdmission func(context.Context, pgx.Tx, string, string, json.RawMessage) error

// OwnerRequest retains an exact result atomically with an owner change. The key
// is scoped to identity and tenant; it never overlaps agent financial receipts.
func (s *Service) OwnerRequest(ctx context.Context, session, tenant, key, operation string, input json.RawMessage, admit OwnerAdmission, call func(ledger.TenantAdmin) (json.RawMessage, error)) (json.RawMessage, error) {
	if !ownerKeyPattern.MatchString(key) || len(operation) < 1 || len(operation) > 64 || len(input) < 2 || len(input) > 8192 || !json.Valid(input) || call == nil {
		return nil, ledger.ErrInvalid
	}
	var result []byte
	e := s.withOwner(ctx, session, tenant, true, false, "owner-request:"+tenant, func(tx pgx.Tx, owner ledger.TenantAdmin, who string) error {
		var oldOperation string
		var oldInput []byte
		e := tx.QueryRow(ctx, "SELECT operation,request_bytes,result_bytes FROM access.owner_requests WHERE identity_id=$1 AND tenant_id=$2 AND request_key=$3", who, tenant, key).Scan(&oldOperation, &oldInput, &result)
		if e == nil {
			if operation != oldOperation || !bytes.Equal(input, oldInput) {
				return ledger.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return ErrDenied
		}
		var live string
		if e = tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", tenant).Scan(&live); e != nil || live != who {
			return ErrDenied
		}
		if admit != nil {
			if e = admit(ctx, tx, tenant, operation, input); e != nil {
				return ErrDenied
			}
		}
		result, e = call(owner)
		if e != nil {
			return e
		}
		if len(result) < 2 || len(result) > 1<<20 || !json.Valid(result) {
			return ledger.ErrInvalid
		}
		if _, e = tx.Exec(ctx, "INSERT INTO access.owner_requests(identity_id,tenant_id,request_key,operation,request_bytes,result_bytes) VALUES($1,$2,$3,$4,$5,$6)", who, tenant, key, operation, []byte(input), result); e != nil {
			return ErrDenied
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return json.RawMessage(result), nil
}

func (s *Service) OwnerOutcome(ctx context.Context, session, tenant, key string) (ledger.Outcome, error) {
	if !ownerKeyPattern.MatchString(key) {
		return ledger.Outcome{}, ledger.ErrInvalid
	}
	out := ledger.Outcome{Status: "unresolved"}
	e := s.withOwner(ctx, session, tenant, false, false, "", func(tx pgx.Tx, _ ledger.TenantAdmin, who string) error {
		var result []byte
		e := tx.QueryRow(ctx, "SELECT operation,result_bytes FROM access.owner_requests WHERE identity_id=$1 AND tenant_id=$2 AND request_key=$3", who, tenant, key).Scan(&out.Operation, &result)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return ErrDenied
		}
		out.Status = "recorded"
		out.Result = result
		return nil
	})
	return out, e
}
