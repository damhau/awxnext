// Package rbac implements role-based access control modelled on
// django-ansible-base: role definitions are sets of permissions, assigned to
// users or teams on an object, an organization (applies to every child object
// of the organization) or globally.
package rbac

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Principal is the authenticated actor of a request.
type Principal struct {
	UserID    int64
	Username  string
	Superuser bool
	Auditor   bool
	Teams     []int64
	// TokenScope is "read" or "write" for token auth, "" otherwise.
	TokenScope string
}

func (p *Principal) Anonymous() bool { return p == nil || p.UserID == 0 }

// System returns a principal with unrestricted access, used by background tasks.
func System() *Principal { return &Principal{Superuser: true, Username: "system"} }

// LoadTeams resolves the teams a user is a member of.
func LoadTeams(ctx context.Context, db *pgxpool.Pool, userID int64) ([]int64, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT ra.object_id FROM role_assignments ra
		JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE ra.user_id = $1 AND ra.content_type = 'team' AND 'member_team' = ANY(rd.permissions)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ArgFunc appends a query argument and returns its placeholder.
type ArgFunc func(v any) string

// Predicate returns a SQL boolean expression that is true when the principal
// holds perm on the row identified by idExpr. orgExpr, when non-empty, is the
// SQL expression of the row's organization id so organization-level
// assignments apply.
func (p *Principal) Predicate(arg ArgFunc, contentType, perm, idExpr, orgExpr string) string {
	if p.Superuser {
		return "TRUE"
	}
	if p.Auditor && strings.HasPrefix(perm, "view_") {
		return "TRUE"
	}
	if p.Anonymous() {
		return "FALSE"
	}
	scope := fmt.Sprintf("(ra.content_type = %s AND ra.object_id = %s)", arg(contentType), idExpr)
	if orgExpr != "" {
		scope += fmt.Sprintf(" OR (ra.content_type = 'organization' AND ra.object_id = %s)", orgExpr)
	}
	scope += " OR ra.content_type IS NULL"
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND %s = ANY(rd.permissions) AND (%s))`,
		arg(p.UserID), arg(p.teamsArg()), arg(perm), scope)
}

func (p *Principal) teamsArg() []int64 {
	if p.Teams == nil {
		return []int64{}
	}
	return p.Teams
}

// Has reports whether the principal holds perm on a specific object.
func (p *Principal) Has(ctx context.Context, db *pgxpool.Pool, contentType, perm string, objectID int64, orgID *int64) (bool, error) {
	if p.Superuser || (p.Auditor && strings.HasPrefix(perm, "view_")) {
		return true, nil
	}
	if p.Anonymous() {
		return false, nil
	}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	org := ""
	if orgID != nil {
		org = arg(*orgID)
	}
	pred := p.Predicate(arg, contentType, perm, arg(objectID), org)
	var ok bool
	err := db.QueryRow(ctx, "SELECT "+pred, args...).Scan(&ok)
	return ok, err
}

// HasOrg reports whether the principal holds perm through an assignment on the organization.
func (p *Principal) HasOrg(ctx context.Context, db *pgxpool.Pool, perm string, orgID int64) (bool, error) {
	return p.Has(ctx, db, "organization", perm, orgID, &orgID)
}

// PermsFor returns, for each object id, the set of permissions the principal holds.
func (p *Principal) PermsFor(ctx context.Context, db *pgxpool.Pool, table, contentType, orgColumn string, ids []int64) (map[int64]map[string]bool, error) {
	out := map[int64]map[string]bool{}
	if len(ids) == 0 || p.Anonymous() {
		return out, nil
	}
	orgJoin := ""
	if orgColumn != "" {
		orgJoin = fmt.Sprintf(" OR (ra.content_type = 'organization' AND ra.object_id = t.%s)", orgColumn)
	}
	q := fmt.Sprintf(`SELECT t.id, array_agg(DISTINCT perm) FROM %s t
		JOIN role_assignments ra ON ((ra.content_type = $1 AND ra.object_id = t.id)%s OR ra.content_type IS NULL)
		JOIN role_definitions rd ON rd.id = ra.role_definition_id
		CROSS JOIN LATERAL unnest(rd.permissions) perm
		WHERE t.id = ANY($2) AND (ra.user_id = $3 OR ra.team_id = ANY($4))
		GROUP BY t.id`, table, orgJoin)
	rows, err := db.Query(ctx, q, contentType, ids, p.UserID, p.teamsArg())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var perms []string
		if err := rows.Scan(&id, &perms); err != nil {
			return nil, err
		}
		m := map[string]bool{}
		for _, x := range perms {
			m[x] = true
		}
		out[id] = m
	}
	return out, rows.Err()
}

// Execer is satisfied by *pgxpool.Pool, pgx.Tx and *pgx.Conn.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
