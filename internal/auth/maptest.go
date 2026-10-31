package auth

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/settings"
)

// The map tester, POST /api/v2/settings/maps/test/: one login's
// organization and team maps (with LDAP's user flags, and SAML's
// attributes and flags) evaluated against a sample user by the code the
// login runs. Nothing is saved or changed. Values not saved yet may be
// given, so the settings page tests what it shows.

// mapTestLogin is a login whose maps can be tested: how it matches a user,
// and the settings it reads.
type mapTestLogin struct {
	kind string // social (username and email), ldap (group DNs) or saml (attributes)
	keys []string
}

// mapTestLogins are the logins by name: "default" for the maps OIDC and the
// logins without maps of their own use, AWX's social backend names, and
// ldap, ldap1 to ldap5 for the LDAP servers.
func mapTestLogins() map[string]mapTestLogin {
	out := map[string]mapTestLogin{"default": {"social", []string{"SOCIAL_AUTH_ORGANIZATION_MAP", "SOCIAL_AUTH_TEAM_MAP"}}}
	for name, prefix := range providerMaps {
		l := mapTestLogin{"social", []string{prefix + "ORGANIZATION_MAP", prefix + "TEAM_MAP"}}
		if name == "saml" {
			l = mapTestLogin{"saml", append(l.keys, prefix+"ORGANIZATION_ATTR", prefix+"TEAM_ATTR", prefix+"USER_FLAGS_BY_ATTR")}
		}
		out[name] = l
	}
	for i, prefix := range settings.LDAPServers {
		name := "ldap"
		if i > 0 {
			name = fmt.Sprintf("ldap%d", i)
		}
		out[name] = mapTestLogin{"ldap", []string{prefix + "ORGANIZATION_MAP", prefix + "TEAM_MAP", prefix + "USER_FLAGS_BY_GROUP"}}
	}
	return out
}

func (a *Auth) mapTest(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	if !c.User.Superuser {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	data, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	logins := mapTestLogins()
	name, _ := data["login"].(string)
	login, ok := logins[name]
	if !ok {
		names := make([]string, 0, len(logins))
		for n := range logins {
			names = append(names, n)
		}
		sort.Strings(names)
		api.WriteError(w, api.FieldError("login", "Choose one of "+strings.Join(names, ", ")+"."))
		return
	}
	username, _ := data["username"].(string)
	email, _ := data["email"].(string)
	if username = strings.TrimSpace(username); username == "" {
		api.WriteError(w, api.FieldError("username", "This field may not be blank."))
		return
	}
	// The values not saved yet, checked as saving them would be.
	given := map[string]any{}
	if v, ok := data["settings"]; ok && v != nil {
		if given, ok = normalizeJSON(v).(map[string]any); !ok {
			api.WriteError(w, api.FieldError("settings", "Expected an object of setting values."))
			return
		}
	}
	for k, v := range given {
		if !slices.Contains(login.keys, k) {
			api.WriteError(w, api.FieldError("settings", fmt.Sprintf("%s is not a map setting of %s.", k, name)))
			return
		}
		if d := settings.Lookup(k); d != nil && d.Validate != nil && v != nil {
			if err := d.Validate(v); err != nil {
				api.WriteError(w, api.FieldError(k, err.Error()))
				return
			}
		}
	}
	get := func(k string) any {
		if v, ok := given[k]; ok {
			return v
		}
		return c.App.Settings.Get(c, k)
	}
	// An existing user keeps its email unless one is given, and its flags
	// count where a map leaves them as they are (SAML's remove options).
	var known string
	var superuser, auditor bool
	c.App.DB.QueryRow(c, "SELECT email, is_superuser, is_system_auditor FROM users WHERE username = $1", username).Scan(&known, &superuser, &auditor)
	if email = strings.TrimSpace(email); email == "" {
		email = known
	}
	out := api.Obj{"login": name, "username": username, "email": email}
	bad := func(key string, err error) {
		api.WriteError(w, api.FieldError(key, err.Error()))
	}
	var res MappingResult
	switch login.kind {
	case "social":
		maps, err := LoadAWXMaps(get, name)
		if err != nil {
			api.WriteError(w, api.BadRequest(err.Error()))
			return
		}
		res = Mapping{AWX: maps}.Evaluate(Identity{Username: username, Email: email})
	case "ldap":
		orgKey, teamKey, flagsKey := login.keys[0], login.keys[1], login.keys[2]
		var maps AWXMaps
		if maps.Orgs, err = ParseLDAPOrgMap(get(orgKey)); err != nil {
			bad(orgKey, err)
			return
		}
		if maps.Teams, err = ParseLDAPTeamMap(get(teamKey)); err != nil {
			bad(teamKey, err)
			return
		}
		flags, err := parseLDAPFlags(get(flagsKey))
		if err != nil {
			bad(flagsKey, err)
			return
		}
		groups := stringList(data["groups"])
		in := func(dn string) bool {
			return slices.ContainsFunc(groups, func(g string) bool { return sameDN(g, dn) })
		}
		values, _ := ldapFlagValues(flags, func(dn string) (bool, error) { return in(dn), nil })
		res = ldapResult(maps, values, in)
		out["groups"] = groups
	case "saml":
		cfg := &samlConfig{}
		if cfg.orgMap, err = ParseOrgMap(get(login.keys[0])); err != nil {
			bad(login.keys[0], err)
			return
		}
		if cfg.teamMap, err = ParseTeamMap(get(login.keys[1])); err != nil {
			bad(login.keys[1], err)
			return
		}
		cfg.orgAttr, _ = get(login.keys[2]).(map[string]any)
		cfg.teamAttr, _ = get(login.keys[3]).(map[string]any)
		cfg.flags, _ = get(login.keys[4]).(map[string]any)
		attrs := map[string][]string{}
		raw, _ := normalizeJSON(data["attributes"]).(map[string]any)
		for k, v := range raw {
			if vals := stringList(v); len(vals) > 0 {
				attrs[k] = vals
			}
		}
		res = samlMapping(cfg, username, email, attrs, superuser, auditor)
		out["attributes"] = attrs
	}
	// A role granted is not also removed (OIDC rules can grant what a map
	// does not).
	removed := []Grant{}
	for _, g := range res.Revoke {
		if !slices.ContainsFunc(res.Grants, func(x Grant) bool {
			return x.RoleDefinition == g.RoleDefinition && x.Organization == g.Organization && x.Team == g.Team
		}) {
			removed = append(removed, g)
		}
	}
	out["superuser"], out["auditor"] = res.Superuser, res.Auditor
	out["grants"] = nonNil(res.Grants)
	out["kept"] = nonNil(res.Keep)
	out["removed"] = removed
	out["would_create"] = wouldCreate(c, res.Grants)
	out["errors"] = nonNilStrings(res.Errors)
	api.WriteJSON(w, 200, out)
}

// stringList reads a string or a list of strings, without blanks.
func stringList(v any) []string {
	out := []string{}
	add := func(x any) {
		if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	if l, ok := v.([]any); ok {
		for _, x := range l {
			add(x)
		}
	} else {
		add(v)
	}
	return out
}

func nonNil(g []Grant) []Grant {
	if g == nil {
		return []Grant{}
	}
	return g
}
