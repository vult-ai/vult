package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const connectionPath = "/api/v1/access/tokens"

type connectionGrant struct {
	BookID string   `json:"book_id"`
	Role   string   `json:"role"`
	Rights []string `json:"rights"`
}

type browserConnection struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Connection bool              `json:"connection"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  *time.Time        `json:"expires_at"`
	BookGrants []connectionGrant `json:"book_grants"`
	// Access is read or write; LastRecordedAt is the agent's latest recorded
	// change; RevokedAt is set only on disconnected agents.
	Access         string     `json:"access,omitempty"`
	LastRecordedAt *time.Time `json:"last_recorded_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

func browserGrant(c ledger.Connection) connectionGrant {
	grant := connectionGrant{BookID: c.BookID, Role: "none", Rights: []string{}}
	if c.RevokedAt == nil {
		grant.Role, grant.Rights = "view", []string{"read"}
		switch c.Access {
		case "write":
			grant.Role, grant.Rights = "manage", append(grant.Rights, "write")
		case "propose":
			grant.Role, grant.Rights = "propose", append(grant.Rights, "propose")
		}
	}
	return grant
}

func (a *webAPI) listConnections(w http.ResponseWriter, r *http.Request, session string) {
	tenants, err := a.sessions.TenantsBounded(r.Context(), session, webBookLimit)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tokens := []browserConnection{}
	disconnected := []browserConnection{}
	for _, tenant := range tenants {
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
			list, err := owner.ConnectionsWithActivity(r.Context(), max(1, webBookLimit-len(tokens)), false)
			if err != nil {
				return err
			}
			for _, c := range list {
				tokens = append(tokens, browserConnection{
					ID: c.ID, Name: c.Name, Connection: true, CreatedAt: c.CreatedAt,
					BookGrants: []connectionGrant{browserGrant(c.Connection)}, Access: c.Access, LastRecordedAt: c.LastRecordedAt,
				})
			}
			gone, err := owner.ConnectionsWithActivity(r.Context(), 50, true)
			for _, c := range gone {
				disconnected = append(disconnected, browserConnection{
					ID: c.ID, Name: c.Name, Connection: true, CreatedAt: c.CreatedAt,
					BookGrants: []connectionGrant{{BookID: c.BookID, Role: "none", Rights: []string{}}}, Access: c.Access, LastRecordedAt: c.LastRecordedAt, RevokedAt: c.RevokedAt,
				})
			}
			return err
		})
		if err == nil && len(tokens) > webBookLimit {
			err = ledger.ErrLimit
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "tokens": tokens, "disconnected": disconnected})
}

type connectionBook struct {
	BookID string `json:"book_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type connectionBooks struct {
	SchemaVersion int              `json:"schema_version"`
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Revision      string           `json:"revision"`
	Books         []connectionBook `json:"books"`
	ledger.RecordedState
}

func (a *webAPI) readConnectionBooks(w http.ResponseWriter, r *http.Request, session, id string) {
	ctx := r.Context()
	tenant, err := a.sessions.ConnectionTenant(ctx, session, id)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out connectionBooks
	err = a.sessions.WithOwner(ctx, session, tenant, false, func(owner ledger.TenantAdmin) error {
		c, err := owner.ConnectionRecord(ctx, id)
		if err != nil {
			return err
		}
		book, state, err := owner.BookWithRecordedState(ctx, c.BookID)
		if err != nil {
			return err
		}
		revision := "1"
		if c.RevokedAt != nil {
			revision = "2"
		}
		out = connectionBooks{1, c.ID, c.Name, revision, []connectionBook{{c.BookID, book.Name, browserGrant(c).Role}}, state}
		return nil
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func emptyConnectionPatch(w http.ResponseWriter, r *http.Request) error {
	data, err := readOwnerBody(w, r, 1024)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) != 0 &&
		(len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" ||
			strictjson.DecodeNonNull(data, &struct{}{}, "") != nil) {
		return ledger.ErrInvalid
	}
	return nil
}

func (a *webAPI) ownerConnections(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if r.URL.Path == connectionPath && r.Method == http.MethodGet {
		a.listConnections(w, r, session)
		return
	}
	id, action, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, connectionPath+"/"), "/")
	if !ok || !ledger.ValidID(id) {
		apiFailure(w, ledger.ErrNotFound)
		return
	}
	switch {
	case action == "books" && r.Method == http.MethodGet:
		a.readConnectionBooks(w, r, session, id)
	case action == "access" && r.Method == http.MethodPost:
		data, err := readOwnerBody(w, r, 1024)
		var q struct {
			Access string `json:"access"`
		}
		if err == nil && (r.Header.Get("Content-Type") != "application/json" || strictjson.DecodeNonNull(data, &q, "") != nil || !ledger.ValidAccess(q.Access)) {
			err = ledger.ErrInvalid
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
		tenant, err := a.sessions.ConnectionTenant(r.Context(), session, id)
		if err == nil {
			err = a.sessions.WithOwner(r.Context(), session, tenant, true, func(owner ledger.TenantAdmin) error {
				return owner.ChangeAccess(r.Context(), id, q.Access)
			})
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "access": q.Access})
	case action == "revoke" && r.Method == http.MethodPost:
		if err := emptyConnectionPatch(w, r); err != nil {
			apiFailure(w, err)
			return
		}
		tenant, err := a.sessions.ConnectionTenant(r.Context(), session, id)
		if err == nil {
			err = a.sessions.WithOwner(r.Context(), session, tenant, true, func(owner ledger.TenantAdmin) error {
				return owner.Revoke(r.Context(), id)
			})
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "status": "revoked"})
	default:
		apiFailure(w, ledger.ErrNotFound)
	}
}
