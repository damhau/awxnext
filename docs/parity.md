# AWX parity

Feature by feature comparison of awxnext with AWX 24.6, the last AWX release,
from an audit of the code on 2026-10-27 (not of the docs). Status words:

- **Done**: works as in AWX, possibly with a different implementation.
- **Partial**: present, with the gap in the notes.
- **Missing**: not there, and not ruled out by the plan.
- **Later** / **By design**: deferred or excluded by [PLAN.md](PLAN.md).

## Where it stands

The core of AWX is there and tested end to end: the `/api/v2` resource model
with AWX's filtering, OPTIONS metadata and object roles; inventories (regular,
smart, constructed) and cloud inventory sources; credentials with custom types
and every AWX secret lookup; git projects; job templates with every
launch prompt, surveys, slicing, callbacks and webhooks; ad hoc commands;
workflows with approvals and convergence; schedules; management jobs; remote
execution nodes and Kubernetes container groups; HA on PostgreSQL alone.

The gaps fall in four groups:

1. **Settings that do nothing** (fixed in roadmap M1, 2026-10-28). Every
   setting now has an effect, or says "No effect in awxnext" and why in its
   help (analytics, subscriptions, OAuth2, custom virtualenvs, and
   `LOG_AGGREGATOR_*` until M6-03). A test fails CI when a setting is neither.
2. **API endpoints that AWX clients call**: done (the bulk API, the
   dashboard, host metrics, the subscription endpoints and instance health
   checks, roadmap M2-02 to M2-06).
   Compatibility with the collection, awxkit and the Terraform provider is
   tested nightly since roadmap M2-01 ([compat/](../compat/README.md)); the
   gaps those suites found are roadmap M2-14 to M2-26.
3. **Deferred by plan**: RADIUS and TACACS+ login (LDAP, SAML, GitHub, Google and Azure AD are done, roadmap M5-02 to M5-05);
   notification types other than email and webhook
   (backup and restore, and the Helm chart, are done, roadmap M6-09 and M6-10; CyberArk, Thycotic, AWS Secrets Manager and Centrify lookups are done,
   roadmap M4-07; archive projects, roadmap M4-10).
4. **UI holes over a finished API**: a few, listed below (inventory instance
   groups, inventory source schedules, `start_at_task` and credential type
   management are done, roadmap M7-01 to M7-04).

## Fixed during the audit

| Bug | Fix |
|---|---|
| Fact cache silently broken with ansible-core 2.19+ execution environments (new `s1_<host>` / `__payload__` format) | Both formats are seeded and read; only facts a run changed are saved |
| Copying a workflow shared its approval templates with the original, so editing one renamed both | A copy gets its own approval templates |
| `PATCH users/N/` with `password: "$encrypted$"` (what reads return) set that literal password | The placeholder keeps the current password |
| Editing a deactivated user in the UI reactivated them (`is_active` was not serialized) | `is_active` is returned |
| Users from OIDC showed as local accounts with password fields | `external_account` and `auth` reflect the identity provider |
| Management jobs were offered as workflow steps in the UI but rejected by the API | Management-job nodes run, with `days`/`dry_run` from the node |
| The template page showed `/generic/` as the generic webhook URL | Shows `/webhook/` |
| The template list could not tell which templates the user may edit or copy (no capabilities on `unified_job_templates`) | Each row carries its template's own capabilities |

## Next, by priority

**P1: wrong or misleading today**

1. ~~Implement or remove the settings that do nothing~~ (done, roadmap M1-01 to M1-07).
2. ~~Notifications tab on templates, projects, inventory sources and organizations~~ (done, roadmap M1-08).
3. ~~Custom credential types use only a Jinja subset in injectors~~ (done, roadmap M1-09).
4. ~~Relaunch from the UI never asks for credential passwords~~ (done, roadmap M1-10).
5. ~~A test harness running the `awx.awx` collection and awxkit against awxnext~~ (done, roadmap M2-01: also the Terraform provider).

**P2: missing features people will hit**

6. ~~Bulk API: `bulk/job_launch`, `bulk/host_create`, `bulk/host_delete`~~ (done, roadmap M2-02).
7. ~~`?host_filter=` on `/hosts/`, `chain__` filters~~ (done, roadmap M1-13).
8. ~~Host metrics API, `host_metric_summary_monthly`~~ (done, roadmap M2-04); the page is roadmap M7-08.
9. ~~Instance group policies (percentage, minimum, list) and `max_forks`; `max_concurrent_jobs` outside container groups~~ (done, roadmap M6-01).
10. ~~External log aggregation (logstash, splunk, ...) and its test endpoint~~ (done, roadmap M6-03).
11. ~~Password policy settings; activity stream entries for job launches and for role grants made through the role assignment API~~ (done, roadmap M5-06 and M6-04).
12. ~~Dashboard endpoints (`/dashboard/`, `/dashboard/graphs/jobs/`)~~ (done, roadmap M2-03).
13. ~~Managed credential types for Insights and RHV~~ (done, roadmap M4-01); ~~CA file injection for the OpenShift/Kubernetes token type~~ (done, roadmap M4-02).
14. Project refspec is not used when a job checks out an overridden branch, so jobs on refs only the refspec fetches (pull requests) can fail.

**P3: completeness**

15. ~~Relaunch keeps launch-time labels and instance groups; workflow labels passed to nodes; labels and instance group prompts on schedules and nodes~~ (done, roadmap M3-02 and M3-03).
16. ~~Typed workflow node outputs~~ (done, roadmap M3-04).
17. ~~Named URLs for credentials and other multi-hop formats, `related.named_url`~~ (done, roadmap M2-10).
18. ~~Websocket groups `jobs-summary`, `schedules-changed` and `control-limit_reached_<user>` (subscribable, never published)~~ (done, roadmap M6-05).
19. ~~UI: credential types page, inventory source schedules, the ad hoc dialog's execution environment and passwords, node credentials and labels, EE contents view~~ (done, roadmap M7-01 to M7-07).
20. Plan items not built yet: dark mode (per-job event rate caps, S3 and Azure Blob event archives, the Helm chart and the [benchmarks against AWX](benchmarks.md) done, roadmap M6-06, M6-07, M6-10 and M6-11; saved views, starred templates and list settings kept per user on the server, roadmap M7-09; bulk actions and the column chooser on every list, roadmap M7-10; "Used by" and dependents in delete confirmations, roadmap M7-11).

## Authentication, users, organizations, RBAC

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| Password rules (`LOCAL_PASSWORD_MIN_LENGTH`, `_MIN_DIGITS`, `_MIN_UPPER`, `_MIN_SPECIAL`) | Yes | Done | Roadmap M5-06. Checked as AWX checks them, in its order and words, when a user's password is set through the API (create and update; `$encrypted$` keeps the password) and by `createsuperuser` (which AWX's Django command does not check); special means neither a letter nor a digit. Readable by every user, so the user form shows the rules and checks the password as it is typed. The container's entrypoint starts the server even when `AWXNEXT_ADMIN_PASSWORD` breaks the rules, saying so |
| Local users, superuser and system auditor | Yes | Done | `createsuperuser` CLI also resets a password. As in AWX, only superusers create users at `/users/`; organization admins create them through `organizations/N/users/` (the UI's new-user form asks for the organization), where they join it |
| Password policy settings | Yes | Missing | |
| Organizations: max_hosts, default EE, Galaxy credentials, instance groups | Yes | Done | max_hosts also stops inventory source imports. AWX's public `Ansible Galaxy` credential exists from the start, read-only |
| Organization notification attachments | Yes | Done | |
| Teams | Yes | Done | Plus nested teams |
| `teams/N/projects/`, `teams/N/credentials/`, `users/N/projects/`, `users/N/access_list/` | Yes | Done | Under a parent the requester sees, what the requester sees of the target. A user's access list is superusers, the user, and the admins of their organizations and teams |
| Object roles (`/roles/`, `object_roles`, users/teams roles, role fields per type) | Yes | Done | |
| `access_list` | Yes | Done | Each user's `direct_access` (grants on the object, directly or through a team, with the team) and `indirect_access` (organization, folder, system administrator), with the role, its object, the role fields it amounts to and whether the requester can remove it |
| Role definitions and assignments (DAB RBAC) | Yes | Done | No UI to create custom roles. `role_metadata/` lists, per type, the permissions its managed roles hold; assignments read and accept `user_ansible_id`/`team_ansible_id`/`object_ansible_id` (users, teams and organizations have an `ansible_id`); permissions are prefixed `shared.` for organizations and teams |
| Personal access tokens with read/write scope | Yes | Done | Stored hashed, shown once |
| OAuth2 applications, `/api/o/` | Deprecated | By design | Not advertised by `/api/` |
| Session login, CSRF, basic auth, `/me/` | Yes | Done | |
| OIDC login | Yes | Done | Ahead of AWX: rules re-applied at every login, bearer JWTs, dry-run endpoint |
| Organization and team maps (`SOCIAL_AUTH_ORGANIZATION_MAP`, `SOCIAL_AUTH_TEAM_MAP`, per-login maps) | Yes | Done | Roadmap M5-01. AWX's validation and its social pipeline's results: expressions null (no change), true, false, usernames, emails and `/regex/` (flags i, m, matched from the start), `remove_admins`, `remove_users` and `remove`, `organization_alias`; a team map adds to the team only. Applied with the group rules at each login, in the dry run too. As in AWX, a login with its own maps (GitHub, Google, Azure AD, SAML) uses them even when unset; OIDC uses the global maps. Deliberate differences: only memberships a login gave are removed, never manual ones, and organizations and teams are created when someone is mapped into them, not at every login. The per-login map settings come with their logins (M5-03 to M5-05) |
| LDAP login (six servers) | Yes | Done | Roadmap M5-02. AWX's `AUTH_LDAP_*` and `AUTH_LDAP_1_*` to `AUTH_LDAP_5_*` settings, validated as AWX does, tried in order before local accounts (session login and Basic auth, cached a minute for Basic auth). django-auth-ldap 4.6's behaviour: user search (or union of searches) bound as the bind DN or anonymously, or DN template; the password checked with the user's own bind; StartTLS and LDAPS; required and denied groups; the 12 group types (nested ones searched bottom up, posix by `memberUid` and `gidNumber`); `USER_ATTR_MAP`; `USER_FLAGS_BY_GROUP` (`is_superuser`, `is_system_auditor`); organization (admins, auditors, users) and team maps by group DN, re-applied at each login. Usernames are matched case-insensitively and created in lower case. `ldap_dn` and `external_account` on users, and the fields LDAP sets cannot be changed, as in AWX. The importer brings AWX's login settings (secrets decrypted) and its LDAP users. Connection options used: `OPT_NETWORK_TIMEOUT`, `OPT_X_TLS_REQUIRE_CERT`, `OPT_X_TLS_CACERTFILE`; referrals are not followed. Deliberate differences: a local account is never taken over, even with another letter case, and DNs in maps are compared as DNs rather than as lower-case strings |
| SAML login | Yes | Done | Roadmap M5-03. AWX's `SOCIAL_AUTH_SAML_*` settings, validated as AWX does, with the callback and metadata URLs read-only from `TOWER_URL_BASE`. `/sso/login/saml/?idp=<name>` (HTTP-Redirect, signed when `authnRequestsSigned`), `/sso/complete/saml/` (HTTP-POST; signed response or assertion required, encrypted assertions decrypted with the service provider key) and `/sso/metadata/saml/` (entity ID, ACS, certificate, organization, contacts; the missing settings as text until configured). Several identity providers, listed in `/api/v2/auth/` and on the login page. Users as python-social-auth makes them: uid `<idp>:<permanent id>` (`name_id` is the NameID), the attributes AWX reads by default, a username made unique with random hex, the names updated at each login. AWX's SAML pipeline at each login: organizations and teams from attributes (`ORGANIZATION_ATTR`, `TEAM_ATTR` with `team_org_map`), the SAML organization and team maps (where a null expression removes), `USER_FLAGS_BY_ATTR`. `SECURITY_CONFIG` used: `authnRequestsSigned`, `signatureAlgorithm`, `wantMessagesSigned`, `wantAssertionsSigned`, `wantAssertionsEncrypted`, `wantNameId`, `requestedAuthnContext`. The importer brings the settings and AWX's SAML users. As AWX, responses the identity provider starts are accepted; ahead of AWX, each assertion can be used once. Deliberate differences: an email already used by another account refuses the login rather than linking to it, and removals only touch the memberships SAML gave. `SP_EXTRA` and `EXTRA_DATA` have no effect; no single logout, as in AWX |
| GitHub login (GitHub, organization, team, Enterprise and its organization and team) | Yes | Done | Roadmap M5-04. AWX's `SOCIAL_AUTH_GITHUB*` settings in their six categories, callback URLs read-only from `TOWER_URL_BASE`, each login listed in `/api/v2/auth/` and on the login page once its required settings are set, as in AWX. python-social-auth's backends: OAuth2 with the `user:email,read:org` scope, `/user` then the primary email of `/user/emails` when none is public, the name split into first and last, organization and team logins refused unless `orgs/<name>/members/<login>` or `teams/<id>/members/<login>` answers 2xx ("User doesn't belong to the organization" or "to the team"). Users made as python-social-auth makes them, the names updated at each login, the login's own organization and team maps applied (not the global ones, as in AWX). A GitHub user is one account across GitHub's three logins (and across Enterprise's), found by GitHub's user ID. The importer keeps AWX's GitHub users GitHub users. Deliberate difference: an email already used by another account refuses the login instead of linking to that account (AWX's `associate_by_email` would log the GitHub user in as it) |
| Google and Azure AD login | Yes | Done | Roadmap M5-05. AWX's `SOCIAL_AUTH_GOOGLE_OAUTH2_*` and `SOCIAL_AUTH_AZUREAD_OAUTH2_*` settings, so imported AWX settings work without editing: the same callback URLs (`/sso/complete/google-oauth2/`, `/sso/complete/azuread-oauth2/`), read-only from `TOWER_URL_BASE`. python-social-auth's backends: Google with AWX's `profile` scope and the backend's `openid email profile`, `AUTH_EXTRA_ARGUMENTS` (such as `hd`), the userinfo endpoint, the email as the user ID and its local part as the username, `WHITELISTED_DOMAINS` ("Your credentials aren't allowed"); Azure AD (`common` tenant, v1 endpoints, `openid profile user_impersonation email`) with the ID token's claims, the `upn` as the user ID and the email when there is none, the name as the username. Each login's own maps, as in AWX. Built on the OAuth2 logins of M5-04 rather than on the OIDC login, which has its own callback URL and settings |
| RADIUS, TACACS+ | Yes | Missing | Not in the plan |
| Session, token and login settings | Yes | Partial | Stored but ignored, see P1 |
| UI: users, teams, organizations, access tabs | Yes | Done | |

## Inventories and credentials

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| Regular, smart and constructed inventories | Yes | Done | Smart filters compiled to SQL and checked when saved |
| Hosts, groups (nested), variables, facts | Yes | Done | `instance_id` is not set by imports. Deleting a group deletes its hosts left in no group and its children left without a parent, recursively, as AWX's `delete_recursive` |
| Inventory script endpoint | Yes | Done | `subset=sliceNofM` gives one slice of the hosts, with AWX's errors |
| Inventory instance groups, `prevent_instance_group_fallback` | Yes | Done | In the inventory form (roadmap M7-02), the groups in order (reordered in place); on constructed inventories too, whose `constructed_inventories/` serializer now has `prevent_instance_group_fallback` as AWX's does |
| Inventory copy | Yes | Done | Groups, hosts, memberships and instance groups |
| `inventories/N/job_templates/`, `groups/N/job_events/` | Yes | Done | A group's events are those of its hosts |
| Inventory labels | Yes | Done | `inventories/N/labels/` and `summary_fields.labels`; in the inventory form (roadmap M7-02) |
| Asynchronous inventory delete | Yes | Done | 202 and `pending_deletion`, then hosts are deleted in batches in the background; a second delete and launches on it are refused, as in AWX |
| Cloud sources: ec2, gce, azure_rm, vmware, openstack, satellite6, controller, terraform, openshift_virtualization | Yes | Done | AWX's per-source defaults apply: the enabled variable and value when the source sets none, the host `instance_id` variable (hosts are matched by it before their name, so a host renamed in the cloud keeps its record), and leaving out empty groups (roadmap M4-03). Values ansible-core 2.19+ marks unsafe are stored as plain values |
| Project file (scm) source | Yes | Done | `source_vars` become environment variables for the source's scripts and plugins, as Python's `str()` of each value, except names already set and `HOME`, `USER`, `_`, `TERM`, `PATH` (roadmap M4-04) |
| rhv, insights sources | Yes | Done | With AWX's Red Hat Virtualization (INI file and environment) and Insights credential types (roadmap M4-01) |
| `file` source, `inventory_import` | Yes | Done | `awxnext inventory-import` takes `awx-manage inventory_import`'s options (`--inventory-name`/`--inventory-id`, `--source`, `--overwrite`, `--overwrite-vars`, `--enabled-var`/`--enabled-value`, `--group-filter`, `--host-filter`, `--exclude-empty-groups`, `--instance-id-var`, `-v`) and records each run as an update of a `file` source, which the API does not create or update (roadmap M4-05). It runs `ansible-inventory` on the machine running the command rather than in the default execution environment's image |
| overwrite, overwrite_vars, enabled_var, host_filter, update on launch with cache | Yes | Done | |
| Inventory updates: cancel, stdout, events, notifications, schedules | Yes | Done | Notifications and schedules are in the source's menu; a source's schedules have their own page under the inventory's Sources tab (roadmap M7-03), created, edited and switched off as a template's |
| Bulk host create/delete | Yes | Done | `bulk/host_create` (each host created as through `/hosts/`, all or none) and `bulk/host_delete`, with AWX's limits, checks and messages |
| Host metrics | Yes | Done | Collected per job and cleaned up. `/host_metrics/` (list, filter, soft delete) and `/host_metric_summary_monthly/`, for superusers and auditors, as in AWX; the summary is the `host_metric_summary_monthly` management job (weekly), where AWX runs a periodic task. The Host metrics page (Admin, for superusers and auditors; roadmap M7-08) lists the hosts with their first and last automation and counts, soft deletes them (superusers), and shows the last twelve months of the summary |
| Managed credential types | Yes | Partial | All of AWX's, Insights and Red Hat Virtualization included (M4-01). The Kubernetes bearer token is injected in code as in AWX, its CA data as a file in `K8S_AUTH_SSL_CA_CERT` (M4-02); unlike AWX, `verify_ssl` without CA data keeps verifying, against the system's CAs. As in AWX, an encrypted SSH key (OpenSSH with a cipher, PKCS#8 `ENCRYPTED PRIVATE KEY`, PEM `Proc-Type: 4,ENCRYPTED`) needs its passphrase and a passphrase needs an encrypted key, checked against the stored key on update |
| Custom credential types | Yes | Done | Injectors are Jinja2 (gonja), sandboxed like AWX and checked when saved; `2 ** n` gives a float and `None` prints empty. The Credential types page (Admin, roadmap M7-04) lists the types, built-in ones read-only, and creates and edits custom types with YAML or JSON input and injector editors: injector variables no field defines are pointed out as you type, the API's errors shown under each editor, and the inputs locked once credentials use the type |
| Lookups: HashiCorp Vault KV and SSH, Azure Key Vault | Yes | Done | Vault logs in with a token, username and password, AppRole, Kubernetes or a TLS client certificate (`client_cert_public`, `client_cert_private`, `client_cert_role`), with AWX 24.6's inputs (roadmap M4-06); an auth path left at AWX's default `approle` uses the method's own mount (`userpass`, `kubernetes`, `cert`). Azure falls back to managed identity |
| Lookups: CyberArk Central Credential Provider and Conjur, Centrify Vault, Thycotic DevOps Secrets Vault and Secret Server, AWS Secrets Manager | Yes | Done | AWX 24.6's types (namespaces, inputs, metadata), test buttons and importer mapping, making the requests AWX's plugins (or their Delinea and AWS SDKs) make (roadmap M4-07). Secret Server follows python-tss-sdk 1.2 (Secret Server, not Delinea Platform); Centrify names are quoted in its query |
| Lookups: CyberArk, Conjur, Thycotic, AWS Secrets Manager, Centrify | Yes | Later | |
| Credentials: owners, input sources, test, copy, ASK | Yes | Done | |
| Execution environments: pull policy, registry auth, org scope, resolution order | Yes | Done | Plus each environment's contents (roadmap M7-07, awxnext's `execution_environments/N/contents/`): its collections and versions, ansible-core and Python versions, platform and `ansible.cfg`, read from the image's registry with the environment's registry credential, without running the image or needing a container runtime, and reused for the same digest |
| UI: inventories, hosts, groups, sources, credentials, EEs | Yes | Done | No credential types page |

## Projects, jobs and workflows

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| Git and manual projects | Yes | Done | Native Go sync, one immutable checkout per commit. A template's `current_job` is set as soon as a job is pending, and `current_update`/`last_update` are in the summaries of every template and the links of projects and inventory sources, as in AWX |
| Archive projects | Yes | Done | Roadmap M4-10. Downloaded in Go over HTTP or HTTPS with the credential's username and password (basic auth, not sent on to another host after a redirect). The revision is the archive's SHA-1, zip or tar (gzip, bzip2, xz) is told by content, and a single top-level directory is removed. Each checksum is unpacked once into its own directory, as git revisions are, so a changed archive never leaves stale files (AWX keeps them unless `scm_clean`). Deliberate differences: members outside the project fail the sync, links that leave it are skipped, and a single top-level file is kept (AWX unpacks nothing). Subversion projects: out of scope by decision (2026-10-29); Insights projects: by design |
| Branch override, refspec, submodules, delete on update, clean, update on launch with cache | Yes | Done | |
| Requirements (roles, collections) | Yes | Done | Installed in the EE at run time, cached by content |
| Content signature validation | Yes | Done | Each sync checks the `ansible-sign` GPG signature and checksum manifest (`MANIFEST.in` as distlib reads it) as AWX's verification play does, and fails without moving the project's revision. Deliberate difference: `.git` entries never count, so `prune .git` is not needed. As in AWX, job checkouts are not verified |
| Job template fields, multiple credentials, labels, instance groups, EE | Yes | Done | `MAX_FORKS` is enforced wherever forks are saved or asked for. The playbook is checked against the project unless the project allows branch override, as in AWX. The form has every field (roadmap M7-01): instance groups in order (reordered in place, the list rebuilt when the order changes), `prevent_instance_group_fallback`, `start_at_task`, and the webhook key shown and rotated. `start_at_task` can be asked at launch with awxnext's `ask_start_at_task_on_launch` (AWX has no such prompt) |
| All 16 `ask_*_on_launch` prompts | Yes | Done | |
| Launch fields the template does not prompt for | Yes | Done | Deliberate difference: refused with a 400 instead of ignored (`ignored_fields`); `IGNORE_UNPROMPTED_LAUNCH_FIELDS` gives AWX's behaviour. Empty values and the template's own values change nothing and pass (still in `ignored_fields`), so the Terraform provider can update an `aap_job` |
| `ansible_*` variables at launch (extra vars, survey questions, schedules, ad hoc) | Yes | Done | Deliberate difference: refused, as they set how Ansible connects and runs; `ALLOW_ANSIBLE_VARS_AT_LAUNCH` gives AWX's behaviour. Template, inventory and host variables are not affected |
| Surveys (all types, password encryption) | Yes | Done | Plus choices from the system, conditional questions, answer formats |
| Relaunch (all, failed hosts) | Yes | Done | Keeps the labels and instance groups chosen at launch, checked as prompts (roadmap M3-02); a sliced job's relaunch takes them from its first slice |
| `jobs/N/create_schedule/` | Yes | Done | A disabled monthly schedule of the job's template with its launch prompts, credentials, labels and instance groups |
| Job slicing | Yes | Done | |
| Job events, stdout formats, host summaries, artifacts | Yes | Done | `job_events/N/children/` and `playbook_counts` (also on job lists). `event_processing_finished` is true once a job has ended, including a job that never ran (a failed dependency, canceled while pending), and workflow jobs don't have it, as in AWX: clients wait on it |
| Ad hoc commands | Yes | Done | The dialog (roadmap M7-05) chooses the execution environment, and asks for the machine credential's passwords set to ASK (SSH password, key passphrase, privilege escalation), or for those the API reports missing |
| Workflows: edges, convergence, nested, approvals, artifacts, prompts | Yes | Done | Plus `when` conditions, loops and typed outputs (job templates declare the `set_stats` outputs their jobs must publish; see [workflows.md](workflows.md#typed-outputs)). The editor saves a step's credentials, labels and instance groups (in order) when its template prompts for them (roadmap M7-06) |
| Management-job workflow nodes | Yes | Done | Fixed during the audit |
| Node labels and instance groups prompts; workflow labels passed to nodes | Yes | Done | Saved on nodes by whoever may see the labels and use the instance groups, copied to workflow job nodes, and given to the node's job when its template prompts for them (ignored otherwise, as in AWX); a workflow job's labels are added to the jobs of nodes whose template prompts for labels (roadmap M3-03). Editable in the step editor |
| Schedules: rrule with exclusions, timezones, saved prompts, preview | Yes | Done | Labels and ordered instance groups saved as prompts (`schedules/N/labels/`, `schedules/N/instance_groups/`), by whoever may see and use them, used when the template prompts for them (roadmap M3-03), and credentials (`schedules/N/credentials/`), which replace the template's of the same type. All three editable in the schedule dialog (roadmap M7-06) |
| Notifications: email and webhook, started/success/error/approval events, custom messages | Yes | Done | Email over SMTP in clear, with STARTTLS or TLS from the start, with a login when a username and password are set, AWX's fields, defaults and messages (roadmap M3-01). The server's certificate is verified against the system's CAs; sender and recipient addresses are checked when saved. Attaching needs change on the notification too, as in AWX; `used_by` (awxnext) lists where one is attached |
| Email | Yes | Later | Roadmap M3-01 |
| Slack, PagerDuty, Twilio, Grafana, IRC, Mattermost, Rocket.Chat, AWS SNS | Yes | Out of scope | Dropped on 2026-10-27 |
| GitHub/GitLab/Bitbucket DC webhooks with status posting | Yes | Done | Plus a generic JSONPath-mapped webhook |
| Provisioning callbacks | Yes | Done | |
| Management jobs (cleanups) with schedules | Yes | Done | `host_metric_summary_monthly` missing |
| Dependencies, capacity-aware placement, `allow_simultaneous` | Yes | Done | |

## Platform and operations

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| API root, ping, config, me | Yes | Done | The version root is public with AWX's keys (`inventory`, `constructed_inventory`); `applications/` is always empty, OAuth2 applications being out of scope. `POST`/`DELETE config/`, `config/subscriptions/` and `config/attach/` answer as an upstream AWX without a subscription (its `OpenLicense`): nothing can be attached, with AWX's errors |
| Compatibility suites: `awx.awx` integration targets, `awx` CLI, Terraform provider `ansible/aap` | Yes | Done | Nightly and on demand in CI against a throwaway install over HTTPS ([compat/](../compat/README.md)); each expected failure is listed with its issue in `compat/known-failures.yml` |
| Field validation like AWX's serializers | Yes | Done | Null reads as `""` for text and choice fields and as `false` for booleans, then is validated, as the `awx.awx` modules expect. Texts clients match are AWX's: a missing object is `No Organization matches the given query.` (Django's 404), an unknown ad hoc module `"X" is not a valid choice.`, and a job's `extra_vars` are written as Python's `json.dumps` (`{"foo": "bar"}`, keys sorted) |
| OPTIONS, pagination, filtering, ordering | Yes | Done | Plain filters crossing a relation match the same related row and `chain__` ones each their own, as in AWX; `role_level` needs every permission of the role. OPTIONS answers every list, sub-list and action, `/settings/` included, with DRF's `Allow` header (also on GET; HEAD answers like GET), `object_roles`, and `id` among the POST fields of attach lists, so awxkit builds the CLI's `modify`, `delete`, `grant` and `revoke` and `awx export` works. AWX's reserved query parameters (`no_truncate`, `limit`, `validate`…) are not field filters, and `type` takes a comma-separated list of types as AWX's `TypeFilterBackend` |
| Named URLs | Yes | Done | AWX's graph: every resource it names (credentials as `<name>++<credential_type.name>+<credential_type.kind>++<organization.name>`, hosts, groups and sources through their inventory, workflow nodes by identifier, users by username, instances by hostname), `[+]` for a `+` in a value, an empty segment for a missing parent. `related.named_url` on a detail and in create and update answers, as in AWX; `NAMED_URL_FORMATS` and `NAMED_URL_GRAPH_NODES` in `settings/named-url/` |
| Browsable HTML API | Yes | Done | Replaced by `/api/docs/`: Swagger UI over the OpenAPI document, in the UI, where a signed-in user calls any endpoint with their session (account menu, API docs). DRF's own browsable pages are out of scope |
| Dashboard endpoints | Yes | Done | `/dashboard/` (deprecated, as in AWX) and `/dashboard/graphs/jobs/` with `period` and `job_type`, counting what the user sees. The graph counts per day for every period (AWX's `day` period matches only the jobs of the midnight hour). The UI's "Now" page replaces the dashboard |
| Bulk API | Yes | Done | `bulk/job_launch` creates a workflow job without a template (`is_bulk_job`), one node per job; node prompts are checked against their template like a workflow node's (AWX ignores unprompted ones), including labels and instance groups. Limits `BULK_JOB_MAX_LAUNCH`, `BULK_HOST_MAX_CREATE`, `BULK_HOST_MAX_DELETE` (settings, jobs category) |
| Activity stream | Yes | Done | Launches of jobs, workflow jobs and workflow approvals are recorded as AWX records their creation (fields, credentials and labels by name, survey passwords hidden), with the user who launched them; roles given or taken through the role assignment API are recorded as the role API records them, with the role's field and object as AWX summarizes a role, and role definitions without a role on the object on the definition (roadmap M6-04) |
| Settings API and UI | Yes | Done | Settings with no effect say so in their help ("No effect in awxnext": analytics, subscriptions, OAuth2, custom virtualenvs, `LOG_AGGREGATOR_*` until M6-03); a test keeps it that way. Null is refused ("This field may not be null.") except for the settings AWX registers with `allow_null` (`DEFAULT_EXECUTION_ENVIRONMENT`, `LOG_AGGREGATOR_HOST`, `_PORT`, `_TYPE`) |
| External log aggregation | Yes | Done | All of `LOG_AGGREGATOR_*`: AWX's loggers (`awx` for service logs, `activity_stream`, `job_events`, `system_tracking`), formatted as its LogstashFormatter (Splunk's event envelope and token header), filtered by logger and level, sent as rsyslogd does: an HTTPS/HTTP POST per message (basic authentication, certificate verification), a TCP line or a UDP datagram, retried every `LOG_AGGREGATOR_TCP_TIMEOUT` seconds. `/settings/logging/test/` and the settings page's Test button. The queue is kept in memory (`LOG_AGGREGATOR_ACTION_QUEUE_SIZE` and its discard rule; the disk settings have no effect), an HTTP 4xx answer drops the message, `broadcast_websocket` has nothing to send, and `LOG_AGGREGATOR_INDIVIDUAL_FACTS` sends a message per fact, which AWX 24.6.1 registers but ignores (roadmap M6-03) |
| Prometheus metrics | Yes | Done | AWX's database metrics and its subsystem metrics per node (task, dependency and workflow managers from the dispatcher pass and the workflow engine, dispatcher pool, callback receiver from the event writer), each node publishing its values for the others; `node`, `metric`, `dbonly` and `subsystemonly` as in AWX. Metrics of AWX's Redis queue are 0 (awxnext has none). OpenTelemetry traces added |
| Analytics, subscriptions | Yes | By design | No subscriptions, as upstream AWX: the collection's `license` and `subscriptions` modules behave as against it (tested by `compat/targets/`) |
| Instances: capacity, drain, jobs | Yes | Done | AWX's formula: forks from CPU and memory (`SYSTEM_TASK_FORKS_CPU`, `SYSTEM_TASK_FORKS_MEM`, and `SYSTEM_TASK_ABS_CPU`/`_MEM` for control nodes, read from the environment), 2 GiB set aside outside Kubernetes; `capacity_adjustment` interpolates between the CPU and memory capacities, and disabled instances have none (roadmap M6-02). `AWXNEXT_CAPACITY` and the agent's `--capacity` pin both capacities, which AWX does not offer |
| Instance health check endpoint | Yes | Done | `instances/N/health_check/` (superusers; auditors read): an execution node's agent reports its resources again, or the node is marked unavailable when it does not answer; control nodes probe the database and the projects directory, which AWX does not offer. `health_check_started` and `health_check_pending` on instances |
| Receptor mesh, peers, hop nodes, topology view | Yes | By design | Dial-back agent instead; install bundle replaced by an agent token |
| Instance group policies, `max_forks`, `max_concurrent_jobs` | Yes | Done | AWX's `apply_cluster_membership_policies` (list, then minimums, then percentages) runs when an instance joins, leaves or changes `managed_by_policy`, `hostname` or `capacity_adjustment`, and when a group's policies change. Attaching an instance lists it, detaching unlists it, so an instance the percentage selects comes back, as in AWX; `default` and `controlplane` take every instance and keep their names and policies. Both limits hold on every group, counted on its instances; a job that does not fit goes to the next group, and `default` joins the chain only when nothing else is selected, as AWX's `preferred_instance_groups`. The control task impact (`AWX_CONTROL_NODE_TASK_IMPACT`) counts only for control plane tasks (roadmap M6-01) |
| Container groups with `pod_spec_override` | Yes | Done | Push-based shim, no log tailing |
| Job isolation | Yes | Done | AWX always runs jobs in containers. awxnext's `none` runtime, on the all-in-one image, runs each job as a job user of its own (roadmap M6-13): its own directory and emptied home, no access to the server's files or other jobs' files and processes, no_new_privs, all its processes killed when it ends. Elsewhere (no helper, a Kubernetes `emptyDir`) jobs run as the server's user, with a warning; container groups and execution environments are containers |
| Websockets | Yes | Done | Every AWX group is published, to subscribers on every node: `jobs-summary` once a job's events are stored (with `final_counter`, AWX's EOF), `schedules-changed` when a schedule's next run, start or end moves (created, changed, run by the scheduler), and `control-limit_reached_<user>` when a login beyond `SESSIONS_PER_USER` ends that user's oldest sessions; the UI then signs those tabs out (roadmap M6-05) |
| Event processing | Yes | Done | Batched COPY into daily partitions, archived after N days (`JOB_EVENTS_HOT_DAYS`) as one compressed file per job, kept in the database or in S3 (or S3-compatible) or Azure Blob Storage (`JOB_EVENTS_ARCHIVE_STORAGE`, roadmap M6-07; tested against MinIO and Azurite), read back when the output is viewed and deleted with the job. Per-job rate cap (`JOB_EVENT_MAX_RATE`, awxnext's, 1000 a second): beyond it, consecutive verbose events are folded into one, stdout unchanged and counters contiguous, so a noisy job leaves others' event latency within 750 ms (p95) in a test (roadmap M6-06) |
| HA, task and dependency manager | Yes | Done | PostgreSQL only, no Redis |
| `awx-manage` commands | Yes | Done | `inventory_import` as `awxnext inventory-import`; `provision_instance`, `deprovision_instance`, `register_queue`, `unregister_queue`, `remove_from_queue` and `list_instances` with their options and output, under their names with dashes or underscores (roadmap M6-08). `provision-instance` needs `--hostname` (control nodes register themselves), and `unregister-queue` keeps `default` and `controlplane`, as the API does. Other awx-manage commands (`cleanup_jobs`...) are management jobs |
| Backup/restore, operator, Helm | Yes | Partial | `awxnext backup` and `awxnext restore` (roadmap M6-09): the database with `pg_dump`, and with a passphrase the data keys' escrow, so a backup restores under another master key; restore goes into an empty database, re-wraps the keys, migrates, marks the backed up install's nodes unavailable and queues an update of every SCM project to rebuild the checkouts (manual projects' files are not in it). Helm chart (roadmap M6-10, `deploy/helm/awxnext`, published as an OCI chart on release tags): API replicas, a migration Job before upgrades, bundled or external PostgreSQL, the master key in a kept secret, ingress, the service account and Role of container groups, an optional remote agent; CI installs it in kind, runs a job in a container group, upgrades it to two replicas and runs one again, then backs the release up and restores it into a new release, whose restored template runs. No operator |
| Operator documentation | Yes | Done | [Operator guide](operations.md) (roadmap M6-12): Compose and Helm install, upgrade, migration from AWX with the importer, high availability, the master key and its rotation, backup and restore with Compose and Kubernetes. [Configuration reference](configuration.md): every environment variable and setting, generated by `go run ./cmd/gendocs`; tests check it is current and lists every variable the code reads |
| UI: dashboard, approvals bell, instances, management jobs | Yes | Done | Redesigned dashboard; no topology |
| UI: dark mode, translations, about dialog | Yes | Missing | |

## Only in awxnext

- "Used by" on inventories, projects, credentials, execution environments and templates (`used_by/` on each, awxnext's), and in their delete confirmations: what refers to the object and what deleting it does to each, deleted with it or left without it; dependents the user cannot see are counted, not named.
- Bulk actions on every main list (templates, inventories, projects, credentials, schedules, users and teams): select a page or every row the filter matches, then launch, sync, turn schedules on or off, or delete, each object on its own with failures reported; and a column chooser per list, kept with the user.
- Saved views, starred templates and list settings kept with the user on the server (`/api/v2/me/preferences/`), in every browser; what a browser kept before is uploaded the first time.
- Execution environment contents, read from the registry: collections, ansible-core, Python and `ansible.cfg` of each image.
- Version history with diffs and one-click revert on every object.
- Folders inside organizations, with roles inherited by everything below; nested teams.
- GitOps sources applying YAML declarations atomically, with dry-run plans, pruning and locked managed objects.
- Importer reading an AWX database and re-encrypting its credentials and login settings.
- Envelope encryption of secrets with local, Vault Transit or Azure Key Vault master keys, rotation and escrow.
- Workflow `when` conditions, loops and typed node outputs; survey choices from inventories, git refs and the API, conditional questions, answer formats.
- Operator catalog (`/run`), launch page with "what will happen" and each variable's source, host × task grid, "why it failed" summary, ⌘K palette, query-language filters.
- Native git sync with per-commit checkouts and content-addressed requirements cache.
- Push-based execution for remote nodes and container groups; job event archive.
- OIDC bearer JWTs, regex group rules and mapping dry-run.
- Generic webhooks with JSONPath mapping; OpenAPI document; OpenTelemetry traces.
