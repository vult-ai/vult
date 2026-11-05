package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/mcpserver"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

var ErrResponseLimit = errors.New("response exceeds server limits; request a smaller journal page or contact your operator")
var errOperation = errors.New("operation unavailable; for a lost write response, look up the original request key")

// TransactionAdmission applies durable quotas only to a new financial request,
// after retained-outcome lookup, inside the same financial transaction.
// It receives the already-authorized scope and cannot expand ledger authority.
type TransactionAdmission interface {
	CheckTransaction(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error
}

type OwnerAdmission interface {
	CheckOwnerTransaction(context.Context, pgx.Tx, string, string, json.RawMessage) error
}

func remoteCall(ctx context.Context, db *pgxpool.Pool, sessions *access.Service, token string, scope ledger.Scope, admission Admission, method string, input json.RawMessage) (json.RawMessage, error) {
	if !ledger.Supported(method) {
		return nil, ledger.ErrDenied
	}
	if method == "list_journals" {
		var q ledger.ListQuery
		if strictjson.Decode(input, &q) != nil {
			return nil, ledger.ErrInvalid
		}
		if q.Limit > 20 {
			return nil, ErrResponseLimit
		}
		if q.Limit == 0 {
			q.Limit = 20
		}
		input, _ = json.Marshal(q)
	}
	if admission != nil && admission.Check(ctx, scope, method) != nil {
		return nil, ledger.ErrDenied
	}
	store := ledger.Store{Runtime: db, Authorize: func(ctx context.Context, tx pgx.Tx, s ledger.Scope, m string, b json.RawMessage) error {
		if e := sessions.AgentAuthorizer(token)(ctx, tx, s, m, b); e != nil {
			return ledger.ErrDenied
		}
		if m == "list_accounts" || m == "get_trial_balance" || m == "get_balance" {
			var tooMany bool
			if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 OFFSET 2000 LIMIT 1)", s.TenantID, s.BookID).Scan(&tooMany); e != nil {
				return errOperation
			}
			if tooMany {
				return ErrResponseLimit
			}
		}
		return nil
	}}
	if check, ok := admission.(TransactionAdmission); ok {
		store.AdmitWrite = func(ctx context.Context, tx pgx.Tx, s ledger.Scope, m string, b json.RawMessage) error {
			if check.CheckTransaction(ctx, tx, s, m, b) != nil {
				return ledger.ErrDenied
			}
			return nil
		}
	}
	result, e := store.Call(ctx, scope, method, input)
	if e != nil {
		for _, safe := range []error{ledger.ErrInvalid, ledger.ErrDenied, ledger.ErrConflict, ledger.ErrNotFound, ledger.ErrUncertain, ErrResponseLimit} {
			if errors.Is(e, safe) {
				return nil, safe
			}
		}
		return nil, errOperation
	}
	if len(result) > 4<<20 {
		return nil, ErrResponseLimit
	}
	return result, nil
}

func remoteMCP(db *pgxpool.Pool, sessions *access.Service, admission Admission) http.Handler {
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Cookie")) != 0 || r.URL.RawQuery != "" {
			failure(w, 403)
			return
		}
		value := one(r.Header, "Authorization")
		if !strings.HasPrefix(value, "Bearer ") || len(value) != 50 {
			unauthorized(w, sessions)
			return
		}
		token := strings.TrimPrefix(value, "Bearer ")
		scope, e := sessions.ResolveAgent(r.Context(), token)
		if e != nil {
			unauthorized(w, sessions)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			failure(w, 429)
			return
		}
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return mcpserver.NewRemote(func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
				ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				return remoteCall(ctx, db, sessions, token, scope, admission, method, input)
			}, scope.Access)
		}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, DisableLocalhostProtection: true, PropagateRequestCancellation: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		// The enclosing ingress validates exact public Host/HTTPS and trusted proxy;
		// this transport must therefore accept that proxy's loopback connection.
		handler.ServeHTTP(w, r)
	})
}
func unauthorized(w http.ResponseWriter, s *access.Service) {
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+strings.TrimSuffix(s.Resource(), "/mcp")+`/.well-known/oauth-protected-resource"`)
	failure(w, 401)
}
