package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
)

// Rule maps an identity-provider group to access.
//
//	{"group": "awx-admins", "superuser": true}
//	{"group": "auditors", "auditor": true}
//	{"group": "ops", "organization": "Ops", "role": "member"}
//	{"group_regex": "^team-(.+)$", "organization": "Ops", "team": "$1", "role": "member"}
//	{"group": "deployers", "organization": "Ops", "role_definition": "Organization Execute"}
type Rule struct {
	Group          string `json:"group,omitempty"`
	GroupRegex     string `json:"group_regex,omitempty"`
	Superuser      bool   `json:"superuser,omitempty"`
	Auditor        bool   `json:"auditor,omitempty"`
	Organization   string `json:"organization,omitempty"`
	Team           string `json:"team,omitempty"`
	Role           string `json:"role,omitempty"`
	RoleDefinition string `json:"role_definition,omitempty"`
	// Create missing organizations/teams (default true).
	Create *bool `json:"create,omitempty"`

	re *regexp.Regexp
}

// Grant is a resolved role assignment produced by the mapping.
type Grant struct {
	RoleDefinition string `json:"role_definition"`
	Organization   string `json:"organization"`
	Team           string `json:"team,omitempty"`
	Group          string `json:"matched_group"`
	Create         bool   `json:"-"`
}

// MappingResult is the outcome of evaluating rules against a user's groups.
type MappingResult struct {
	Superuser *bool    `json:"superuser"`
	Auditor   *bool    `json:"auditor"`
	Grants    []Grant  `json:"grants"`
	Errors    []string `json:"errors"`
}

var orgRoles = map[string]string{"member": "Organization Member", "admin": "Organization Admin", "auditor": "Organization Audit"}
var teamRoles = map[string]string{"member": "Team Member", "admin": "Team Admin"}

// ParseRules decodes and validates mapping rules from a setting value.
func ParseRules(v any) ([]Rule, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var rules []Rule
	if string(b) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(b, &rules); err != nil {
		return nil, fmt.Errorf("mapping must be a list of rule objects: %w", err)
	}
	for i := range rules {
		r := &rules[i]
		if (r.Group == "") == (r.GroupRegex == "") {
			return nil, fmt.Errorf("rule %d: exactly one of group or group_regex is required", i+1)
		}
		if r.GroupRegex != "" {
			re, err := regexp.Compile(r.GroupRegex)
			if err != nil {
				return nil, fmt.Errorf("rule %d: invalid group_regex: %w", i+1, err)
			}
			r.re = re
		}
		if !r.Superuser && !r.Auditor && r.Organization == "" {
			return nil, fmt.Errorf("rule %d: must grant superuser, auditor or an organization role", i+1)
		}
		if r.Team != "" && r.Organization == "" {
			return nil, fmt.Errorf("rule %d: team requires organization", i+1)
		}
		if r.Organization != "" && r.RoleDefinition == "" {
			role := r.Role
			if role == "" {
				role = "member"
			}
			table := orgRoles
			if r.Team != "" {
				table = teamRoles
			}
			if _, ok := table[role]; !ok {
				return nil, fmt.Errorf("rule %d: invalid role %q", i+1, r.Role)
			}
		}
	}
	return rules, nil
}

// Evaluate applies rules to groups. It is pure so it can back the dry-run endpoint.
func Evaluate(rules []Rule, groups []string) MappingResult {
	res := MappingResult{Grants: []Grant{}, Errors: []string{}}
	var hasSU, hasAud bool
	su, aud := false, false
	seen := map[string]bool{}
	add := func(g Grant) {
		k := g.RoleDefinition + "|" + g.Organization + "|" + g.Team
		if !seen[k] {
			seen[k] = true
			res.Grants = append(res.Grants, g)
		}
	}
	for _, r := range rules {
		if r.Superuser {
			hasSU = true
		}
		if r.Auditor {
			hasAud = true
		}
		for _, g := range groups {
			var sub []string
			if r.re != nil {
				sub = r.re.FindStringSubmatch(g)
				if sub == nil {
					continue
				}
			} else if r.Group != g {
				continue
			}
			expand := func(s string) string {
				for i := len(sub) - 1; i >= 1; i-- {
					s = strings.ReplaceAll(s, fmt.Sprintf("$%d", i), sub[i])
				}
				return s
			}
			if r.Superuser {
				su = true
			}
			if r.Auditor {
				aud = true
			}
			if r.Organization == "" {
				continue
			}
			create := r.Create == nil || *r.Create
			org, team := expand(r.Organization), expand(r.Team)
			role := r.Role
			if role == "" {
				role = "member"
			}
			if team != "" {
				rd := r.RoleDefinition
				if rd == "" {
					rd = teamRoles[role]
				}
				add(Grant{RoleDefinition: rd, Organization: org, Team: team, Group: g, Create: create})
				// Team members are organization members, like AWX.
				add(Grant{RoleDefinition: "Organization Member", Organization: org, Group: g, Create: create})
			} else {
				rd := r.RoleDefinition
				if rd == "" {
					rd = orgRoles[role]
				}
				add(Grant{RoleDefinition: rd, Organization: org, Group: g, Create: create})
			}
		}
	}
	if hasSU {
		res.Superuser = &su
	}
	if hasAud {
		res.Auditor = &aud
	}
	sort.Slice(res.Grants, func(i, j int) bool {
		a, b := res.Grants[i], res.Grants[j]
		return a.Organization+a.Team+a.RoleDefinition < b.Organization+b.Team+b.RoleDefinition
	})
	return res
}

// ApplyMapping makes the user's mapped (source='oidc') assignments match the result.
// Manual grants are never touched.
func ApplyMapping(ctx context.Context, tx api.DBTX, userID int64, res MappingResult) ([]string, error) {
	var warnings []string
	type key struct {
		rd  int64
		ct  string
		obj int64
	}
	desired := map[key]bool{}
	for _, g := range res.Grants {
		var rdID int64
		var rdCT *string
		if err := tx.QueryRow(ctx, "SELECT id, content_type FROM role_definitions WHERE name = $1", g.RoleDefinition).Scan(&rdID, &rdCT); err != nil {
			warnings = append(warnings, fmt.Sprintf("unknown role definition %q", g.RoleDefinition))
			continue
		}
		orgID, err := ensureNamed(ctx, tx, "organizations", "name = $1", []any{g.Organization},
			"INSERT INTO organizations (name) VALUES ($1) ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id", g.Create)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("organization %q: %v", g.Organization, err))
			continue
		}
		ct, obj := "organization", orgID
		if g.Team != "" {
			teamID, err := ensureNamed(ctx, tx, "teams", "organization_id = $1 AND name = $2", []any{orgID, g.Team},
				"INSERT INTO teams (organization_id, name) VALUES ($1, $2) ON CONFLICT (organization_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id", g.Create)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("team %q in %q: %v", g.Team, g.Organization, err))
				continue
			}
			ct, obj = "team", teamID
		}
		if rdCT == nil || *rdCT != ct {
			warnings = append(warnings, fmt.Sprintf("role definition %q cannot be assigned on a %s", g.RoleDefinition, ct))
			continue
		}
		desired[key{rdID, ct, obj}] = true
	}
	rows, err := tx.Query(ctx, "SELECT id, role_definition_id, content_type, object_id FROM role_assignments WHERE user_id = $1 AND source = 'oidc'", userID)
	if err != nil {
		return nil, err
	}
	var stale []int64
	existing := map[key]bool{}
	for rows.Next() {
		var id, rd, obj int64
		var ct string
		if err := rows.Scan(&id, &rd, &ct, &obj); err != nil {
			rows.Close()
			return nil, err
		}
		k := key{rd, ct, obj}
		existing[k] = true
		if !desired[k] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if len(stale) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM role_assignments WHERE id = ANY($1)", stale); err != nil {
			return nil, err
		}
	}
	for k := range desired {
		if existing[k] {
			continue
		}
		// An identical manual grant wins (unique index); it is left as is.
		if _, err := tx.Exec(ctx, `INSERT INTO role_assignments (role_definition_id, user_id, content_type, object_id, source)
			VALUES ($1, $2, $3, $4, 'oidc') ON CONFLICT DO NOTHING`, k.rd, userID, k.ct, k.obj); err != nil {
			return nil, err
		}
	}
	if res.Superuser != nil {
		if _, err := tx.Exec(ctx, "UPDATE users SET is_superuser = $2 WHERE id = $1", userID, *res.Superuser); err != nil {
			return nil, err
		}
	}
	if res.Auditor != nil {
		if _, err := tx.Exec(ctx, "UPDATE users SET is_system_auditor = $2 WHERE id = $1", userID, *res.Auditor); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

func ensureNamed(ctx context.Context, tx api.DBTX, table, where string, args []any, insert string, create bool) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, "SELECT id FROM "+table+" WHERE "+where, args...).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !create {
		return 0, fmt.Errorf("does not exist")
	}
	if err := tx.QueryRow(ctx, insert, args...).Scan(&id); err != nil {
		return 0, err
	}
	ct := map[string]string{"organizations": "organization", "teams": "team"}[table]
	return id, rbac.EnsureObjectRoles(ctx, tx, ct, id)
}

// claimPath reads a possibly dotted claim (e.g. "realm_access.roles").
func claimPath(claims map[string]any, path string) any {
	if v, ok := claims[path]; ok {
		return v
	}
	var cur any = claims
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// claimStrings converts a claim to a list of strings (lists, or comma/space separated strings).
func claimStrings(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case []string:
		return x
	case string:
		return strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' })
	}
	return nil
}
