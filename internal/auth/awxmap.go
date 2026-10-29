package auth

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// AWX's organization and team maps (SOCIAL_AUTH_ORGANIZATION_MAP,
// SOCIAL_AUTH_TEAM_MAP and their per-provider versions), evaluated as AWX's
// social pipeline does:
//
//	{"Default": {"users": true},
//	 "Ops": {"admins": ["alice@example.com", "/^ops-lead-.*$/i"], "users": "/^[^@]+@example\.com$/", "remove_users": false}}
//	{"Web": {"organization": "Ops", "users": "/^web-/", "remove": true}}
//
// An expression is null (leave the membership as it is), true (everyone),
// false (no one), or one or more strings: an exact username or email, or
// "/pattern/flags" (flags i and m), matched from the start like Python's
// re.match. A user an expression does not match is removed, unless the
// entry's remove option is false.

// mapExpr is one parsed expression of an organization or team map.
type mapExpr struct {
	set      bool // false: null, no change
	all      bool
	literals []string
	regexes  []*regexp.Regexp
}

// Matcher tells whether a user matches a literal or a regex of a map: social
// logins compare the username and email; LDAP compares group DNs.
type Matcher func(literal string, re *regexp.Regexp) bool

// IdentityMatcher is AWX's social match: the username or email equals the
// literal, or the regex matches either from the start.
func IdentityMatcher(username, email string) Matcher {
	return func(lit string, re *regexp.Regexp) bool {
		if re != nil {
			return re.MatchString(username) || re.MatchString(email)
		}
		return lit == username || lit == email
	}
}

func (e mapExpr) matches(m Matcher) bool {
	if e.all {
		return true
	}
	for _, l := range e.literals {
		if m(l, nil) {
			return true
		}
	}
	for _, re := range e.regexes {
		if m("", re) {
			return true
		}
	}
	return false
}

// DRF's boolean and null spellings, which AWX's map fields accept.
var (
	trueValues  = map[string]bool{"t": true, "T": true, "y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "true": true, "True": true, "TRUE": true, "on": true, "On": true, "ON": true, "1": true}
	falseValues = map[string]bool{"f": true, "F": true, "n": true, "N": true, "no": true, "No": true, "NO": true, "false": true, "False": true, "FALSE": true, "off": true, "Off": true, "OFF": true, "0": true}
	nullValues  = map[string]bool{"null": true, "Null": true, "NULL": true, "": true}
	mapRegexRe  = regexp.MustCompile(`^/(.*)/([im]+)?$`)
)

func parseExpr(v any) (mapExpr, error) {
	switch x := v.(type) {
	case nil:
		return mapExpr{}, nil
	case bool:
		return mapExpr{set: true, all: x}, nil
	case float64:
		if x == 1 || x == 0 {
			return mapExpr{set: true, all: x == 1}, nil
		}
	case string:
		switch {
		case trueValues[x]:
			return mapExpr{set: true, all: true}, nil
		case falseValues[x]:
			return mapExpr{set: true}, nil
		case nullValues[x]:
			return mapExpr{}, nil
		}
		e := mapExpr{set: true}
		return e, e.add(x)
	case []any:
		e := mapExpr{set: true}
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				if f, isNum := item.(float64); isNum {
					s = fmt.Sprint(f)
				} else {
					return mapExpr{}, fmt.Errorf("Expected None, True, False, a string or list of strings but got %s instead.", pyType(item))
				}
			}
			if err := e.add(s); err != nil {
				return mapExpr{}, err
			}
		}
		return e, nil
	}
	return mapExpr{}, fmt.Errorf("Expected None, True, False, a string or list of strings but got %s instead.", pyType(v))
}

// add appends a literal, or a regex written /pattern/flags.
func (e *mapExpr) add(s string) error {
	m := mapRegexRe.FindStringSubmatch(s)
	if m == nil {
		e.literals = append(e.literals, s)
		return nil
	}
	flags := ""
	if strings.Contains(m[2], "i") {
		flags += "i"
	}
	if strings.Contains(m[2], "m") {
		flags += "m"
	}
	// Python's re.match anchors at the start only; \Z is Go's \z.
	pattern := strings.ReplaceAll(m[1], `\Z`, `\z`)
	if flags != "" {
		pattern = "(?" + flags + ")" + pattern
	}
	re, err := regexp.Compile(`^(?:` + pattern + `)`)
	if err != nil {
		return fmt.Errorf("%v: %s", err, s)
	}
	e.regexes = append(e.regexes, re)
	return nil
}

// pyName is the Python type name of a decoded JSON value, for AWX's messages.
func pyName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "dict"
	case []any:
		return "list"
	case float64:
		return "float"
	case string:
		return "str"
	case bool:
		return "bool"
	}
	return "NoneType"
}

func pyType(v any) string { return "<class '" + pyName(v) + "'>" }

// orgEntry is one organization of an organization map.
type orgEntry struct {
	name, alias               string
	admins, users             mapExpr
	removeAdmins, removeUsers bool
}

// teamEntry is one team of a team map.
type teamEntry struct {
	name, organization string
	users              mapExpr
	remove             bool
}

// AWXMaps is a parsed pair of AWX organization and team maps.
type AWXMaps struct {
	Orgs  []orgEntry
	Teams []teamEntry
}

// Empty reports whether no map is configured.
func (m AWXMaps) Empty() bool { return len(m.Orgs) == 0 && len(m.Teams) == 0 }

func asObject(v any, what string) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("Expected a dictionary of items but got type \"%s\".", pyName(v))
	}
	return obj, nil
}

func checkKeys(entry map[string]any, allowed ...string) error {
	var bad []string
	for k := range entry {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("Invalid key(s): %s.", `"`+strings.Join(bad, `", "`)+`"`)
	}
	return nil
}

func optBool(entry map[string]any, key string, def bool) (bool, error) {
	v, ok := entry[key]
	if !ok || v == nil {
		return def, nil
	}
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if trueValues[x] {
			return true, nil
		}
		if falseValues[x] {
			return false, nil
		}
	case float64:
		if x == 1 || x == 0 {
			return x == 1, nil
		}
	}
	return false, fmt.Errorf("%s: Must be a valid boolean.", key)
}

// ParseOrgMap reads an AWX organization map, with AWX's validation: each
// organization takes admins, users, remove_admins, remove_users and
// organization_alias.
func ParseOrgMap(v any) ([]orgEntry, error) {
	obj, err := asObject(v, "organization map")
	if err != nil {
		return nil, err
	}
	names := sortedKeys(obj)
	out := make([]orgEntry, 0, len(names))
	for _, name := range names {
		entry, ok := obj[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: Expected a dictionary of items but got type \"%s\".", name, pyName(obj[name]))
		}
		if err := checkKeys(entry, "admins", "users", "remove_admins", "remove_users", "organization_alias"); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		o := orgEntry{name: name}
		if o.admins, err = parseExpr(entry["admins"]); err != nil {
			return nil, fmt.Errorf("%s: admins: %v", name, err)
		}
		if o.users, err = parseExpr(entry["users"]); err != nil {
			return nil, fmt.Errorf("%s: users: %v", name, err)
		}
		// AWX's pipeline defaults both to its "remove" option, which the
		// field does not accept, so to true.
		if o.removeAdmins, err = optBool(entry, "remove_admins", true); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if o.removeUsers, err = optBool(entry, "remove_users", true); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if a, ok := entry["organization_alias"].(string); ok && !nullValues[a] {
			o.alias = a
		}
		out = append(out, o)
	}
	return out, nil
}

// ParseTeamMap reads an AWX team map: each team takes organization
// (required), users and remove.
func ParseTeamMap(v any) ([]teamEntry, error) {
	obj, err := asObject(v, "team map")
	if err != nil {
		return nil, err
	}
	names := sortedKeys(obj)
	out := make([]teamEntry, 0, len(names))
	for _, name := range names {
		entry, ok := obj[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: Expected a dictionary of items but got type \"%s\".", name, pyName(obj[name]))
		}
		if err := checkKeys(entry, "organization", "users", "remove"); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		org, _ := entry["organization"].(string)
		if org == "" {
			return nil, fmt.Errorf("%s: organization: This field is required.", name)
		}
		t := teamEntry{name: name, organization: org}
		if t.users, err = parseExpr(entry["users"]); err != nil {
			return nil, fmt.Errorf("%s: users: %v", name, err)
		}
		if t.remove, err = optBool(entry, "remove", true); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Evaluate applies the maps: a matched expression grants its role, an
// unmatched one revokes it unless its remove option is false, and a null
// one leaves it as it is. Like AWX, a team map makes the user a member of
// the team only, not of its organization.
func (m AWXMaps) Evaluate(match Matcher, res *MappingResult) {
	decide := func(e mapExpr, remove bool, g Grant) {
		switch {
		case !e.set:
			res.Keep = append(res.Keep, g)
		case e.matches(match):
			res.Grants = append(res.Grants, g)
		case !remove:
			res.Keep = append(res.Keep, g)
		}
	}
	for _, o := range m.Orgs {
		org := o.name
		if o.alias != "" {
			org = o.alias
		}
		from := fmt.Sprintf("organization map %q", o.name)
		decide(o.admins, o.removeAdmins, Grant{RoleDefinition: "Organization Admin", Organization: org, Map: from + " admins", Create: true})
		decide(o.users, o.removeUsers, Grant{RoleDefinition: "Organization Member", Organization: org, Map: from + " users", Create: true})
	}
	for _, t := range m.Teams {
		decide(t.users, t.remove, Grant{RoleDefinition: "Team Member", Organization: t.organization, Team: t.name,
			Map: fmt.Sprintf("team map %q users", t.name), Create: true})
	}
}

// providerMaps are the logins with their own maps in AWX 24.6. As AWX's
// settings return None rather than nothing for them, their maps replace
// the global ones even when unset; other logins (OIDC, LDAP has its own)
// use SOCIAL_AUTH_ORGANIZATION_MAP and SOCIAL_AUTH_TEAM_MAP.
var providerMaps = map[string]string{
	"google-oauth2":          "SOCIAL_AUTH_GOOGLE_OAUTH2_",
	"azuread-oauth2":         "SOCIAL_AUTH_AZUREAD_OAUTH2_",
	"github":                 "SOCIAL_AUTH_GITHUB_",
	"github-org":             "SOCIAL_AUTH_GITHUB_ORG_",
	"github-team":            "SOCIAL_AUTH_GITHUB_TEAM_",
	"github-enterprise":      "SOCIAL_AUTH_GITHUB_ENTERPRISE_",
	"github-enterprise-org":  "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_",
	"github-enterprise-team": "SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_",
	"saml":                   "SOCIAL_AUTH_SAML_",
}

// MapSettings returns the organization and team map settings a login uses.
func MapSettings(provider string) (org, team string) {
	prefix := "SOCIAL_AUTH_"
	if p, ok := providerMaps[provider]; ok {
		prefix = p
	}
	return prefix + "ORGANIZATION_MAP", prefix + "TEAM_MAP"
}

// LoadAWXMaps reads the maps a login uses with get, a setting lookup.
func LoadAWXMaps(get func(key string) any, provider string) (AWXMaps, error) {
	orgKey, teamKey := MapSettings(provider)
	orgs, err := ParseOrgMap(get(orgKey))
	if err != nil {
		return AWXMaps{}, fmt.Errorf("%s: %v", orgKey, err)
	}
	teams, err := ParseTeamMap(get(teamKey))
	if err != nil {
		return AWXMaps{}, fmt.Errorf("%s: %v", teamKey, err)
	}
	return AWXMaps{Orgs: orgs, Teams: teams}, nil
}
