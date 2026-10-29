package settings

// GitHubVariant is one of AWX's six GitHub logins.
type GitHubVariant struct {
	Backend, Prefix, Category, Label string
	Enterprise                       bool
	// Member is "NAME" (an organization) or "ID" (a team) to check.
	Member string
}

// GitHubVariants are AWX's GitHub logins, by python-social-auth backend.
var GitHubVariants = []GitHubVariant{
	{"github", "SOCIAL_AUTH_GITHUB_", "github", "GitHub", false, ""},
	{"github-org", "SOCIAL_AUTH_GITHUB_ORG_", "github-org", "GitHub Organization", false, "NAME"},
	{"github-team", "SOCIAL_AUTH_GITHUB_TEAM_", "github-team", "GitHub Team", false, "ID"},
	{"github-enterprise", "SOCIAL_AUTH_GITHUB_ENTERPRISE_", "github-enterprise", "GitHub Enterprise", true, ""},
	{"github-enterprise-org", "SOCIAL_AUTH_GITHUB_ENTERPRISE_ORG_", "github-enterprise-org", "GitHub Enterprise Organization", true, "NAME"},
	{"github-enterprise-team", "SOCIAL_AUTH_GITHUB_ENTERPRISE_TEAM_", "github-enterprise-team", "GitHub Enterprise Team", true, "ID"},
}

func init() {
	for _, v := range GitHubVariants {
		defs := []*Def{
			{Key: v.Prefix + "CALLBACK_URL", Type: "string", Default: "", ReadOnly: true, Label: v.Label + " OAuth2 Callback URL",
				Help: "Give this URL as the callback URL of the OAuth application (from TOWER_URL_BASE)."},
		}
		if v.Enterprise {
			defs = append(defs,
				&Def{Key: v.Prefix + "URL", Type: "string", Default: "", Label: v.Label + " URL",
					Help: "The URL of the GitHub Enterprise instance, such as https://github.example.com."},
				&Def{Key: v.Prefix + "API_URL", Type: "string", Default: "", Label: v.Label + " API URL",
					Help: "The API URL of the GitHub Enterprise instance, such as https://github.example.com/api/v3/."})
		}
		defs = append(defs,
			&Def{Key: v.Prefix + "KEY", Type: "string", Default: "", Label: v.Label + " OAuth2 Key", Help: "The OAuth application's client ID."},
			&Def{Key: v.Prefix + "SECRET", Type: "string", Default: "", Secret: true, Label: v.Label + " OAuth2 Secret", Help: "The OAuth application's client secret."})
		switch v.Member {
		case "NAME":
			defs = append(defs, &Def{Key: v.Prefix + "NAME", Type: "string", Default: "", Label: v.Label + " Name",
				Help: "The organization users must be members of, as in its URL: https://github.com/<organization>."})
		case "ID":
			defs = append(defs, &Def{Key: v.Prefix + "ID", Type: "string", Default: "", Label: v.Label + " ID",
				Help: "The numeric ID of the team users must be members of."})
		}
		defs = append(defs,
			&Def{Key: v.Prefix + "ORGANIZATION_MAP", Type: "nested object", Default: nil, Nullable: true, Label: v.Label + " OAuth2 Organization Map",
				Help: `Organizations users of this login join, by username or email: {"Default": {"users": true}}. Used instead of SOCIAL_AUTH_ORGANIZATION_MAP, as in AWX.`},
			&Def{Key: v.Prefix + "TEAM_MAP", Type: "nested object", Default: nil, Nullable: true, Label: v.Label + " OAuth2 Team Map",
				Help: `Teams users of this login join: {"Web": {"organization": "Default", "users": true}}.`})
		for _, d := range defs {
			d.Category = v.Category
			Register(d)
		}
	}
}
