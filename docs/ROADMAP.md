# Roadmap to full AWX parity

Draft for review. Built from [parity.md](parity.md) (audit of 2026-10-27) and
the UI goals in [PLAN.md](PLAN.md) that are not built yet. Once approved, each
milestone becomes a GitHub milestone and each task a GitHub issue. The task ID
(`M1-02`) goes into the issue body so the two can be matched.

## How to read it

- **Labels.** A type: `bug` (wrong or misleading today), `feature`, `compat`
  (needed by AWX clients: `awx.awx`, awxkit, Terraform) or `chore` (tests,
  docs, tooling). Plus one or more areas: `api`, `auth`, `rbac`, `settings`,
  `inventory`, `credentials`, `projects`, `jobs`, `workflows`,
  `notifications`, `instances`, `ui`, `deploy`.
- **Size**, relative: **S** about a day, **M** a few days, **L** a week or
  more (often worth splitting when it starts).
- **Every task is done only with tests and docs.** Go integration tests in
  `test/`, Vitest for UI logic, and an update to the matching row of
  `parity.md`.
- **Order.** Milestones are in priority order, but M7 (UI) has little
  dependency on the others and can run alongside them.

| Milestone | Goal | Tasks |
|---|---|---|
| [M1 Nothing misleading](#m1-nothing-misleading) | What awxnext shows or accepts does what it says | 15 |
| [M2 AWX clients work unchanged](#m2-awx-clients-work-unchanged) | Collection, awxkit and Terraform run against awxnext, proven in CI | 13 |
| [M3 Jobs, workflows and notifications](#m3-jobs-workflows-and-notifications) | The rest of AWX's run-time features, plus typed node outputs | 6 |
| [M4 Inventories, credentials and projects](#m4-inventories-credentials-and-projects) | Every inventory source, credential type, lookup and project type | 10 |
| [M5 Authentication](#m5-authentication) | Every login method AWX has, except RADIUS and TACACS+ | 6 |
| [M6 Operations and scale](#m6-operations-and-scale) | Run awxnext in production: policies, logging, Helm, backup | 12 |
| [M7 UI completeness](#m7-ui-completeness) | Every API feature reachable from the UI; the remaining UI goals | 17 |

---

## M1 Nothing misleading

Settings that are saved but ignored, filters that are silently dropped and
fields that are not checked are worse than missing features: users believe
they work. **Exit:** no setting without effect, no ignored filter, the P1
list in `parity.md` closed, and browser tests in CI.

### M1-01 Fail CI when a setting has no effect
**Labels:** chore, settings · **Size:** S

Add a test that fails when a setting declared in `internal/settings/defs.go`
is neither read by any code nor marked as accepted for compatibility only.
Compatibility-only settings (analytics, and `LOG_AGGREGATOR_*` until M6-03)
say "No effect in awxnext" in their OPTIONS help text and in the settings UI.

**Done when:** the test runs in CI and passes once M1-02 to M1-07 are merged.

### M1-02 Make the session and login settings work
**Labels:** bug, auth, settings · **Size:** M

- `SESSION_COOKIE_AGE`: sessions expire after this much inactivity.
- `SESSIONS_PER_USER`: logging in beyond the limit ends the oldest session.
- `AUTH_BASIC_ENABLED`: when off, HTTP basic auth is refused.
- `LOGIN_REDIRECT_OVERRIDE`: users who are not logged in are sent to this URL.
- `CUSTOM_LOGO` and `CUSTOM_LOGIN_INFO`: shown on the login page and returned
  where AWX returns them. `config` stops returning hard-coded login text.

**Done when:** each setting has an integration test, and the login page uses
the logo and text.

### M1-03 Make the organization admin settings work
**Labels:** bug, rbac, settings · **Size:** S

`ORG_ADMINS_CAN_SEE_ALL_USERS` (organization admins see users and teams
outside their organizations) and `MANAGE_ORGANIZATION_AUTH` (when off, only
superusers change organization membership and team roles).

**Done when:** RBAC tests cover both values of each setting.

### M1-04 Make the activity stream switches work
**Labels:** bug, settings · **Size:** S

`ACTIVITY_STREAM_ENABLED` and `ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC`
stop recording entries. Version history is a separate feature and keeps
recording.

**Done when:** tests show no entries when each switch is off.

### M1-05 Enforce `MAX_FORKS` and `SCHEDULE_MAX_JOBS`
**Labels:** bug, jobs, settings · **Size:** S

`MAX_FORKS` rejects saving or launching a template with more forks (0 means
no limit). `SCHEDULE_MAX_JOBS` stops a schedule from creating a job when that
many jobs of the same template are already waiting.

**Done when:** both limits are tested on templates, launches, slices and
schedules.

### M1-06 Make the UI and websocket settings work
**Labels:** bug, ui, settings · **Size:** S

`MAX_UI_JOB_EVENTS` caps how many events the job output page loads at once,
with a way to load more or download. `UI_LIVE_UPDATES_ENABLED` turns live
updates off. `MAX_WEBSOCKET_EVENT_RATE` throttles job events sent to each
client.

**Done when:** the hub throttle has a Go test, and the UI behaviour has
Vitest or browser tests.

### M1-07 Make the runner settings work
**Labels:** bug, jobs, settings · **Size:** S

`AWX_RUNNER_KEEPALIVE_SECONDS` keeps quiet, long-running tasks connected on
container groups and remote nodes. `AWX_ISOLATION_SHOW_PATHS` mounts the
listed host paths into the execution environment.

**Done when:** a job can read a file from a shown path, and the keepalive
interval is tested in the shim.

### M1-08 Notifications tab on templates, projects, sources and organizations
**Labels:** bug, ui, notifications · **Size:** M

The API already supports attaching notifications, but the UI has no way to do
it, and the Notifications page points to a tab that doesn't exist. Add a tab
on job templates, workflow templates (including approval events), projects,
inventory sources and organizations, with a switch per event (started,
success, failure, approval).

**Done when:** a notification can be attached and detached from each page,
and the Notifications page link lands on the tab.

### M1-09 Full Jinja2 in credential type injectors
**Labels:** bug, credentials · **Size:** M

`internal/credtypes/template.go` only understands `{{ var }}` and a few
filters. Custom types imported from AWX that use `{% if %}`, loops or other
filters fail when a job runs. Render them with gonja v2, a Jinja2-compatible
Go library, so awxnext stays a single binary. Sandbox it as AWX does, and
check templates when a type is saved.

**Done when:** every managed type, and a set of real community custom types
with conditionals and filters, render the same as in AWX.

### M1-10 Ask for credential passwords on relaunch
**Labels:** bug, jobs, ui · **Size:** M

Jobs always report `passwords_needed_to_start: []`
(`internal/resources/job_templates.go`), so relaunching a job whose
credentials ask for a password at launch fails. Relaunch GET lists the needed
passwords and POST accepts them, for jobs and workflow jobs. The UI relaunch
asks for them the same way launch does.

**Done when:** relaunching a job that needs a password works from the API and
the UI.

### M1-11 Check permissions on prompted labels and instance groups
**Labels:** bug, rbac · **Size:** S

Labels and instance groups given at launch, on schedules and on workflow
nodes are not checked against what the user may use. This is a privilege
issue: a user can run on an instance group they have no access to.

**Done when:** tests show a 403 for each place these prompts are accepted.

### M1-12 Use the project refspec for branch overrides; settle `scm_clean`
**Labels:** bug, projects · **Size:** S

A job that overrides the branch checks it out without the project's refspec,
so refs that only the refspec fetches (pull requests) fail. `scm_clean` has
no effect: either honour it, or document that per-commit checkouts are always
clean and say so in its help text.

**Done when:** a job runs on a pull request ref fetched through the refspec.

### M1-13 `host_filter`, `chain__` and `role_level` filters
**Labels:** bug, api, compat · **Size:** M

`?host_filter=` on `/hosts/` is silently ignored, so callers get every host.
`chain__` filters are combined like plain AND filters. AWX applies each one
separately, which gives different results across many-to-many relations.
`role_level` is ignored. Reuse the smart inventory compiler for
`host_filter`.

**Done when:** each filter returns the same results as AWX on the cases in
the test.

### M1-14 Small API corrections
**Labels:** bug, api · **Size:** S

- `/api/` stops advertising `/api/o/`.
- Inventory source imports respect the organization's `max_hosts`.
- Copying an inventory copies its instance groups.

**Done when:** each correction has a test.

### M1-15 Browser tests in CI
**Labels:** chore, ui · **Size:** M

No UI change has been checked in a browser so far. Add Playwright tests
against the server with embedded PostgreSQL, run in CI, covering login,
creating and launching a template, live job output, the survey and workflow
editors, copy, and the notifications tab.

**Done when:** the suite runs on every pull request, with screenshots saved
on failure.

---

## M2 AWX clients work unchanged

Compatibility is the main promise, and today nothing tests it. **Exit:** the
`awx.awx` collection, the `awx` CLI and the Terraform provider pass in CI,
apart from a short list of known exceptions that is kept up to date.

### M2-01 Compatibility test harness
**Labels:** chore, compat · **Size:** L

A CI job that starts awxnext and runs:

- the `awx.awx` integration targets from the AWX 24.6 tag;
- awxkit / `awx` CLI smoke tests;
- the acceptance tests of the `ansible/aap` Terraform provider.

It publishes a pass/fail report per module.

**Done when:** the job runs nightly and on demand, and each failing area has
its own issue.

### M2-02 Bulk API
**Labels:** compat, api · **Size:** M

`/bulk/`, `bulk/job_launch/` (which creates a workflow job, as in AWX),
`bulk/host_create/` and `bulk/host_delete/`, with the `BULK_JOB_MAX_LAUNCH`,
`BULK_HOST_MAX_CREATE` and `BULK_HOST_MAX_DELETE` limits and AWX's
permission rules.

**Done when:** the collection's `bulk_job_launch`, `bulk_host_create` and
`bulk_host_delete` modules pass.

### M2-03 Dashboard endpoints
**Labels:** compat, api · **Size:** S

`/dashboard/` and `/dashboard/graphs/jobs/` with `period` and `job_type`.

**Done when:** they return AWX's response shape, with counts filtered by
RBAC.

### M2-04 Host metrics API
**Labels:** compat, api · **Size:** M

`/host_metrics/` (list, filter, soft delete) and
`/host_metric_summary_monthly/`, plus the `host_metric_summary_monthly`
management job. The data is already collected for each job.

**Done when:** the endpoints and the management job are tested. The UI page
is M7-08.

### M2-05 License and subscription endpoints behave like upstream AWX
**Labels:** compat, api · **Size:** S

`POST`/`DELETE /config/`, `/config/subscriptions/` and `/config/attach/`
answer the way an unlicensed upstream AWX does, so the collection's `license`
and `subscriptions` modules behave the same. Analytics stays out of scope.

**Done when:** both modules give the same results as against AWX.

### M2-06 Instance health check endpoint
**Labels:** compat, instances · **Size:** S

`POST /instances/N/health_check/` runs an active check: a local probe on
control nodes, and a request to the agent on execution nodes. It updates the
instance's capacity and error fields. GET returns the last result.

**Done when:** health checks are tested on a control node and on an agent
node.

### M2-07 Missing sublists and actions
**Labels:** compat, api · **Size:** M

`teams/N/projects/`, `teams/N/credentials/`, `users/N/projects/`,
`users/N/access_list/`, `inventories/N/job_templates/`,
`groups/N/job_events/`, `job_templates/N/slice_workflow_jobs/`,
`job_events/N/children/`, `jobs/N/create_schedule/`, and the job event
`playbook_counts`.

**Done when:** each endpoint is tested, with the same RBAC as its parent
object.

### M2-08 `access_list` details
**Labels:** compat, rbac · **Size:** S

`summary_fields.direct_access` and `indirect_access` on `access_list`
entries: the role, where it comes from (organization, team, folder) and
whether the user can remove it.

**Done when:** direct, team and organization grants all show with their
origin.

### M2-09 Role definition fields
**Labels:** compat, rbac · **Size:** S

`/role_metadata/` and the `*_ansible_id` fields of the role definition and
assignment API.

**Done when:** they match AWX 24.6's responses.

### M2-10 Named URLs in every format
**Labels:** compat, api · **Size:** M

All of AWX's named URL formats, including credentials and the other formats
that span several objects. `related.named_url` on every object. The
`NAMED_URL_FORMATS` and `NAMED_URL_GRAPH_NODES` settings.

**Done when:** every resource with a named URL in AWX resolves one in
awxnext.

### M2-11 Inventory API completeness
**Labels:** compat, inventory · **Size:** S

Inventory labels, `subset=slice` on the inventory script endpoint, and
asynchronous inventory delete: 202, `pending_deletion`, then deletion in the
background.

**Done when:** each is tested, including a delete of a large inventory.

### M2-12 Prometheus metrics parity
**Labels:** compat, instances · **Size:** M

The subsystem metrics AWX exposes, mapped to awxnext's event writer,
dispatcher and task manager. `node` and `metric` query parameters on
`/api/v2/metrics/`.

**Done when:** dashboards built for AWX's metrics show data from awxnext.

### M2-13 Interactive API docs
**Labels:** feature, api · **Size:** S

Replaces DRF's browsable API, which is out of scope. Serve the existing
OpenAPI document in a browsable viewer at `/api/docs/`, using the session
login, where calls can be tried out.

**Done when:** a logged-in user can browse and call any endpoint from the
page.

---

## M3 Jobs, workflows and notifications

**Exit:** every AWX notification type works, launch-time choices are kept
everywhere, and workflow nodes can declare typed outputs.

### M3-01 Email notifications
**Labels:** feature, notifications · **Size:** M

SMTP with TLS or SSL, authentication, sender, recipients and timeout, AWX's
inputs and custom messages. The test send works.

**Done when:** sends are tested against an SMTP server started by the test.

### M3-02 Slack notifications
**Labels:** feature, notifications · **Size:** S

Token, channels, hex colour, custom messages, as in AWX.

**Done when:** tested against a fake Slack API.

### M3-03 Remaining notification types
**Labels:** feature, notifications · **Size:** M

One checklist item per type: PagerDuty, Twilio, Grafana, IRC, Mattermost,
Rocket.Chat, AWS SNS. Each uses AWX's inputs, so the importer and the
collection work without changes.

**Done when:** each type has a test send against a fake endpoint.

### M3-04 Relaunch keeps launch-time labels and instance groups
**Labels:** feature, jobs · **Size:** S

Relaunching a job reuses the labels and instance groups chosen when it was
launched.

**Done when:** tested for jobs and workflow jobs.

### M3-05 Labels and instance groups through workflows and schedules
**Labels:** feature, workflows · **Size:** M

Workflow labels are passed to the nodes that ask for labels. Nodes and
schedules can store label and instance group prompts. Depends on M1-11, and
the editors get the fields too.

**Done when:** a scheduled workflow runs its nodes with the saved labels and
instance groups.

### M3-06 Typed workflow node outputs
**Labels:** feature, workflows · **Size:** L

The last phase 6 item. Templates declare outputs (name, type, required).
Artifacts from `set_stats` are checked against them, and a missing or
mistyped output fails the node with a clear message. `when` and `loop`
expressions get typed values, and the editor suggests outputs. Starts with a
short design note in `docs/workflows.md`.

**Done when:** a workflow uses a declared list output as a loop, and a wrong
type fails the node.

---

## M4 Inventories, credentials and projects

**Exit:** every AWX inventory source, managed credential type, secret lookup
and project type is available.

### M4-01 Insights and Red Hat Virtualization credential types
**Labels:** feature, credentials · **Size:** S

Both inventory sources exist, but no credential can be picked for them.

**Done when:** both sources sync with their credential, against fakes.

### M4-02 Inject the CA certificate for Kubernetes bearer tokens
**Labels:** bug, credentials · **Size:** S

The "OpenShift or Kubernetes API Bearer Token" type does not inject
`ssl_ca_cert` as a file (`K8S_AUTH_SSL_CA_CERT`).

**Done when:** a job sees the CA file and the variable.

### M4-03 Cloud source defaults and host `instance_id`
**Labels:** feature, inventory · **Size:** S

Apply AWX's per-source defaults for `enabled_var`, `enabled_value` and
`instance_id_var`, and set the host's `instance_id` on import, so hosts
renamed in the cloud are matched rather than duplicated.

**Done when:** an ec2 import keeps the same host when its name changes.

### M4-04 Inventory source variables as environment for project sources
**Labels:** bug, inventory · **Size:** S

For `scm` sources, `source_vars` become environment variables, as in AWX.

**Done when:** a script inventory from a project reads them.

### M4-05 `file` source and `inventory-import` command
**Labels:** feature, inventory · **Size:** M

An `awxnext inventory-import` command, the equivalent of `awx-manage
inventory_import`, with the same options (overwrite, overwrite vars, enabled
var, group and host filters). It records imports as the `file` source, as
AWX does.

**Done when:** it imports an INI and a YAML inventory, both with overwrite
on and off.

### M4-06 HashiCorp Vault TLS certificate authentication
**Labels:** feature, credentials · **Size:** S

`client_cert_public`, `client_cert_private` and `client_cert_role` on the
Vault lookups.

**Done when:** lookups work against a Vault started by the test with cert
auth.

### M4-07 Remaining secret lookups
**Labels:** feature, credentials · **Size:** L

One checklist item per lookup: CyberArk Central Credential Provider,
CyberArk Conjur, Thycotic DevOps Secrets Vault, Thycotic Secret Server,
Centrify Vault, AWS Secrets Manager. Check the list against AWX 24.6's
credential plugins first. Each uses AWX's inputs and metadata, has a working
test button and is mapped by the importer.

**Done when:** each lookup is tested against a fake of its API.

### M4-08 Project signature validation
**Labels:** feature, projects · **Size:** M

When a project has a signature validation credential, sync checks the
`ansible-sign` manifest and fails if a file was changed or the signature is
wrong.

**Done when:** signed, modified and unsigned projects are all tested.

### M4-09 Subversion projects
**Labels:** feature, projects · **Size:** M

SVN checkouts with credentials and revision override, cached per revision
like git.

**Done when:** sync and job runs are tested against a local SVN repository.

### M4-10 Archive projects
**Labels:** feature, projects · **Size:** S

Projects from a URL to a tar or zip archive, with optional credentials. The
revision is the archive's checksum.

**Done when:** sync, update on launch and a changed archive are tested.

---

## M5 Authentication

Moved up from "later" because LDAP and SAML are often what stops a team
moving off AWX. **Exit:** every AWX login method except RADIUS and TACACS+
works, configured with AWX's setting names so the importer carries them
over.

### M5-01 Organization and team mapping for every provider
**Labels:** feature, auth · **Size:** M

Make the OIDC mapping rules (authoritative, re-applied at each login, with a
dry run) work for any external provider. Accept AWX's
`SOCIAL_AUTH_ORGANIZATION_MAP`, `SOCIAL_AUTH_TEAM_MAP` and the
provider-specific maps as input. This is groundwork for M5-02 to M5-05.

**Done when:** AWX-style maps give the same memberships as in AWX, in tests.

### M5-02 LDAP login
**Labels:** feature, auth · **Size:** L

Up to six servers (`AUTH_LDAP_*` and `AUTH_LDAP_1_*` to `AUTH_LDAP_5_*`):
bind, user search or DN template, group types, require and deny groups, user
flags by group, organization and team maps, StartTLS. The importer maps
AWX's LDAP settings.

**Done when:** tested against an LDAP server started by the test, including
group-based maps.

### M5-03 SAML login
**Labels:** feature, auth · **Size:** L

Service provider metadata endpoint, several identity providers, attribute
mapping, organization and team maps, and AWX's `SOCIAL_AUTH_SAML_*`
settings.

**Done when:** tested against a fake identity provider, including the
metadata endpoint.

### M5-04 GitHub login
**Labels:** feature, auth · **Size:** M

The GitHub, GitHub organization, GitHub team and GitHub Enterprise
(including enterprise organization and team) variants, with AWX's settings.

**Done when:** each variant is tested against a fake GitHub API.

### M5-05 Google and Azure AD login
**Labels:** feature, auth · **Size:** S

Presets over the existing OIDC login, accepting AWX's
`SOCIAL_AUTH_GOOGLE_OAUTH2_*` and `SOCIAL_AUTH_AZUREAD_OAUTH2_*` settings.

**Done when:** imported AWX settings for both work without editing.

### M5-06 Password policy
**Labels:** feature, auth · **Size:** S

`LOCAL_PASSWORD_MIN_LENGTH`, `_MIN_DIGITS`, `_MIN_UPPER` and `_MIN_SPECIAL`,
checked on user create, update and `createsuperuser`, with the rules shown
in the UI.

**Done when:** each rule is tested through the API.

---

## M6 Operations and scale

**Exit:** awxnext can be installed, observed, scaled, backed up and upgraded
in production without reading the code.

### M6-01 Instance group policies and limits
**Labels:** feature, instances · **Size:** M

Apply `policy_instance_percentage`, `policy_instance_minimum` and
`policy_instance_list` to group membership, recomputed when instances join
or leave. Enforce `max_forks` and `max_concurrent_jobs` on every group, not
only container groups.

**Done when:** membership and placement are tested as instances join and
leave.

### M6-02 Control node capacity like AWX
**Labels:** feature, instances · **Size:** S

Measure control node capacity from CPU and memory instead of a fixed value.
`capacity_adjustment` should interpolate between CPU and memory capacity as
AWX does, instead of scaling.

**Done when:** capacity matches AWX's formula for given CPU and memory.

### M6-03 External log aggregation
**Labels:** feature, settings · **Size:** L

All of `LOG_AGGREGATOR_*`: logstash, splunk, loggly, sumologic and other;
HTTPS, TCP or UDP; the selected loggers (`awx`, `activity_stream`,
`job_events`, `system_tracking`); level; facts sent one by one; TLS
verification; and the `/settings/logging/test/` endpoint.

**Done when:** each protocol is tested against a receiver started by the
test, and the settings UI test button works.

### M6-04 Activity stream completeness
**Labels:** feature, api · **Size:** S

Record job launches, and roles granted or revoked through the role
assignment API.

**Done when:** both show in the activity stream with the user who made them.

### M6-05 Publish the remaining websocket groups
**Labels:** feature, api · **Size:** S

`jobs-summary`, `schedules-changed` and `control-limit_reached_<user>`
accept subscriptions but never send anything.

**Done when:** each group delivers its messages in a websocket test.

### M6-06 Per-job event rate caps
**Labels:** feature, jobs · **Size:** M

A PLAN.md decision that is not built: cap how fast one job can write events,
so a very verbose job cannot starve the others. Over the cap, the extra
verbose events are folded into summaries. Stdout stays complete.

**Done when:** a noisy job leaves another job's event latency within a set
bound in a benchmark.

### M6-07 Event archives in S3 and Azure Blob
**Labels:** feature, jobs, deploy · **Size:** M

The job event archive, filesystem only today, gets S3 (or S3-compatible)
and Azure Blob backends, using the existing credentials or the environment.

**Done when:** archive and read-back are tested against MinIO and Azurite.

### M6-08 Instance management commands
**Labels:** feature, instances · **Size:** S

Equivalents of the `awx-manage` commands for instances and groups:
provision, deprovision, register a queue, list instances.

**Done when:** each command is tested and documented.

### M6-09 Backup and restore
**Labels:** feature, deploy · **Size:** M

`awxnext backup` and `awxnext restore`: database, master key escrow and the
settings needed to restore. Project checkouts are rebuilt on restore.

**Done when:** CI restores a backup into an empty instance and runs a job.

### M6-10 Helm chart
**Labels:** feature, deploy · **Size:** L

API replicas, a migration job, external or bundled PostgreSQL, master key
secret, ingress, container group service account, and an optional remote
agent. Published as an OCI chart.

**Done when:** CI installs it in kind, runs a job in a container group, then
upgrades it.

### M6-11 Benchmarks against AWX
**Labels:** chore, deploy · **Size:** M

A reproducible script that measures event ingestion rate, list latency at
scale and launch throughput, against AWX 24.6 on the same hardware. Results
go in the docs.

**Done when:** the numbers are published and can be reproduced from the
repository.

### M6-12 Operator documentation
**Labels:** chore, deploy · **Size:** M

Install with Compose and Helm, upgrade, migration from AWX with the
importer, HA, the master key and its rotation, and a configuration reference
generated from the settings definitions.

**Done when:** a new user can install, migrate and upgrade from the docs
alone.

---

## M7 UI completeness

**Exit:** every API feature can be used from the UI, and the remaining UI
goals in PLAN.md are met.

### M7-01 Missing fields on the template forms
**Labels:** feature, ui · **Size:** S

Instance groups (ordered), `prevent_instance_group_fallback`,
`start_at_task` (including the launch prompt), and showing and rotating the
webhook key.

**Done when:** each field saves and reloads, as checked by browser tests.

### M7-02 Missing fields on the inventory form
**Labels:** feature, ui · **Size:** S

Instance groups, `prevent_instance_group_fallback`, and labels (after
M2-11).

**Done when:** each field saves and reloads.

### M7-03 Schedules tab on inventory sources
**Labels:** feature, ui · **Size:** S

The API supports schedules on inventory sources, but the UI has no tab for
them.

**Done when:** a source schedule can be created, edited and switched off.

### M7-04 Credential types page
**Labels:** feature, ui, credentials · **Size:** M

List the types. Create and edit custom types with input and injector
editors, validated by the API. Managed types are read-only.

**Done when:** a custom type made in the UI is used by a credential and a
job.

### M7-05 Execution environment and passwords in the ad hoc dialog
**Labels:** feature, ui · **Size:** S

The ad hoc command dialog has no execution environment choice and does not
ask for credential passwords.

**Done when:** an ad hoc command runs with a chosen EE and an ASK password.

### M7-06 Credentials and labels on workflow nodes
**Labels:** feature, ui, workflows · **Size:** S

The workflow editor does not offer the credential and label prompts of a
node, or instance groups (after M3-05).

**Done when:** they save, reload and are used at run time.

### M7-07 Execution environment contents
**Labels:** feature, ui · **Size:** M

Promised in PLAN.md. Show an EE's collections, ansible-core and Python
versions, and `ansible.cfg`. They are read once per image digest by running
the image, then cached.

**Done when:** the EE page lists the contents of `awx-ee`.

### M7-08 Host metrics page
**Labels:** feature, ui · **Size:** S

After M2-04: automated hosts with their first and last automation dates and
counts, a monthly summary, and soft delete.

**Done when:** the page lists hosts automated by test jobs.

### M7-09 Saved views and stars stored on the server
**Labels:** feature, ui, api · **Size:** M

Saved views, starred templates and list preferences are kept per user on the
server instead of in the browser. What a browser already stores is uploaded
the first time.

**Done when:** a saved view shows up in a second browser.

### M7-10 Bulk actions on every list; column chooser
**Labels:** feature, ui · **Size:** M

Turn the template bulk bar into a shared component. Use it on inventories,
projects, credentials, schedules (turn on and off) and users. Add a column
chooser to the lists.

**Done when:** each list supports select all and its bulk actions, as
checked by browser tests.

### M7-11 "Used by" everywhere, and before delete
**Labels:** feature, ui · **Size:** M

Credentials and projects have a "Used by" panel. Add one to inventories,
execution environments and templates (the workflows and schedules that use
them), and list those dependents in the delete confirmation.

**Done when:** deleting something that is in use says what it is used by.

### M7-12 Diff view for changed tasks
**Labels:** feature, ui, jobs · **Size:** S

Job output shows the before and after diff of changed tasks when a job ran
with diff mode on.

**Done when:** a job with `diff_mode` shows the diff of a template task.

### M7-13 Dark mode
**Labels:** feature, ui · **Size:** M

Follows the system setting, with a manual switch, built on colour tokens
across the whole app. The job output terminal is already dark.

**Done when:** every page passes a contrast check in both themes.

### M7-14 Responsive layout
**Labels:** feature, ui · **Size:** M

The UI has two media queries. Make approvals, job output, the operator
catalog and launch usable on a phone, and the rest usable on a tablet.

**Done when:** browser tests run those pages at phone width.

### M7-15 About dialog and keyboard shortcut help
**Labels:** feature, ui · **Size:** S

Version, build and AWX compatibility version. A `?` overlay lists the
shortcuts.

**Done when:** both open from the user menu and the keyboard.

### M7-16 Translations
**Labels:** feature, ui · **Size:** L

Make the UI translatable: extract strings and format dates and numbers by
locale. Ship English plus one language as a check, and document how to add
more. AWX ships es, fr, ja, ko, nl and zh.

**Done when:** the second language covers every page, and CI reports any
untranslated string.

### M7-17 Custom role definitions
**Labels:** feature, ui, rbac · **Size:** M

The role definition API supports custom roles, but the UI cannot create
them. Add a page to create and edit them (pick permissions by type) and
offer them in the access tabs.

**Done when:** a custom role made in the UI can be granted and gives exactly
its permissions.

---

## Proposed out of scope

These stay out unless you want any of them back:

| Item | Why |
|---|---|
| OAuth2 applications and `/api/o/` | Deprecated in AWX 24. Personal tokens and OIDC bearer tokens replace them. |
| Receptor mesh, hop nodes, peers, topology view, install bundles | The dial-back agent replaces them. |
| Automation Analytics, real subscription management | Red Hat services. The endpoints only answer like upstream AWX (M2-05). |
| Insights projects, custom virtualenvs, isolated nodes | Removed or deprecated in AWX itself. |
| AWX operator | The Helm chart (M6-10) covers Kubernetes installs. |
| DRF browsable HTML API | Replaced by interactive OpenAPI docs (M2-13). |
| Kubelet log tailing for container groups | Push-based execution is what fixes AWX's dropped-log problem. |
| RADIUS and TACACS+ login | Rarely used. Left out by decision on 2026-10-27. |

## Decisions

Taken on 2026-10-27:

1. Credential type injectors are rendered with gonja v2, in Go (M1-09).
2. The Terraform provider tested is `ansible/aap` (M2-01).
3. The milestone order stays as above.
4. RADIUS and TACACS+ are dropped.

## Moving it to GitHub

After approval, using `gh` on `damhau/awxnext`:

1. Create the labels: types, areas and `size:S`, `size:M`, `size:L`.
2. Create one milestone per section, described by its goal and exit line.
3. Create one issue per task, in order. The title is the task title. The
   body has the ID, the description and a **Done when** checklist. Tasks with
   several parts (M3-03, M4-07) get one checkbox per item.
4. Link `parity.md` and this file from the milestones, then keep this file as
   the index, or remove it once the issues exist.
