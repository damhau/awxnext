package settings

func init() {
	for _, d := range []*Def{
		// system
		{Key: "TOWER_URL_BASE", Category: "system", Type: "string", Default: "", Label: "Base URL of the service", Help: "Used by services like notifications to render a valid URL to the service."},
		{Key: "ACTIVITY_STREAM_ENABLED", Category: "system", Type: "boolean", Default: true, Label: "Enable Activity Stream", Help: "Record who changed which object, role and setting. Version history is recorded either way."},
		{Key: "ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC", Category: "system", Type: "boolean", Default: false, Label: "Enable Activity Stream for Inventory Sync", Help: "Also record the hosts and groups inventory syncs add and remove. Off by default: a sync can touch thousands of hosts."},
		{Key: "ORG_ADMINS_CAN_SEE_ALL_USERS", Category: "system", Type: "boolean", Default: true, Label: "All Users Visible to Organization Admins", Help: "On: organization admins see every user and team. Off: only those of their organizations."},
		{Key: "MANAGE_ORGANIZATION_AUTH", Category: "system", Type: "boolean", Default: true, Label: "Organization Admins Can Manage Users and Teams", Help: "Off: only superusers create, change and delete users and teams and change organization and team roles, for example when an identity provider owns membership."},
		{Key: "DEFAULT_EXECUTION_ENVIRONMENT", Category: "system", Type: "field", Default: nil, Label: "Global default execution environment"},
		{Key: "INSTALL_UUID", Category: "system", Type: "string", Default: "", ReadOnly: true, Public: true},
		{Key: "CUSTOM_VENV_PATHS", Category: "system", Type: "list", Default: []any{}},
		{Key: "REMOTE_HOST_HEADERS", Category: "system", Type: "list", Default: []any{"REMOTE_ADDR", "REMOTE_HOST"}},
		{Key: "PROXY_IP_ALLOWED_LIST", Category: "system", Type: "list", Default: []any{}},
		{Key: "SUBSCRIPTIONS_USERNAME", Category: "system", Type: "string", Default: ""},
		{Key: "AUTOMATION_ANALYTICS_GATHER_INTERVAL", Category: "system", Type: "integer", Default: 14400},
		{Key: "INSIGHTS_TRACKING_STATE", Category: "system", Type: "boolean", Default: false},

		// jobs
		{Key: "AD_HOC_COMMANDS", Category: "jobs", Type: "list", Label: "Ansible Modules Allowed for Ad Hoc Jobs", Default: []any{
			"command", "shell", "yum", "apt", "apt_key", "apt_repository", "apt_rpm", "service", "group", "user", "mount", "ping",
			"selinux", "setup", "win_ping", "win_service", "win_updates", "win_group", "win_user"}},
		{Key: "ALLOW_JINJA_IN_EXTRA_VARS", Category: "jobs", Type: "choice", Default: "template", Choices: [][]any{{"always", "Always"}, {"never", "Never"}, {"template", "Only On Job Template Definitions"}}},
		{Key: "ALLOW_ANSIBLE_VARS_AT_LAUNCH", Category: "jobs", Type: "boolean", Default: false, Label: "Allow ansible_* variables at launch",
			Help: "Off: launch-time variables, survey questions and answers, schedule and ad hoc variables named ansible_* are refused, as they control how Ansible connects and runs (for example ansible_ssh_common_args). On: AWX's behaviour. Variables on templates, inventories and hosts are not affected."},
		{Key: "IGNORE_UNPROMPTED_LAUNCH_FIELDS", Category: "jobs", Type: "boolean", Default: false, Label: "Ignore launch fields that are not prompted",
			Help: "Off: a launch that sends a field the template does not prompt for (limit, inventory, extra variables…) is refused. On: AWX's behaviour, the field is ignored, the job starts without it and the response lists it in ignored_fields."},
		{Key: "AWX_ISOLATION_BASE_PATH", Category: "jobs", Type: "string", Default: "/tmp"},
		{Key: "AWX_ISOLATION_SHOW_PATHS", Category: "jobs", Type: "list", Default: []any{}, Label: "Paths to expose to isolated jobs", Help: "Host paths mounted into execution environment containers, one per entry: HOST-DIR[:CONTAINER-DIR[:OPTIONS]], options such as ro or z. Jobs on the none runtime see the host's files already."},
		{Key: "AWX_TASK_ENV", Category: "jobs", Type: "nested object", Default: map[string]any{}, Label: "Extra Environment Variables"},
		{Key: "AWX_RUNNER_KEEPALIVE_SECONDS", Category: "jobs", Type: "integer", Default: 0, Label: "K8S Ansible Runner Keep-Alive Message Interval", Help: "Seconds of silence after which a job's worker sends a keepalive, so quiet tasks on remote execution nodes and container groups are not cut off by idle timeouts. 0: never."},
		{Key: "GALAXY_TASK_ENV", Category: "jobs", Type: "nested object", Default: map[string]any{"ANSIBLE_FORCE_COLOR": "false", "GIT_SSH_COMMAND": "ssh -o StrictHostKeyChecking=no"}},
		{Key: "PROJECT_UPDATE_VVV", Category: "jobs", Type: "boolean", Default: false},
		{Key: "AWX_ROLES_ENABLED", Category: "jobs", Type: "boolean", Default: true},
		{Key: "AWX_COLLECTIONS_ENABLED", Category: "jobs", Type: "boolean", Default: true},
		{Key: "AWX_SHOW_PLAYBOOK_LINKS", Category: "jobs", Type: "boolean", Default: false},
		{Key: "AWX_MOUNT_ISOLATED_PATHS_ON_K8S", Category: "jobs", Type: "boolean", Default: false, Label: "Expose host paths for Container Groups", Help: "Also mount the paths to expose into container group pods, as hostPath volumes of the node the pod runs on."},
		{Key: "GALAXY_IGNORE_CERTS", Category: "jobs", Type: "boolean", Default: false},
		{Key: "STDOUT_MAX_BYTES_DISPLAY", Category: "jobs", Type: "integer", Default: 1048576},
		{Key: "EVENT_STDOUT_MAX_BYTES_DISPLAY", Category: "jobs", Type: "integer", Default: 1024},
		{Key: "MAX_WEBSOCKET_EVENT_RATE", Category: "jobs", Type: "integer", Default: 30, Label: "Job Event Maximum Websocket Messages Per Second", Help: "Job events sent live to each browser per second; the job page fetches the rest from the API. 0: no limit."},
		{Key: "SCHEDULE_MAX_JOBS", Category: "jobs", Type: "integer", Default: 10, Label: "Maximum Scheduled Jobs", Help: "A schedule creates no job while this many jobs of its template are waiting to run."},
		{Key: "AWX_ANSIBLE_CALLBACK_PLUGINS", Category: "jobs", Type: "list", Default: []any{}},
		{Key: "DEFAULT_JOB_TIMEOUT", Category: "jobs", Type: "integer", Default: 0},
		{Key: "DEFAULT_JOB_IDLE_TIMEOUT", Category: "jobs", Type: "integer", Default: 0},
		{Key: "DEFAULT_INVENTORY_UPDATE_TIMEOUT", Category: "jobs", Type: "integer", Default: 0},
		{Key: "DEFAULT_PROJECT_UPDATE_TIMEOUT", Category: "jobs", Type: "integer", Default: 0},
		{Key: "ANSIBLE_FACT_CACHE_TIMEOUT", Category: "jobs", Type: "integer", Default: 0},
		{Key: "MAX_FORKS", Category: "jobs", Type: "integer", Default: 200, Label: "Maximum number of forks per job", Help: "Most forks a template, launch, schedule, workflow node or ad hoc command may ask for. 0: no limit."},
		{Key: "PROJECT_ALLOW_FILE_URLS", Category: "jobs", Type: "boolean", Default: false, Help: "Allow git projects to use file:// URLs or local paths on the control plane. Only enable for trusted single-tenant installs."},
		{Key: "JOB_EVENTS_HOT_DAYS", Category: "jobs", Type: "integer", Default: 14, Help: "Days job events stay in the database before they are served from the compressed archive."},
		{Key: "AWX_COLLECTIONS_ENABLED_ON_PROJECT_SYNC", Category: "jobs", Type: "boolean", Default: true},

		// authentication
		{Key: "SESSION_COOKIE_AGE", Category: "authentication", Type: "integer", Default: 1800, Label: "Idle Time Force Log Out", Help: "Seconds without activity after which a session ends (at least 60)."},
		{Key: "SESSIONS_PER_USER", Category: "authentication", Type: "integer", Default: -1, Label: "Maximum Number of Simultaneous Logged In Sessions", Help: "Logging in beyond this number ends the user's oldest session. -1: no limit."},
		{Key: "DISABLE_LOCAL_AUTH", Category: "authentication", Type: "boolean", Default: false, Help: "Disable password login for non-superusers. Superusers keep local login for break-glass access."},
		{Key: "AUTH_BASIC_ENABLED", Category: "authentication", Type: "boolean", Default: true, Label: "Enable HTTP Basic Auth", Help: "Accept a username and password in the Authorization header of API calls. The awx.awx collection uses it unless it is given a token."},
		{Key: "OAUTH2_PROVIDER", Category: "authentication", Type: "nested object", Default: map[string]any{"ACCESS_TOKEN_EXPIRE_SECONDS": 31536000000, "AUTHORIZATION_CODE_EXPIRE_SECONDS": 600, "REFRESH_TOKEN_EXPIRE_SECONDS": 2628000}},
		{Key: "ALLOW_OAUTH2_FOR_EXTERNAL_USERS", Category: "authentication", Type: "boolean", Default: false},
		{Key: "LOGIN_REDIRECT_OVERRIDE", Category: "authentication", Type: "string", Default: "", Label: "Login redirect override URL", Help: "Where the web UI sends users who are not logged in, instead of its login page (for example a single sign-on login URL). Open the UI with ?login=local to reach the local login page anyway."},

		// oidc
		{Key: "SOCIAL_AUTH_OIDC_KEY", Category: "oidc", Type: "string", Default: "", Label: "OIDC Key", Help: "The OIDC key (Client ID) from your IDP."},
		{Key: "SOCIAL_AUTH_OIDC_SECRET", Category: "oidc", Type: "string", Default: "", Secret: true, Label: "OIDC Secret"},
		{Key: "SOCIAL_AUTH_OIDC_OIDC_ENDPOINT", Category: "oidc", Type: "string", Default: "", Label: "OIDC Provider URL", Help: "The URL for your OIDC provider including the path up to /.well-known/openid-configuration"},
		{Key: "SOCIAL_AUTH_OIDC_VERIFY_SSL", Category: "oidc", Type: "boolean", Default: true, Label: "Verify OIDC Provider Certificate"},
		{Key: "OIDC_SCOPES", Category: "oidc", Type: "list", Default: []any{"openid", "profile", "email", "groups"}},
		{Key: "OIDC_USERNAME_CLAIM", Category: "oidc", Type: "string", Default: "preferred_username"},
		{Key: "OIDC_GROUPS_CLAIM", Category: "oidc", Type: "string", Default: "groups", Help: "Claim holding group names. Dotted paths are supported, e.g. realm_access.roles."},
		{Key: "OIDC_AUDIENCES", Category: "oidc", Type: "list", Default: []any{}, Help: "Accepted audiences for bearer JWTs. Defaults to the client id."},
		{Key: "OIDC_ALLOW_BEARER", Category: "oidc", Type: "boolean", Default: true, Help: "Accept access tokens issued by the OIDC provider as API bearer tokens."},
		{Key: "OIDC_LINK_EXISTING_USERS", Category: "oidc", Type: "boolean", Default: false, Help: "Allow OIDC logins to take over an existing local user with the same username."},
		{Key: "OIDC_MAPPING", Category: "oidc", Type: "list", Default: []any{}, Label: "OIDC Group Mapping",
			Help: `Rules mapping groups to access. Examples: {"group": "awx-admins", "superuser": true}, {"group": "auditors", "auditor": true}, {"group": "ops", "organization": "Ops", "role": "member"}, {"group": "ops-leads", "organization": "Ops", "team": "Leads", "role": "admin"}. Group can be a regex with "group_regex". Mappings are authoritative and re-evaluated on every login.`},

		// ui
		{Key: "CUSTOM_LOGIN_INFO", Category: "ui", Type: "string", Default: "", Public: true, Label: "Custom Login Info", Help: "Text shown on the login page, for example who to ask for an account."},
		{Key: "CUSTOM_LOGO", Category: "ui", Type: "string", Default: "", Public: true, Label: "Custom Logo", Help: "Image shown on the login page: a data URL of a GIF, PNG or JPEG image (data:image/png;base64,…)."},
		{Key: "MAX_UI_JOB_EVENTS", Category: "ui", Type: "integer", Default: 4000, Public: true, Label: "Max Job Events Retrieved by UI", Help: "Events the job output page loads at once; more can be loaded on demand, or the output downloaded. 0: all."},
		{Key: "UI_LIVE_UPDATES_ENABLED", Category: "ui", Type: "boolean", Default: true, Public: true, Label: "Enable Live Updates in the UI", Help: "Off: the web UI opens no websocket; pages show new data when reloaded."},
		{Key: "PENDO_TRACKING_STATE", Category: "ui", Type: "string", Default: "off", Public: true},

		// logging
		{Key: "LOG_AGGREGATOR_ENABLED", Category: "logging", Type: "boolean", Default: false},
		{Key: "LOG_AGGREGATOR_HOST", Category: "logging", Type: "string", Default: ""},
		{Key: "LOG_AGGREGATOR_PORT", Category: "logging", Type: "integer", Default: nil},
		{Key: "LOG_AGGREGATOR_TYPE", Category: "logging", Type: "string", Default: ""},
		{Key: "LOG_AGGREGATOR_PROTOCOL", Category: "logging", Type: "string", Default: "https"},
		{Key: "LOG_AGGREGATOR_LEVEL", Category: "logging", Type: "string", Default: "INFO"},
		{Key: "LOG_AGGREGATOR_LOGGERS", Category: "logging", Type: "list", Default: []any{"awx", "activity_stream", "job_events", "system_tracking", "broadcast_websocket"}},
	} {
		Register(d)
	}
}
