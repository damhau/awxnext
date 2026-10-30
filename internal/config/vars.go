package config

// Var is an environment variable awxnext reads, for the configuration
// reference (docs/configuration.md, from cmd/gendocs). A test checks that
// every AWXNEXT_ variable the code reads is here.
type Var struct {
	Name, Default, Help, Group string
}

// Vars are the environment variables, by group.
var Vars = []Var{
	{"AWXNEXT_DATABASE_URL", "postgres://postgres@localhost:5432/awxnext?sslmode=disable", "PostgreSQL connection URL.", "Server"},
	{"AWXNEXT_LISTEN", ":8052", "Address the API and UI listen on.", "Server"},
	{"AWXNEXT_BASE_URL", "http://localhost:8052", "External URL: OIDC and SAML redirects, links in notifications.", "Server"},
	{"AWXNEXT_NODE_NAME", "the host name", "This node's instance name; unique per node.", "Server"},
	{"AWXNEXT_INTERNAL_URL", "", "How the other control nodes reach this one, to forward a container group pod's or an agent's connection to the node running its job.", "Server"},
	{"AWXNEXT_SERVICE_URL", "AWXNEXT_BASE_URL", "How container group job pods reach the control plane.", "Server"},
	{"AWXNEXT_UI_DIR", "", "Serve the web UI from this directory instead of the embedded build.", "Server"},
	{"AWXNEXT_METRICS_TOKEN", "", "Bearer token /metrics requires; open when empty.", "Server"},
	{"AWXNEXT_LOG_FORMAT", "text", "text or json.", "Server"},
	{"AWXNEXT_LOG_LEVEL", "info", "debug, info, warn or error.", "Server"},
	{"AWXNEXT_DEV", "", "Development mode: an insecure default secret key, the database created when missing, websockets from UI dev servers on localhost.", "Server"},

	{"AWXNEXT_PROJECTS_ROOT", "/var/lib/awxnext/projects", "Project checkouts.", "Jobs"},
	{"AWXNEXT_JOBS_ROOT", "/var/lib/awxnext/jobs", "Job working directories.", "Jobs"},
	{"AWXNEXT_CONTAINER_RUNTIME", "podman", "How jobs run on control nodes: podman or docker (in the execution environment's image), or none (ansible-runner on the PATH).", "Jobs"},
	{"AWXNEXT_RUNNER_CMD", "ansible-runner", "ansible-runner for runtime none.", "Jobs"},
	{"AWXNEXT_JOB_ISOLATION", "auto", "Runtime none: auto (each job as a job user of its own when awxnext-jobexec works, else as the server's user), users (required: the server does not start without) or off.", "Jobs"},
	{"AWXNEXT_JOB_EXEC", "/usr/local/libexec/awxnext-jobexec", "The helper that runs a job as a job user (awxjob0, awxjob1, ...).", "Jobs"},
	{"AWXNEXT_DEFAULT_EE_IMAGE", "quay.io/ansible/awx-ee:latest", "Image of the default execution environment created at the first start.", "Jobs"},
	{"AWXNEXT_CAPACITY", "", "Fixed forks capacity of this node, instead of the one its CPU and memory allow.", "Jobs"},
	{"AWXNEXT_SHIM_IMAGE", "ghcr.io/damhau/awxnext:latest", "Image whose awxnext binary is copied into container group job pods.", "Jobs"},
	{"AWXNEXT_KEEP_JOB_PODS", "", "Keep container group job pods after their job (debugging).", "Jobs"},
	{"SYSTEM_TASK_FORKS_CPU", "4", "Forks per CPU, as in AWX.", "Jobs"},
	{"SYSTEM_TASK_FORKS_MEM", "100", "MB of memory per fork, as in AWX.", "Jobs"},
	{"SYSTEM_TASK_ABS_CPU", "", "Replaces a control node's measured CPUs (2, 1500m), as in AWX.", "Jobs"},
	{"SYSTEM_TASK_ABS_MEM", "", "Replaces a control node's measured memory (8Gi), as in AWX.", "Jobs"},

	{"AWXNEXT_KMS", "local", "Master key provider: local (derived from AWXNEXT_SECRET_KEY), vault-transit or azure-keyvault.", "Master key"},
	{"AWXNEXT_SECRET_KEY", "", "Local provider: the secret the master key is derived from. Required unless AWXNEXT_DEV is set.", "Master key"},
	{"AWXNEXT_KMS_VAULT_ADDR", "", "Vault Transit: the server's address.", "Master key"},
	{"AWXNEXT_KMS_VAULT_KEY", "", "Vault Transit: the key's name (required).", "Master key"},
	{"AWXNEXT_KMS_VAULT_MOUNT", "transit", "Vault Transit: the secrets engine's mount.", "Master key"},
	{"AWXNEXT_KMS_VAULT_NAMESPACE", "", "Vault Transit: the namespace (Vault Enterprise).", "Master key"},
	{"AWXNEXT_KMS_VAULT_TOKEN", "", "Vault Transit: a token, or AppRole or Kubernetes authentication below.", "Master key"},
	{"AWXNEXT_KMS_VAULT_ROLE_ID", "", "Vault Transit: AppRole role ID, with AWXNEXT_KMS_VAULT_SECRET_ID.", "Master key"},
	{"AWXNEXT_KMS_VAULT_SECRET_ID", "", "Vault Transit: AppRole secret ID.", "Master key"},
	{"AWXNEXT_KMS_VAULT_K8S_ROLE", "", "Vault Transit: Kubernetes authentication role (the pod's service account token).", "Master key"},
	{"AWXNEXT_KMS_VAULT_CACERT", "", "Vault Transit: CA certificate file of the server.", "Master key"},
	{"AWXNEXT_KMS_AZURE_VAULT_URL", "", "Azure Key Vault: the vault's URL (required).", "Master key"},
	{"AWXNEXT_KMS_AZURE_KEY", "", "Azure Key Vault: the key's name (required).", "Master key"},
	{"AWXNEXT_KMS_AZURE_TENANT_ID", "", "Azure Key Vault: service principal tenant.", "Master key"},
	{"AWXNEXT_KMS_AZURE_CLIENT_ID", "", "Azure Key Vault: service principal client (or workload identity).", "Master key"},
	{"AWXNEXT_KMS_AZURE_CLIENT_SECRET", "", "Azure Key Vault: service principal secret.", "Master key"},
	{"AWXNEXT_KMS_AZURE_MANAGED_IDENTITY", "", "Azure Key Vault: set to use the managed identity.", "Master key"},
	{"AWXNEXT_OLD_", "", "Prefix of the previous master key's variables (AWXNEXT_OLD_KMS, AWXNEXT_OLD_SECRET_KEY, ...) for `awxnext keys rewrap`.", "Master key"},

	{"AWXNEXT_ADMIN_PASSWORD", "", "Images: the superuser is created before the server starts, its password reset to this on every start.", "Images and commands"},
	{"AWXNEXT_ADMIN_USER", "admin", "Images: that superuser's name.", "Images and commands"},
	{"AWXNEXT_DB_PASSWORD", "", "Compose: the bundled PostgreSQL's password.", "Images and commands"},
	{"AWXNEXT_DEMO_REPO", "", "Images: where seed-demo creates its playbook repository.", "Images and commands"},
	{"AWXNEXT_BACKUP_PASSPHRASE", "", "`awxnext backup` and `restore`: the key escrow's passphrase (or --passphrase-file).", "Images and commands"},
	{"AWXNEXT_PG_DUMP", "pg_dump on the PATH", "`awxnext backup`: pg_dump, of the server's major version or newer.", "Images and commands"},
	{"AWXNEXT_PG_RESTORE", "pg_restore on the PATH", "`awxnext restore`: pg_restore.", "Images and commands"},
	{"AWX_DATABASE_URL", "", "`awxnext import-awx`: the AWX database (or --source).", "Images and commands"},
	{"AWX_SECRET_KEY", "", "`awxnext import-awx`: AWX's SECRET_KEY, to decrypt its credentials (or --secret-key).", "Images and commands"},

	{"AWXNEXT_AGENT_URL", "", "Agent: the control plane's URL (or --url).", "Agent"},
	{"AWXNEXT_AGENT_TOKEN", "", "Agent: its instance's token (or --token).", "Agent"},
	{"AWXNEXT_AGENT_RUNTIME", "podman", "Agent: podman, docker or none (or --runtime).", "Agent"},
	{"AWXNEXT_AGENT_WORK_DIR", "/var/lib/awxnext-agent", "Agent: job files (or --work-dir).", "Agent"},
	{"AWXNEXT_AGENT_CA_CERT", "", "Agent: the CA of the control plane's certificate (or --ca-cert).", "Agent"},
	{"AWXNEXT_SHIM_TOKEN", "", "Set by the control plane in container group job pods.", "Agent"},
	{"AWXNEXT_SHIM_INSECURE", "", "Container group job pods: skip TLS verification of the control plane (testing).", "Agent"},

	{"OTEL_EXPORTER_OTLP_ENDPOINT", "", "OpenTelemetry exporter endpoint; no tracing when neither this nor the next is set.", "Observability"},
	{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "", "OpenTelemetry traces endpoint.", "Observability"},
	{"OTEL_SERVICE_NAME", "awxnext", "OpenTelemetry service name.", "Observability"},
}
