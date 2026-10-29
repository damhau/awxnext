package settings

// OAuthLogin is one of AWX's other OAuth2 logins.
type OAuthLogin struct {
	Backend, Prefix, Category, Label string
}

// OAuthLogins are AWX's Google and Azure AD logins, by python-social-auth backend.
var OAuthLogins = []OAuthLogin{
	{"google-oauth2", "SOCIAL_AUTH_GOOGLE_OAUTH2_", "google-oauth2", "Google OAuth2"},
	{"azuread-oauth2", "SOCIAL_AUTH_AZUREAD_OAUTH2_", "azuread-oauth2", "Azure AD OAuth2"},
}

func init() {
	for _, l := range OAuthLogins {
		defs := []*Def{
			{Key: l.Prefix + "CALLBACK_URL", Type: "string", Default: "", ReadOnly: true, Label: l.Label + " Callback URL",
				Help: "Give this URL as the redirect URI of the application (from TOWER_URL_BASE)."},
			{Key: l.Prefix + "KEY", Type: "string", Default: "", Label: l.Label + " Key", Help: "The application's client ID."},
			{Key: l.Prefix + "SECRET", Type: "string", Default: "", Secret: true, Label: l.Label + " Secret", Help: "The application's client secret."},
		}
		if l.Backend == "google-oauth2" {
			defs = append(defs,
				&Def{Key: l.Prefix + "WHITELISTED_DOMAINS", Type: "list", Default: []any{}, Label: "Google OAuth2 Allowed Domains",
					Help: "Only users whose email is in one of these domains can log in. Empty allows every domain."},
				&Def{Key: l.Prefix + "AUTH_EXTRA_ARGUMENTS", Type: "nested object", Default: map[string]any{}, Label: "Google OAuth2 Extra Arguments",
					Help: `Extra parameters of the authorization request, such as {"hd": "example.com"} to offer only accounts of a Google Workspace domain.`})
		}
		defs = append(defs,
			&Def{Key: l.Prefix + "ORGANIZATION_MAP", Type: "nested object", Default: nil, Nullable: true, Label: l.Label + " Organization Map",
				Help: `Organizations users of this login join, by username or email: {"Default": {"users": true}}. Used instead of SOCIAL_AUTH_ORGANIZATION_MAP, as in AWX.`},
			&Def{Key: l.Prefix + "TEAM_MAP", Type: "nested object", Default: nil, Nullable: true, Label: l.Label + " Team Map",
				Help: `Teams users of this login join: {"Web": {"organization": "Default", "users": true}}.`})
		for _, d := range defs {
			d.Category = l.Category
			Register(d)
		}
	}
}
