package settings

// LDAPServers are the prefixes of AWX's six LDAP servers, tried in this order.
var LDAPServers = []string{"AUTH_LDAP_", "AUTH_LDAP_1_", "AUTH_LDAP_2_", "AUTH_LDAP_3_", "AUTH_LDAP_4_", "AUTH_LDAP_5_"}

// LDAPGroupTypes are django-auth-ldap's group types and AWX's
// PosixUIDGroupType, with the parameters each takes.
var LDAPGroupTypes = map[string][]string{
	"PosixGroupType":                    {"name_attr"},
	"PosixUIDGroupType":                 {"name_attr", "ldap_group_user_attr"},
	"MemberDNGroupType":                 {"member_attr", "name_attr"},
	"NestedMemberDNGroupType":           {"member_attr", "name_attr"},
	"GroupOfNamesType":                  {"name_attr"},
	"NestedGroupOfNamesType":            {"name_attr"},
	"GroupOfUniqueNamesType":            {"name_attr"},
	"NestedGroupOfUniqueNamesType":      {"name_attr"},
	"ActiveDirectoryGroupType":          {"name_attr"},
	"NestedActiveDirectoryGroupType":    {"name_attr"},
	"OrganizationalRoleGroupType":       {"name_attr"},
	"NestedOrganizationalRoleGroupType": {"name_attr"},
}

func init() {
	var types [][]any
	for _, t := range []string{"PosixGroupType", "PosixUIDGroupType", "MemberDNGroupType", "NestedMemberDNGroupType", "GroupOfNamesType",
		"NestedGroupOfNamesType", "GroupOfUniqueNamesType", "NestedGroupOfUniqueNamesType", "ActiveDirectoryGroupType",
		"NestedActiveDirectoryGroupType", "OrganizationalRoleGroupType", "NestedOrganizationalRoleGroupType"} {
		types = append(types, []any{t, t})
	}
	for _, p := range LDAPServers {
		for _, d := range []*Def{
			{Key: p + "SERVER_URI", Type: "string", Default: "", Label: "LDAP Server URI",
				Help: `URI of the LDAP server, such as "ldap://ldap.example.com:389" or "ldaps://ldap.example.com:636". Several servers, separated by spaces or commas, are tried in order. Empty turns this LDAP login off.`},
			{Key: p + "BIND_DN", Type: "string", Default: "", Label: "LDAP Bind DN", Help: "DN of the account used for every search. Empty binds anonymously."},
			{Key: p + "BIND_PASSWORD", Type: "string", Default: "", Secret: true, Label: "LDAP Bind Password"},
			{Key: p + "START_TLS", Type: "boolean", Default: false, Label: "LDAP Start TLS", Help: "Upgrade ldap:// connections with StartTLS."},
			{Key: p + "CONNECTION_OPTIONS", Type: "nested object", Default: map[string]any{"OPT_REFERRALS": 0, "OPT_NETWORK_TIMEOUT": 30}, Label: "LDAP Connection Options",
				Help: "python-ldap options. Used: OPT_NETWORK_TIMEOUT (seconds), OPT_X_TLS_REQUIRE_CERT (0 does not verify the certificate) and OPT_X_TLS_CACERTFILE; referrals are never followed, others have no effect."},
			{Key: p + "USER_SEARCH", Type: "list", Default: []any{}, Label: "LDAP User Search",
				Help: `How to find a user: ["OU=Users,DC=example,DC=com", "SCOPE_SUBTREE", "(sAMAccountName=%(user)s)"], or a list of such searches, which must find exactly one user between them.`},
			{Key: p + "USER_DN_TEMPLATE", Type: "string", Default: nil, Nullable: true, Label: "LDAP User DN Template",
				Help: `The user's DN, such as "uid=%(user)s,OU=Users,DC=example,DC=com". Used instead of the user search when set.`},
			{Key: p + "USER_ATTR_MAP", Type: "nested object", Default: map[string]any{}, Label: "LDAP User Attribute Map",
				Help: `User fields read from LDAP at each login: {"first_name": "givenName", "last_name": "sn", "email": "mail"}.`},
			{Key: p + "GROUP_SEARCH", Type: "list", Default: []any{}, Label: "LDAP Group Search",
				Help: `Where the user's groups are: ["DC=example,DC=com", "SCOPE_SUBTREE", "(objectClass=group)"]. Organization and team maps only see groups this search finds.`},
			{Key: p + "GROUP_TYPE", Type: "choice", Default: "MemberDNGroupType", Choices: types, Label: "LDAP Group Type",
				Help: "How group membership is stored. Nested types follow groups in groups."},
			{Key: p + "GROUP_TYPE_PARAMS", Type: "nested object", Default: map[string]any{"member_attr": "member", "name_attr": "cn"}, Label: "LDAP Group Type Parameters",
				Help: "Parameters of the group type, such as member_attr for MemberDNGroupType."},
			{Key: p + "REQUIRE_GROUP", Type: "string", Default: nil, Nullable: true, Label: "LDAP Require Group", Help: "DN of a group users must be in to log in."},
			{Key: p + "DENY_GROUP", Type: "string", Default: nil, Nullable: true, Label: "LDAP Deny Group", Help: "DN of a group whose members cannot log in."},
			{Key: p + "USER_FLAGS_BY_GROUP", Type: "nested object", Default: map[string]any{}, Label: "LDAP User Flags By Group",
				Help: `Superusers and system auditors by group, set at each login: {"is_superuser": ["CN=Admins,OU=Groups,DC=example,DC=com"], "is_system_auditor": "CN=Auditors,OU=Groups,DC=example,DC=com"}.`},
			{Key: p + "ORGANIZATION_MAP", Type: "nested object", Default: map[string]any{}, Label: "LDAP Organization Map",
				Help: `Organizations a user joins by group, at each login: {"Ops": {"admins": "CN=Ops Admins,DC=example,DC=com", "auditors": [...], "users": true, "remove_users": false}}. Each of admins, auditors and users is null (no change), true, false, or group DNs; an unmatched user is removed unless remove_admins, remove_auditors or remove_users is false.`},
			{Key: p + "TEAM_MAP", Type: "nested object", Default: map[string]any{}, Label: "LDAP Team Map",
				Help: `Teams a user joins by group: {"Web": {"organization": "Ops", "users": "CN=Web,DC=example,DC=com", "remove": true}}.`},
		} {
			d.Category = "ldap"
			Register(d)
		}
	}
}
