package ledger

import (
	"context"
	"time"
)

// ConnectionActivity is a connection with the last time its agent recorded
// anything in the Book. Reads are not recorded, so an agent that only reads
// has no recorded activity.
type ConnectionActivity struct {
	Connection
	LastRecordedAt *time.Time `json:"last_recorded_at,omitempty"`
}

// ConnectionsWithActivity lists live connections, or the most recently
// disconnected ones (within 90 days, newest first, at most limit), each with
// its last recorded activity. Too many live connections is an error; older
// disconnections simply fall off the history.
func (a TenantAdmin) ConnectionsWithActivity(ctx context.Context, limit int, disconnected bool) ([]ConnectionActivity, error) {
	if !a.valid() {
		return nil, ErrDenied
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	filter, order := "c.revoked_at IS NULL", "c.created_at,c.id"
	if disconnected {
		filter = "c.revoked_at IS NOT NULL AND c.revoked_at > clock_timestamp() - interval '90 days'"
		order = "c.revoked_at DESC,c.id"
	}
	rows, err := a.db.Query(ctx, `SELECT c.id::text,c.tenant_id::text,c.book_id::text,c.actor_id::text,c.name,c.access,c.created_at,c.revoked_at,
 (SELECT max(r.recorded_at) FROM public.requests r WHERE r.tenant_id=c.tenant_id AND r.book_id=c.book_id AND r.actor_id=c.actor_id)
 FROM public.connections c WHERE c.tenant_id=$1 AND `+filter+` ORDER BY `+order+` LIMIT $2`, a.tenantID, limit+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []ConnectionActivity{}
	for rows.Next() {
		var c ConnectionActivity
		if err = rows.Scan(&c.ID, &c.TenantID, &c.BookID, &c.ActorID, &c.Name, &c.Access, &c.CreatedAt, &c.RevokedAt, &c.LastRecordedAt); err != nil {
			return nil, dbError(err)
		}
		out = append(out, c)
	}
	if len(out) > limit {
		if disconnected {
			return out[:limit], dbError(rows.Err())
		}
		return nil, ErrLimit
	}
	return out, dbError(rows.Err())
}
