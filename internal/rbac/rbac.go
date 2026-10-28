// Package rbac implements role-based access control modelled on
// django-ansible-base: role definitions are sets of permissions, assigned to
// users or teams on an object, a folder (applies to everything below it), an
// organization (applies to every child object of the organization) or globally.
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
	// Teams the user belongs to, directly or through nested teams.
	Teams []int64
	// TokenScope is "read" or "write" for token auth, "" otherwise.
	TokenScope string
}

func (p *Principal) Anonymous() bool { return p == nil || p.UserID == 0 }

// System returns a principal with unrestricted access, used by background tasks.
func System() *Principal { return &Principal{Superuser: true, Username: "system"} }

// LoadTeams resolves the teams a user belongs to, following nested team membership.
func LoadTeams(ctx context.Context, db *pgxpool.Pool, userID int64) ([]int64, error) {
	rows, err := db.Query(ctx, "SELECT user_teams($1)", userID)
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

// Scope locates an object for permission checks. Each field is a SQL expression;
// Org and Folder are optional.
type Scope struct {
	ID     string
	Org    string
	Folder string
}

// ScopeSQL matches assignments (alias ra) that apply to the scoped object.
func ScopeSQL(arg ArgFunc, contentType string, s Scope) string {
	parts := []string{fmt.Sprintf("(ra.content_type = %s AND ra.object_id = %s)", arg(contentType), s.ID)}
	if s.Org != "" {
		parts = append(parts, fmt.Sprintf("(ra.content_type = 'organization' AND ra.object_id = %s)", s.Org))
	}
	if s.Folder != "" {
		parts = append(parts, fmt.Sprintf("(ra.content_type = 'folder' AND ra.object_id IN (SELECT unnest(f.ancestors) FROM folders f WHERE f.id = %s))", s.Folder))
	}
	parts = append(parts, "ra.content_type IS NULL")
	return strings.Join(parts, " OR ")
}

// Predicate returns a SQL boolean expression that is true when the principal
// holds perm on the scoped object.
func (p *Principal) Predicate(arg ArgFunc, contentType, perm string, s Scope) string {
	if p.Superuser {
		return "TRUE"
	}
	if p.Auditor && strings.HasPrefix(perm, "view_") {
		return "TRUE"
	}
	if p.Anonymous() {
		return "FALSE"
	}
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND %s = ANY(rd.permissions) AND (%s))`,
		arg(p.UserID), arg(p.teamsArg()), arg(perm), ScopeSQL(arg, contentType, s))
}

// PredicateAll is true when the principal holds every one of perms on the
// scoped object, through any of its assignments.
func (p *Principal) PredicateAll(arg ArgFunc, contentType string, perms []string, s Scope) string {
	if p.Superuser {
		return "TRUE"
	}
	if p.Anonymous() {
		return "FALSE"
	}
	need := perms
	if p.Auditor {
		need = nil
		for _, perm := range perms {
			if !strings.HasPrefix(perm, "view_") {
				need = append(need, perm)
			}
		}
		if len(need) == 0 {
			return "TRUE"
		}
	}
	return fmt.Sprintf(`%s::text[] <@ ARRAY(SELECT unnest(rd.permissions) FROM role_assignments ra JOIN role_definitions rd ON rd.id = ra.role_definition_id
		WHERE (ra.user_id = %s OR ra.team_id = ANY(%s)) AND (%s))`,
		arg(need), arg(p.UserID), arg(p.teamsArg()), ScopeSQL(arg, contentType, s))
}

func (p *Principal) teamsArg() []int64 {
	if p.Teams == nil {
		return []int64{}
	}
	return p.Teams
}

// Has reports whether the principal holds perm on a specific object.
// orgID and folderID locate the object for organization and folder assignments.
func (p *Principal) Has(ctx context.Context, db *pgxpool.Pool, contentType, perm string, objectID int64, orgID, folderID *int64) (bool, error) {
	if p.Superuser || (p.Auditor && strings.HasPrefix(perm, "view_")) {
		return true, nil
	}
	if p.Anonymous() {
		return false, nil
	}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	s := Scope{ID: arg(objectID)}
	if orgID != nil {
		s.Org = arg(*orgID)
	}
	if folderID != nil {
		s.Folder = arg(*folderID)
	}
	var ok bool
	err := db.QueryRow(ctx, "SELECT "+p.Predicate(arg, contentType, perm, s), args...).Scan(&ok)
	return ok, err
}

// HasOrg reports whether the principal holds perm through an assignment on the organization.
func (p *Principal) HasOrg(ctx context.Context, db *pgxpool.Pool, perm string, orgID int64) (bool, error) {
	return p.Has(ctx, db, "organization", perm, orgID, &orgID, nil)
}

// HasFolder reports whether the principal holds perm on a folder (directly, via an
// ancestor folder, or via the folder's organization).
func (p *Principal) HasFolder(ctx context.Context, db *pgxpool.Pool, perm string, folderID, orgID int64) (bool, error) {
	return p.Has(ctx, db, "folder", perm, folderID, &orgID, &folderID)
}

// PermsFor returns, for each object id, the set of permissions the principal holds.
func (p *Principal) PermsFor(ctx context.Context, db *pgxpool.Pool, table, contentType, orgColumn, folderColumn string, ids []int64) (map[int64]map[string]bool, error) {
	out := map[int64]map[string]bool{}
	if len(ids) == 0 || p.Anonymous() {
		return out, nil
	}
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	s := Scope{ID: "t.id"}
	if orgColumn != "" {
		s.Org = "t." + orgColumn
	}
	if folderColumn != "" {
		s.Folder = "t." + folderColumn
	}
	q := fmt.Sprintf(`SELECT t.id, array_agg(DISTINCT perm) FROM %s t
		JOIN role_assignments ra ON (%s)
		JOIN role_definitions rd ON rd.id = ra.role_definition_id
		CROSS JOIN LATERAL unnest(rd.permissions) perm
		WHERE t.id = ANY(%s) AND (ra.user_id = %s OR ra.team_id = ANY(%s))
		GROUP BY t.id`, table, ScopeSQL(arg, contentType, s), arg(ids), arg(p.UserID), arg(p.teamsArg()))
	rows, err := db.Query(ctx, q, args...)
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
