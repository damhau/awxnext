// Package credtypes holds the managed credential type definitions and the
// validation of credential type schemas and credential inputs.
package credtypes

// Managed credential types, matching AWX's built-in definitions (namespace, kind, inputs, injectors).
// Types whose injection is done in code (ssh, scm, vault, net, ...) carry no injectors.

type field = map[string]any

func str(id, label string, extra ...any) field {
	f := field{"id": id, "label": label, "type": "string"}
	for i := 0; i+1 < len(extra); i += 2 {
		f[extra[i].(string)] = extra[i+1]
	}
	return f
}

func boolean(id, label string, extra ...any) field {
	f := field{"id": id, "label": label, "type": "boolean"}
	for i := 0; i+1 < len(extra); i += 2 {
		f[extra[i].(string)] = extra[i+1]
	}
	return f
}

// Def is a managed credential type.
type Def struct {
	Namespace   string
	Name        string
	Kind        string
	Description string
	Inputs      map[string]any
	Injectors   map[string]any
}

func inputs(fields []field, required []string, extra ...any) map[string]any {
	m := map[string]any{"fields": fields}
	if len(required) > 0 {
		m["required"] = required
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

var becomeHelp = "Specify a method for \"become\" operations. This is equivalent to specifying the --become-method Ansible parameter."

// Managed lists the built-in credential types.
var Managed = []Def{
	{Namespace: "ssh", Name: "Machine", Kind: "ssh", Inputs: inputs([]field{
		str("username", "Username"),
		str("password", "Password", "secret", true, "ask_at_runtime", true),
		str("ssh_key_data", "SSH Private Key", "format", "ssh_private_key", "secret", true, "multiline", true),
		str("ssh_public_key_data", "Signed SSH Certificate", "multiline", true, "secret", true),
		str("ssh_key_unlock", "Private Key Passphrase", "secret", true, "ask_at_runtime", true),
		str("become_method", "Privilege Escalation Method", "help_text", becomeHelp),
		str("become_username", "Privilege Escalation Username"),
		str("become_password", "Privilege Escalation Password", "secret", true, "ask_at_runtime", true),
	}, nil, "dependencies", map[string]any{"ssh_key_unlock": []any{"ssh_key_data"}})},

	{Namespace: "scm", Name: "Source Control", Kind: "scm", Inputs: inputs([]field{
		str("username", "Username"),
		str("password", "Password", "secret", true),
		str("ssh_key_data", "SCM Private Key", "format", "ssh_private_key", "secret", true, "multiline", true),
		str("ssh_key_unlock", "Private Key Passphrase", "secret", true),
	}, nil, "dependencies", map[string]any{"ssh_key_unlock": []any{"ssh_key_data"}})},

	{Namespace: "vault", Name: "Vault", Kind: "vault", Inputs: inputs([]field{
		str("vault_password", "Vault Password", "secret", true, "ask_at_runtime", true),
		str("vault_id", "Vault Identifier", "format", "vault_id", "help_text", "Specify an (optional) Vault ID. This is equivalent to specifying the --vault-id Ansible parameter for providing multiple Vault passwords."),
	}, []string{"vault_password"})},

	{Namespace: "net", Name: "Network", Kind: "net", Inputs: inputs([]field{
		str("username", "Username"),
		str("password", "Password", "secret", true),
		str("ssh_key_data", "SSH Private Key", "format", "ssh_private_key", "secret", true, "multiline", true),
		str("ssh_key_unlock", "Private Key Passphrase", "secret", true),
		boolean("authorize", "Authorize"),
		str("authorize_password", "Authorize Password", "secret", true),
	}, []string{"username"}, "dependencies", map[string]any{"authorize_password": []any{"authorize"}, "ssh_key_unlock": []any{"ssh_key_data"}})},

	{Namespace: "aws", Name: "Amazon Web Services", Kind: "cloud", Inputs: inputs([]field{
		str("username", "Access Key"),
		str("password", "Secret Key", "secret", true),
		str("security_token", "STS Token", "secret", true, "help_text", "Security Token Service (STS) is a web service that enables you to request temporary, limited-privilege credentials for AWS Identity and Access Management (IAM) users."),
	}, []string{"username", "password"}), Injectors: map[string]any{"env": map[string]any{
		"AWS_ACCESS_KEY_ID": "{{username}}", "AWS_SECRET_ACCESS_KEY": "{{password}}", "AWS_SECURITY_TOKEN": "{{security_token}}", "AWS_SESSION_TOKEN": "{{security_token}}",
	}}},

	{Namespace: "openstack", Name: "OpenStack", Kind: "cloud", Inputs: inputs([]field{
		str("username", "Username"),
		str("password", "Password (API Key)", "secret", true),
		str("host", "Host (Authentication URL)", "help_text", "The host to authenticate with. For example, https://openstack.business.com/v2.0/"),
		str("project", "Project (Tenant Name)"),
		str("project_domain_name", "Project (Domain Name)"),
		str("domain", "Domain Name"),
		str("region", "Region Name"),
		boolean("verify_ssl", "Verify SSL", "default", true),
	}, []string{"username", "password", "host", "project"}), Injectors: map[string]any{
		"file": map[string]any{"template.clouds": "clouds:\n  devstack:\n    auth:\n      auth_url: {{host}}\n      username: {{username}}\n      password: {{password}}\n      project_name: {{project}}\n      project_domain_name: {{project_domain_name}}\n      domain_name: {{domain}}\n    region_name: {{region}}\n    verify: {{verify_ssl}}\n"},
		"env":  map[string]any{"OS_CLIENT_CONFIG_FILE": "{{tower.filename.clouds}}"},
	}},

	{Namespace: "vmware", Name: "VMware vCenter", Kind: "cloud", Inputs: inputs([]field{
		str("host", "VCenter Host"),
		str("username", "Username"),
		str("password", "Password", "secret", true),
	}, []string{"host", "username", "password"}), Injectors: map[string]any{"env": map[string]any{
		"VMWARE_HOST": "{{host}}", "VMWARE_USER": "{{username}}", "VMWARE_PASSWORD": "{{password}}", "VMWARE_VALIDATE_CERTS": "False",
	}}},

	{Namespace: "satellite6", Name: "Red Hat Satellite 6", Kind: "cloud", Inputs: inputs([]field{
		str("host", "Satellite 6 URL"),
		str("username", "Username"),
		str("password", "Password", "secret", true),
	}, []string{"host", "username", "password"}), Injectors: map[string]any{"env": map[string]any{
		"FOREMAN_SERVER": "{{host}}", "FOREMAN_USER": "{{username}}", "FOREMAN_PASSWORD": "{{password}}",
	}}},

	{Namespace: "gce", Name: "Google Compute Engine", Kind: "cloud", Inputs: inputs([]field{
		str("username", "Service Account Email Address"),
		str("project", "Project"),
		str("ssh_key_data", "RSA Private Key", "format", "ssh_private_key", "secret", true, "multiline", true),
	}, []string{"username", "ssh_key_data"}), Injectors: map[string]any{
		"file": map[string]any{"template.gce_creds": "{\"type\": \"service_account\", \"private_key\": {{ssh_key_data|tojson}}, \"client_email\": {{username|tojson}}, \"project_id\": {{project|tojson}}, \"token_uri\": \"https://oauth2.googleapis.com/token\"}"},
		"env": map[string]any{
			"GCE_EMAIL": "{{username}}", "GCE_PROJECT": "{{project}}", "GCE_CREDENTIALS_FILE_PATH": "{{tower.filename.gce_creds}}",
			"GCP_AUTH_KIND": "serviceaccount", "GCP_PROJECT": "{{project}}", "GCP_ENV_TYPE": "tower", "GCP_SERVICE_ACCOUNT_FILE": "{{tower.filename.gce_creds}}",
		},
	}},

	{Namespace: "azure_rm", Name: "Microsoft Azure Resource Manager", Kind: "cloud", Inputs: inputs([]field{
		str("subscription", "Subscription ID", "help_text", "Subscription ID is an Azure construct, which is mapped to a username."),
		str("username", "Username"),
		str("password", "Password", "secret", true),
		str("client", "Client ID"),
		str("secret", "Client Secret", "secret", true),
		str("tenant", "Tenant ID"),
		str("cloud_environment", "Azure Cloud Environment", "help_text", "Environment variable AZURE_CLOUD_ENVIRONMENT when using Azure GovCloud or Azure stack."),
	}, []string{"subscription"}), Injectors: map[string]any{"env": map[string]any{
		"AZURE_SUBSCRIPTION_ID": "{{subscription}}", "AZURE_CLIENT_ID": "{{client}}", "AZURE_SECRET": "{{secret}}", "AZURE_TENANT": "{{tenant}}",
		"AZURE_AD_USER": "{{username}}", "AZURE_PASSWORD": "{{password}}", "AZURE_CLOUD_ENVIRONMENT": "{{cloud_environment}}",
	}}},

	{Namespace: "github_token", Name: "GitHub Personal Access Token", Kind: "token", Inputs: inputs([]field{
		str("token", "Token", "secret", true, "help_text", "This token needs to come from your profile settings in GitHub"),
	}, []string{"token"})},

	{Namespace: "gitlab_token", Name: "GitLab Personal Access Token", Kind: "token", Inputs: inputs([]field{
		str("token", "Token", "secret", true, "help_text", "This token needs to come from your profile settings in GitLab"),
	}, []string{"token"})},

	{Namespace: "bitbucket_dc_token", Name: "Bitbucket Data Center HTTP Access Token", Kind: "token", Inputs: inputs([]field{
		str("token", "Token", "secret", true),
	}, []string{"token"})},

	{Namespace: "insights", Name: "Insights", Kind: "insights", Inputs: inputs([]field{
		str("username", "Username"),
		str("password", "Password", "secret", true),
	}, []string{"username", "password"}), Injectors: map[string]any{
		"extra_vars": map[string]any{"scm_username": "{{username}}", "scm_password": "{{password}}"},
		"env":        map[string]any{"INSIGHTS_USER": "{{username}}", "INSIGHTS_PASSWORD": "{{password}}"},
	}},

	// The ovirt inventory plugin reads the INI file, the ovirt modules the
	// environment: AWX injects both.
	{Namespace: "rhv", Name: "Red Hat Virtualization", Kind: "cloud", Inputs: inputs([]field{
		str("host", "Host (Authentication URL)", "help_text", "The host to authenticate with."),
		str("username", "Username"),
		str("password", "Password", "secret", true),
		str("ca_file", "CA File", "help_text", "Absolute file path to the CA file to use (optional)"),
	}, []string{"host", "username", "password"}), Injectors: map[string]any{
		"file": map[string]any{"template": "[ovirt]\novirt_url={{host}}\novirt_username={{username}}\novirt_password={{password}}\n{% if ca_file %}ovirt_ca_file={{ca_file}}{% endif %}"},
		"env": map[string]any{
			"OVIRT_INI_PATH": "{{tower.filename}}", "OVIRT_URL": "{{host}}", "OVIRT_USERNAME": "{{username}}", "OVIRT_PASSWORD": "{{password}}",
		},
	}},

	{Namespace: "controller", Name: "Red Hat Ansible Automation Platform", Kind: "cloud", Inputs: inputs([]field{
		str("host", "Red Hat Ansible Automation Platform", "help_text", "Red Hat Ansible Automation Platform base URL to authenticate with."),
		str("username", "Username"),
		str("password", "Password", "secret", true),
		str("oauth_token", "OAuth Token", "secret", true),
		boolean("verify_ssl", "Verify SSL", "secret", false),
	}, []string{"host"}), Injectors: map[string]any{"env": map[string]any{
		"CONTROLLER_HOST": "{{host}}", "CONTROLLER_USERNAME": "{{username}}", "CONTROLLER_PASSWORD": "{{password}}",
		"CONTROLLER_VERIFY_SSL": "{{verify_ssl}}", "CONTROLLER_OAUTH_TOKEN": "{{oauth_token}}",
		"TOWER_HOST": "{{host}}", "TOWER_USERNAME": "{{username}}", "TOWER_PASSWORD": "{{password}}", "TOWER_VERIFY_SSL": "{{verify_ssl}}", "TOWER_OAUTH_TOKEN": "{{oauth_token}}",
	}}},

	{Namespace: "kubernetes_bearer_token", Name: "OpenShift or Kubernetes API Bearer Token", Kind: "kubernetes", Inputs: inputs([]field{
		str("host", "OpenShift or Kubernetes API Endpoint", "help_text", "The OpenShift or Kubernetes API Endpoint to authenticate with."),
		str("bearer_token", "API authentication bearer token", "secret", true),
		boolean("verify_ssl", "Verify SSL", "default", true),
		str("ssl_ca_cert", "Certificate Authority data", "secret", true, "multiline", true),
	}, []string{"host", "bearer_token"})}, // injected in code, as in AWX

	{Namespace: "registry", Name: "Container Registry", Kind: "registry", Inputs: inputs([]field{
		str("host", "Authentication URL", "help_text", "Authentication endpoint for the container registry.", "default", "quay.io"),
		str("username", "Username"),
		str("password", "Password or Token", "secret", true, "help_text", "A password or token used to authenticate with"),
		boolean("verify_ssl", "Verify SSL", "default", true),
	}, []string{"host"})},

	{Namespace: "galaxy_api_token", Name: "Ansible Galaxy/Automation Hub API Token", Kind: "galaxy", Inputs: inputs([]field{
		str("url", "Galaxy Server URL", "help_text", "The URL of the Galaxy instance to connect to."),
		str("auth_url", "Auth Server URL", "help_text", "The URL of a Keycloak server token_endpoint, if using SSO auth."),
		str("token", "API Token", "secret", true, "help_text", "A token to use for authentication against the Galaxy instance."),
	}, []string{"url"})},

	{Namespace: "gpg_public_key", Name: "GPG Public Key", Kind: "cryptography", Inputs: inputs([]field{
		str("gpg_public_key", "GPG Public Key", "secret", true, "multiline", true, "help_text", "GPG Public Key used to validate content signatures."),
	}, []string{"gpg_public_key"})},

	{Namespace: "terraform", Name: "Terraform backend configuration", Kind: "cloud", Inputs: inputs([]field{
		str("configuration", "Backend configuration", "secret", true, "multiline", true, "help_text", "Terraform backend config as Hashicorp configuration language."),
		str("gce_credentials", "Google Cloud Platform account credentials", "secret", true, "multiline", true),
	}, []string{"configuration"}), Injectors: map[string]any{
		"file": map[string]any{"template.backend": "{{configuration}}", "template.gce_credentials": "{{gce_credentials}}"},
		"env":  map[string]any{"TF_BACKEND_CONFIG_FILE": "{{tower.filename.backend}}", "GOOGLE_BACKEND_CREDENTIALS": "{{tower.filename.gce_credentials}}"},
	}},

	// External secret lookups (v1 scope: HashiCorp Vault and Azure Key Vault).
	{Namespace: "hashivault_kv", Name: "HashiCorp Vault Secret Lookup", Kind: "external", Inputs: inputs([]field{
		str("url", "Server URL", "help_text", "The URL to the HashiCorp Vault"),
		str("token", "Token", "secret", true, "help_text", "The access token used to authenticate to the Vault server"),
		str("cacert", "CA Certificate", "multiline", true, "help_text", "The CA certificate used to verify the SSL certificate of the Vault server"),
		str("role_id", "AppRole role_id", "multiline", false, "help_text", "The Role ID for AppRole Authentication"),
		str("secret_id", "AppRole secret_id", "multiline", false, "secret", true, "help_text", "The Secret ID for AppRole Authentication"),
		str("namespace", "Namespace name (Vault Enterprise only)", "multiline", false),
		str("kubernetes_role", "Kubernetes role", "multiline", false, "help_text", "The Role for Kubernetes Authentication. This is the named role, configured in Vault server, that AWX pod's Service Account is bound to."),
		str("default_auth_path", "Path to Auth", "multiline", false, "default", "approle", "help_text", "The Authentication path to use if one isn't provided in the metadata when linking to an input field. Defaults to 'approle'"),
		str("api_version", "API Version", "choices", []any{"v1", "v2"}, "default", "v1", "help_text", "API v1 is for static key/value lookups.  API v2 is for versioned key/value lookups."),
	}, []string{"url", "api_version"}, "metadata", []any{
		str("secret_backend", "Name of Secret Backend", "help_text", "The name of the kv secret backend (if left empty, the first segment of the secret path will be used)."),
		str("secret_path", "Path to Secret", "help_text", "The path to the secret stored in the secret backend e.g, /some/secret/. It is recommended that you use the secret backend field to identify the storage backend and to use this field for locating a specific secret within that store. However, if you prefer to fully identify both the secret backend and one of its secrets using only this field, join their locations into a single path without any additional separators, e.g, /location/of/backend/some/secret."),
		str("auth_path", "Path to Auth", "help_text", "The path where the Authentication method is mounted e.g, approle"),
		str("secret_key", "Key Name", "help_text", "The name of the key to look up in the secret."),
		str("secret_version", "Secret Version (v2 only)", "help_text", "Used to specify a specific secret version (if left empty, the latest version will be used)."),
	}, "metadata_required", []any{"secret_path", "secret_key"})},

	{Namespace: "hashivault_ssh", Name: "HashiCorp Vault Signed SSH", Kind: "external", Inputs: inputs([]field{
		str("url", "Server URL", "help_text", "The URL to the HashiCorp Vault"),
		str("token", "Token", "secret", true, "help_text", "The access token used to authenticate to the Vault server"),
		str("cacert", "CA Certificate", "multiline", true),
		str("role_id", "AppRole role_id"),
		str("secret_id", "AppRole secret_id", "secret", true),
		str("namespace", "Namespace name (Vault Enterprise only)"),
		str("kubernetes_role", "Kubernetes role"),
		str("default_auth_path", "Path to Auth", "default", "approle"),
	}, []string{"url"}, "metadata", []any{
		str("public_key", "Unsigned Public Key", "multiline", true),
		str("secret_path", "Path to Secret", "help_text", "The path to the secret stored in the secret backend e.g, /some/secret/"),
		str("auth_path", "Path to Auth"),
		str("role", "Role Name", "help_text", "The name of the role used to sign."),
		str("valid_principals", "Valid Principals", "help_text", "Valid principals (either usernames or hostnames) that the certificate should be signed for."),
	}, "metadata_required", []any{"public_key", "secret_path", "role"})},

	{Namespace: "azure_kv", Name: "Microsoft Azure Key Vault", Kind: "external", Inputs: inputs([]field{
		str("url", "Vault URL (DNS Name)"),
		str("client", "Client ID"),
		str("secret", "Client Secret", "secret", true),
		str("tenant", "Tenant ID"),
		str("cloud_name", "Cloud Environment", "help_text", "Specify which azure cloud environment to use.", "choices", []any{"AzureCloud", "AzureChinaCloud", "AzureUSGovernment"}, "default", "AzureCloud"),
	}, []string{"url"}, "metadata", []any{
		str("secret_field", "Secret Name", "help_text", "The name of the secret to look up."),
		str("secret_version", "Secret Version", "help_text", "Used to specify a specific secret version (if left empty, the latest version will be used)."),
	}, "metadata_required", []any{"secret_field"})},
}

func init() {
	// AWX lists required metadata fields of external types in "required" too.
	for _, d := range Managed {
		mr, ok := d.Inputs["metadata_required"].([]any)
		if !ok {
			continue
		}
		req, _ := d.Inputs["required"].([]string)
		for _, r := range mr {
			req = append(req, r.(string))
		}
		d.Inputs["required"] = req
		delete(d.Inputs, "metadata_required")
	}
}
