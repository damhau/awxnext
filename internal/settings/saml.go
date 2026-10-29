package settings

func init() {
	for _, d := range []*Def{
		{Key: "SOCIAL_AUTH_SAML_CALLBACK_URL", Type: "string", Default: "", ReadOnly: true, Label: "SAML Assertion Consumer Service (ACS) URL",
			Help: "Register awxnext with your identity provider as a service provider with this URL (from TOWER_URL_BASE)."},
		{Key: "SOCIAL_AUTH_SAML_METADATA_URL", Type: "string", Default: "", ReadOnly: true, Label: "SAML Service Provider Metadata URL",
			Help: "awxnext's service provider metadata, for identity providers that read it."},
		{Key: "SOCIAL_AUTH_SAML_SP_ENTITY_ID", Type: "string", Default: "", Label: "SAML Service Provider Entity ID",
			Help: "The audience of the SAML assertions. Empty uses TOWER_URL_BASE."},
		{Key: "SOCIAL_AUTH_SAML_SP_PUBLIC_CERT", Type: "string", Default: "", Label: "SAML Service Provider Public Certificate",
			Help: "PEM certificate of awxnext as a service provider, published in its metadata."},
		{Key: "SOCIAL_AUTH_SAML_SP_PRIVATE_KEY", Type: "string", Default: "", Secret: true, Label: "SAML Service Provider Private Key",
			Help: "PEM private key of the certificate, to sign requests and decrypt assertions."},
		{Key: "SOCIAL_AUTH_SAML_ORG_INFO", Type: "nested object", Default: map[string]any{}, Label: "SAML Service Provider Organization Info",
			Help: `For the metadata, by language: {"en-US": {"name": "example", "displayname": "Example", "url": "https://www.example.com"}}.`},
		{Key: "SOCIAL_AUTH_SAML_TECHNICAL_CONTACT", Type: "nested object", Default: map[string]any{}, Label: "SAML Service Provider Technical Contact",
			Help: `For the metadata: {"givenName": "Technical Contact", "emailAddress": "techsup@example.com"}.`},
		{Key: "SOCIAL_AUTH_SAML_SUPPORT_CONTACT", Type: "nested object", Default: map[string]any{}, Label: "SAML Service Provider Support Contact",
			Help: `For the metadata: {"givenName": "Support Contact", "emailAddress": "support@example.com"}.`},
		{Key: "SOCIAL_AUTH_SAML_ENABLED_IDPS", Type: "nested object", Default: map[string]any{}, Label: "SAML Enabled Identity Providers",
			Help: `Identity providers by name: {"myidp": {"entity_id": "https://idp.example.com", "url": "https://idp.example.com/sso", "x509cert": "MIIC…", "attr_user_permanent_id": "name_id", "attr_username": "uid", "attr_email": "mail", "attr_first_name": "givenName", "attr_last_name": "sn"}}. The attributes default to the standard OIDs; "name_id" uses the subject's NameID.`},
		{Key: "SOCIAL_AUTH_SAML_SECURITY_CONFIG", Type: "nested object", Default: map[string]any{"requestedAuthnContext": false}, Label: "SAML Security Config",
			Help: "python3-saml's security settings. Used: authnRequestsSigned, signatureAlgorithm, wantMessagesSigned, wantAssertionsSigned, wantAssertionsEncrypted, wantNameId, requestedAuthnContext and requestedAuthnContextComparison. A response or its assertion must always be signed."},
		{Key: "SOCIAL_AUTH_SAML_SP_EXTRA", Type: "nested object", Default: nil, Nullable: true, CompatOnly: true, Label: "SAML Service Provider extra configuration data",
			Help: "No effect in awxnext: python3-saml's extra service provider settings have no equivalent."},
		{Key: "SOCIAL_AUTH_SAML_EXTRA_DATA", Type: "list", Default: nil, Nullable: true, CompatOnly: true, Label: "SAML IDP to extra_data attribute mapping",
			Help: "No effect in awxnext: it keeps no social auth extra data."},
		{Key: "SOCIAL_AUTH_SAML_ORGANIZATION_MAP", Type: "nested object", Default: nil, Nullable: true, Label: "SAML Organization Map",
			Help: `Organizations a SAML user joins, by username or email: {"Default": {"users": true}}, as SOCIAL_AUTH_ORGANIZATION_MAP, which SAML logins do not use. As in AWX's SAML pipeline, a null expression removes like false.`},
		{Key: "SOCIAL_AUTH_SAML_TEAM_MAP", Type: "nested object", Default: nil, Nullable: true, Label: "SAML Team Map",
			Help: `Teams a SAML user joins, by username or email: {"Web": {"organization": "Ops", "users": true}}.`},
		{Key: "SOCIAL_AUTH_SAML_ORGANIZATION_ATTR", Type: "nested object", Default: nil, Nullable: true, Label: "SAML Organization Attribute Mapping",
			Help: `Organizations from SAML attributes: {"saml_attr": "member-of", "saml_admin_attr": "admin-of", "saml_auditor_attr": "auditor-of", "remove": true, "remove_admins": true, "remove_auditors": true}. With remove (the default), a user loses the memberships SAML gave in organizations the attribute no longer names.`},
		{Key: "SOCIAL_AUTH_SAML_TEAM_ATTR", Type: "nested object", Default: nil, Nullable: true, Label: "SAML Team Attribute Mapping",
			Help: `Teams from a SAML attribute: {"saml_attr": "team", "remove": true, "team_org_map": [{"team": "Blue", "team_alias": "Blue Team", "organization": "Default"}]}.`},
		{Key: "SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR", Type: "nested object", Default: nil, Nullable: true, Label: "SAML User Flags Attribute Mapping",
			Help: `Superusers and system auditors from the Role attribute or another attribute: {"is_superuser_role": ["awx_admins"], "is_superuser_attr": "is_superuser", "is_superuser_value": ["IT-Superadmin"], "remove_superusers": true, "is_system_auditor_role": [...], "is_system_auditor_attr": "...", "is_system_auditor_value": [...], "remove_system_auditors": true}. As in AWX, a SAML login without them takes the flags away unless remove_superusers or remove_system_auditors is false.`},
	} {
		d.Category = "saml"
		Register(d)
	}
}
