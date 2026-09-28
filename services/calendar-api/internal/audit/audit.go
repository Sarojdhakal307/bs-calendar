// Package audit writes and reads the append-only audit trail.
// Every admin write records who did what, from where, with before/after snapshots.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"bscalendar/services/calendar-api/internal/store"
)

// Actor identifies who performed an action.
type Actor struct {
	UserID    string
	TenantID  string
	Email     string
	Role      string
	IP        string
	RequestID string
}

// CanSelfApprove reports whether the actor may approve their own change. Super admins may (for
// single-admin installations); the approval is still recorded in the audit log.
func (a Actor) CanSelfApprove() bool { return a.Role == "super_admin" }

// Write inserts an audit row using q (normally the same transaction as the change).
func Write(ctx context.Context, q store.Querier, a Actor, action, entity, entityID string, before, after any) error {
	b, err := toJSON(before)
	if err != nil {
		return err
	}
	af, err := toJSON(after)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, actor_id, actor_ip, action, entity, entity_id, before, after, request_id)
		VALUES (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, NULLIF($3,''), $4, $5, $6, $7, $8, NULLIF($9,''))`,
		a.TenantID, a.UserID, a.IP, action, entity, entityID, b, af, a.RequestID)
	if err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}

// Entry is one audit row.
type Entry struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorID    *string         `json:"actorId"`
	ActorEmail *string         `json:"actorEmail"`
	ActorIP    *string         `json:"actorIp"`
	Action     string          `json:"action"`
	Entity     string          `json:"entity"`
	EntityID   string          `json:"entityId"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	RequestID  *string         `json:"requestId"`
}

// Filter narrows List.
type Filter struct {
	Entity   string
	EntityID string
	ActorID  string
	Action   string
	Cursor   string // id to continue before
	Limit    int
}

// List returns entries newest first for a tenant (plus global entries such as year table changes).
func List(ctx context.Context, q store.Querier, tenantID string, f Filter) ([]Entry, string, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var before int64
	if f.Cursor != "" {
		n, err := strconv.ParseInt(f.Cursor, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		before = n
	}
	rows, err := q.Query(ctx, `
		SELECT a.id, a.at, a.actor_id::text, u.email, a.actor_ip, a.action, a.entity, a.entity_id,
		       a.before, a.after, a.request_id
		FROM audit_log a LEFT JOIN admin_users u ON u.id = a.actor_id
		WHERE (a.tenant_id = $1::uuid OR a.tenant_id IS NULL)
		  AND ($2 = '' OR a.entity = $2)
		  AND ($3 = '' OR a.entity_id = $3)
		  AND ($4 = '' OR a.actor_id = NULLIF($4,'')::uuid)
		  AND ($5 = '' OR a.action = $5)
		  AND ($6 = 0 OR a.id < $6)
		ORDER BY a.id DESC
		LIMIT $7`, tenantID, f.Entity, f.EntityID, f.ActorID, f.Action, before, f.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorEmail, &e.ActorIP, &e.Action, &e.Entity, &e.EntityID,
			&e.Before, &e.After, &e.RequestID); err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = strconv.FormatInt(out[len(out)-1].ID, 10)
	}
	return out, next, nil
}
