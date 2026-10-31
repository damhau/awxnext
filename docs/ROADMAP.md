# Roadmap to full AWX parity

Approved on 2026-10-27. Built from [parity.md](parity.md) (audit of
2026-10-27) and the UI goals in [PLAN.md](PLAN.md) that are not built yet.
Each milestone is a GitHub milestone and each task a GitHub issue, linked
after its labels; the task ID (`M1-02`) is in the issue body so the two can
be matched.

## How to read it

- **Labels.** A type: `bug` (wrong or misleading today), `security` (a way
  to gain access or rights a user should not have), `feature`, `compat`
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
| [M1 Nothing misleading](#m1-nothing-misleading) | What awxnext shows or accepts does what it says | 17 |
| [M2 AWX clients work unchanged](#m2-awx-clients-work-unchanged) | Collection, awxkit and Terraform run against awxnext, proven in CI | 26 |
| [M3 Jobs, workflows and notifications](#m3-jobs-workflows-and-notifications) | Email notifications, the rest of AWX's run-time features, typed node outputs | 4 |
| [M4 Inventories, credentials and projects](#m4-inventories-credentials-and-projects) | Every inventory source, credential type, lookup and project type (except Subversion) | 9 |
| [M5 Authentication](#m5-authentication) | Every login method AWX has, except RADIUS and TACACS+ | 6 |
| [M6 Operations and scale](#m6-operations-and-scale) | Run awxnext in production: policies, logging, Helm, backup, job isolation | 13 |
| [M7 UI completeness](#m7-ui-completeness) | Every API feature reachable from the UI; the remaining UI goals | 17 |
| [M8 Settings that make sense](#m8-settings-that-make-sense) | Settings laid out by task, without JSON or AWX's jargon; the settings API unchanged | 9 |

---

## M1 Nothing misleading

Settings that are saved but ignored, filters that are silently dropped and
fields that are not checked are worse than missing features: users believe
they work. **Exit:** no setting without effect, no ignored filter or launch
field, no launch input that reaches Ansible's connection settings, and the
P1 list in `parity.md` closed.

### M1-01 Fail CI when a setting has no effect
**Labels:** chore, settings · **Size:** S · [#1](https://github.com/damhau/awxnext/issues/1)

Add a test that fails when a setting declared in `internal/settings/defs.go`
is neither read by any code nor marked as accepted for compatibility only.
Compatibility-only settings (analytics, and `LOG_AGGREGATOR_*` until M6-03)
say "No effect in awxnext" in their OPTIONS help text and in the settings UI.

**Done when:** the test runs in CI and passes once M1-02 to M1-07 are merged.

### M1-02 Make the session and login settings work
**Labels:** bug, auth, settings · **Size:** M · [#2](https://github.com/damhau/awxnext/issues/2)

- `SESSION_COOKIE_AGE`: sessions expire after this much inactivity.
- `SESSIONS_PER_USER`: logging in beyond the limit ends the oldest session.
- `AUTH_BASIC_ENABLED`: when off, HTTP basic auth is refused.
- `LOGIN_REDIRECT_OVERRIDE`: users who are not logged in are sent to this URL.
- `CUSTOM_LOGO` and `CUSTOM_LOGIN_INFO`: shown on the login page and returned
  where AWX returns them. `config` stops returning hard-coded login text.

**Done when:** each setting has an integration test, and the login page uses
the logo and text.

### M1-03 Make the organization admin settings work
**Labels:** bug, rbac, settings · **Size:** S · [#3](https://github.com/damhau/awxnext/issues/3)

`ORG_ADMINS_CAN_SEE_ALL_USERS` (organization admins see users and teams
outside their organizations) and `MANAGE_ORGANIZATION_AUTH` (when off, only
superusers change organization membership and team roles).

**Done when:** RBAC tests cover both values of each setting.

### M1-04 Make the activity stream switches work
**Labels:** bug, settings · **Size:** S · [#4](https://github.com/damhau/awxnext/issues/4)

`ACTIVITY_STREAM_ENABLED` and `ACTIVITY_STREAM_ENABLED_FOR_INVENTORY_SYNC`
stop recording entries. Version history is a separate feature and keeps
recording.

**Done when:** tests show no entries when each switch is off.

### M1-05 Enforce `MAX_FORKS` and `SCHEDULE_MAX_JOBS`
**Labels:** bug, jobs, settings · **Size:** S · [#5](https://github.com/damhau/awxnext/issues/5)

`MAX_FORKS` rejects saving or launching a template with more forks (0 means
no limit). `SCHEDULE_MAX_JOBS` stops a schedule from creating a job when that
many jobs of the same template are already waiting.

**Done when:** both limits are tested on templates, launches, slices and
schedules.

### M1-06 Make the UI and websocket settings work
**Labels:** bug, ui, settings · **Size:** S · [#6](https://github.com/damhau/awxnext/issues/6)

`MAX_UI_JOB_EVENTS` caps how many events the job output page loads at once,
with a way to load more or download. `UI_LIVE_UPDATES_ENABLED` turns live
updates off. `MAX_WEBSOCKET_EVENT_RATE` throttles job events sent to each
client.

**Done when:** the hub throttle has a Go test, and the UI behaviour has
Vitest tests.

### M1-07 Make the runner settings work
**Labels:** bug, jobs, settings · **Size:** S · [#7](https://github.com/damhau/awxnext/issues/7)

`AWX_RUNNER_KEEPALIVE_SECONDS` keeps quiet, long-running tasks connected on
container groups and remote nodes. `AWX_ISOLATION_SHOW_PATHS` mounts the
listed host paths into the execution environment.

**Done when:** the container command and job pods mount the shown paths
(jobs on runtime `none` see the host already), and the keepalive interval is
tested in the shim.

### M1-08 Notifications tab on templates, projects, sources and organizations
**Labels:** bug, ui, notifications · **Size:** M · [#8](https://github.com/damhau/awxnext/issues/8)

The API already supports attaching notifications, but the UI has no way to do
it, and the Notifications page points to a tab that doesn't exist. Add a tab
on job templates, workflow templates (including approval events), projects,
inventory sources and organizations, with a switch per event (started,
success, failure, approval).

**Done when:** a notification can be attached and detached from each page,
and the Notifications page link lands on the tab.

### M1-09 Full Jinja2 in credential type injectors
**Labels:** bug, credentials · **Size:** M · [#9](https://github.com/damhau/awxnext/issues/9)

`internal/credtypes/template.go` only understands `{{ var }}` and a few
filters. Custom types imported from AWX that use `{% if %}`, loops or other
filters fail when a job runs. Render them with gonja v2, a Jinja2-compatible
Go library, so awxnext stays a single binary. Sandbox it as AWX does, and
check templates when a type is saved.

**Done when:** every managed type, and a set of real community custom types
with conditionals and filters, render the same as in AWX.

### M1-10 Ask for credential passwords on relaunch
**Labels:** bug, jobs, ui · **Size:** M · [#10](https://github.com/damhau/awxnext/issues/10)

Jobs always report `passwords_needed_to_start: []`
(`internal/resources/job_templates.go`), so relaunching a job whose
credentials ask for a password at launch fails. Relaunch GET lists the needed
passwords and POST accepts them, for jobs and workflow jobs. The UI relaunch
asks for them the same way launch does.

**Done when:** relaunching a job that needs a password works from the API and
the UI.

### M1-11 Check permissions on prompted labels and instance groups
**Labels:** bug, rbac · **Size:** S · [#11](https://github.com/damhau/awxnext/issues/11)

Labels and instance groups given at launch, on schedules and on workflow
nodes are not checked against what the user may use. This is a privilege
issue: a user can run on an instance group they have no access to.

**Done when:** tests show a 403 for each place these prompts are accepted.

### M1-12 Use the project refspec for branch overrides; settle `scm_clean`
**Labels:** bug, projects · **Size:** S · [#12](https://github.com/damhau/awxnext/issues/12)

A job that overrides the branch checks it out without the project's refspec,
so refs that only the refspec fetches (pull requests) fail. `scm_clean` has
no effect: either honour it, or document that per-commit checkouts are always
clean and say so in its help text.

**Done when:** a job runs on a pull request ref fetched through the refspec.

### M1-13 `host_filter`, `chain__` and `role_level` filters
**Labels:** bug, api, compat · **Size:** M · [#13](https://github.com/damhau/awxnext/issues/13)

`?host_filter=` on `/hosts/` is silently ignored, so callers get every host.
`chain__` filters are combined like plain AND filters. AWX applies each one
separately, which gives different results across many-to-many relations.
`role_level` is ignored. Reuse the smart inventory compiler for
`host_filter`.

**Done when:** each filter returns the same results as AWX on the cases in
the test.

### M1-14 Small API corrections
**Labels:** bug, api · **Size:** S · [#14](https://github.com/damhau/awxnext/issues/14)

- `/api/` stops advertising `/api/o/`.
- Inventory source imports respect the organization's `max_hosts`.
- Copying an inventory copies its instance groups.

**Done when:** each correction has a test.

### M1-15 Refuse Ansible connection variables from launchers
**Labels:** security, jobs, workflows, api · **Size:** M · [#77](https://github.com/damhau/awxnext/issues/77)

Today, like AWX, a template that prompts for variables accepts any name at
launch, including `ansible_ssh_common_args`, `ansible_connection`,
`ansible_host` or `ansible_*_interpreter`. Extra variables override every
other variable, so whoever may launch the template can run a command where
the job connects from (for example a `ProxyCommand`), with the job's
credentials, or point those credentials at another host. A survey question
on an `ansible_*` variable opens the same door to whoever answers it.

- Launch-time variables and survey answers whose name starts with
  `ansible_` are refused with a 400 naming them, on every launch path: job
  and workflow templates, schedules (`extra_data`), relaunch and ad hoc
  commands.
- Saving a survey question on an `ansible_*` variable is refused.
- Variables set on the template itself, by those who may edit it, are not
  affected.
- New setting `ALLOW_ANSIBLE_VARS_AT_LAUNCH`, off by default, restores
  AWX's behaviour. `parity.md` lists the difference.

**Done when:** each launch path refuses `ansible_ssh_common_args` and starts
no job, the survey editor and API refuse such a question, and turning the
setting on gives AWX's behaviour back.

### M1-16 Refuse launch fields the template does not prompt for
**Labels:** security, jobs, workflows, api · **Size:** S · [#78](https://github.com/damhau/awxnext/issues/78)

Today, like AWX, a launch field the template does not prompt for is moved
to `ignored_fields` and the job starts anyway. A `limit` sent to a template
without "prompt on launch" for it runs on the whole inventory: a caller
that patches one host patches all of them.

- A launch that sends a field the template does not prompt for (`limit`,
  `inventory`, `credentials`, tags, `extra_vars`…) is refused with a 400
  listing them ("This template does not prompt for: limit"), and no job is
  created. Same for schedules and workflow templates.
- New setting `IGNORE_UNPROMPTED_LAUNCH_FIELDS`, off by default, restores
  AWX's behaviour (ignore them and report `ignored_fields`).

**Done when:** a launch with an unprompted `limit` returns 400 and creates
no job, the setting restores `ignored_fields`, and the M2 client tests pass
(with the setting on where a client relies on it).

### M1-17 Keep the server's secrets out of reach of jobs on runtime `none`
**Labels:** security, jobs, deploy · **Size:** S · [#79](https://github.com/damhau/awxnext/issues/79)

With `AWXNEXT_CONTAINER_RUNTIME=none` (the default of the all-in-one image)
jobs run as the server's own user. Checked on 2026-10-27: such a process
reads `AWXNEXT_SECRET_KEY` and `AWXNEXT_DATABASE_URL` from the server's
`/proc/<pid>/environ`, which is enough to decrypt every credential. Any
playbook can do it (a task on `localhost` runs beside the server), and with
M1-15 open, so can anyone allowed to launch a template that prompts for
variables.

- The server makes itself non-dumpable at start (`PR_SET_DUMPABLE`), so its
  environment and memory are not readable by other processes of its user.
- The README says plainly that runtime `none` runs playbooks with the
  server's privileges and is for one trusted team; the server logs a
  warning at start when it is used.

**Done when:** a test job on runtime `none` cannot read the server's
environment, and the README and the start-up warning are in place.

---

## M2 AWX clients work unchanged

Compatibility is the main promise, and today nothing tests it. **Exit:** the
`awx.awx` collection, the `awx` CLI and the Terraform provider pass in CI,
apart from a short list of known exceptions that is kept up to date.

### M2-01 Compatibility test harness
**Labels:** chore, compat · **Size:** L · [#15](https://github.com/damhau/awxnext/issues/15)

A CI job that starts awxnext and runs:

- the `awx.awx` integration targets from the AWX 24.6 tag;
- awxkit / `awx` CLI smoke tests;
- the acceptance tests of the `ansible/aap` Terraform provider.

It publishes a pass/fail report per module.

**Done when:** the job runs nightly and on demand, and each failing area has
its own issue.

### M2-02 Bulk API
**Labels:** compat, api · **Size:** M · [#16](https://github.com/damhau/awxnext/issues/16)

`/bulk/`, `bulk/job_launch/` (which creates a workflow job, as in AWX),
`bulk/host_create/` and `bulk/host_delete/`, with the `BULK_JOB_MAX_LAUNCH`,
`BULK_HOST_MAX_CREATE` and `BULK_HOST_MAX_DELETE` limits and AWX's
permission rules.

**Done when:** the collection's `bulk_job_launch`, `bulk_host_create` and
`bulk_host_delete` modules pass.

### M2-03 Dashboard endpoints
**Labels:** compat, api · **Size:** S · [#17](https://github.com/damhau/awxnext/issues/17)

`/dashboard/` and `/dashboard/graphs/jobs/` with `period` and `job_type`.

**Done when:** they return AWX's response shape, with counts filtered by
RBAC.

### M2-04 Host metrics API
**Labels:** compat, api · **Size:** M · [#18](https://github.com/damhau/awxnext/issues/18)

`/host_metrics/` (list, filter, soft delete) and
`/host_metric_summary_monthly/`, plus the `host_metric_summary_monthly`
management job. The data is already collected for each job.

**Done when:** the endpoints and the management job are tested. The UI page
is M7-08.

### M2-05 License and subscription endpoints behave like upstream AWX
**Labels:** compat, api · **Size:** S · [#19](https://github.com/damhau/awxnext/issues/19)

`POST`/`DELETE /config/`, `/config/subscriptions/` and `/config/attach/`
answer the way an unlicensed upstream AWX does, so the collection's `license`
and `subscriptions` modules behave the same. Analytics stays out of scope.

**Done when:** both modules give the same results as against AWX.

### M2-06 Instance health check endpoint
**Labels:** compat, instances · **Size:** S · [#20](https://github.com/damhau/awxnext/issues/20)

`POST /instances/N/health_check/` runs an active check: a local probe on
control nodes, and a request to the agent on execution nodes. It updates the
instance's capacity and error fields. GET returns the last result.

**Done when:** health checks are tested on a control node and on an agent
node.

### M2-07 Missing sublists and actions
**Labels:** compat, api · **Size:** M · [#21](https://github.com/damhau/awxnext/issues/21)

`teams/N/projects/`, `teams/N/credentials/`, `users/N/projects/`,
`users/N/access_list/`, `inventories/N/job_templates/`,
`groups/N/job_events/`, `job_templates/N/slice_workflow_jobs/`,
`job_events/N/children/`, `jobs/N/create_schedule/`, and the job event
`playbook_counts`.

**Done when:** each endpoint is tested, with the same RBAC as its parent
object.

### M2-08 `access_list` details
**Labels:** compat, rbac · **Size:** S · [#22](https://github.com/damhau/awxnext/issues/22)

`summary_fields.direct_access` and `indirect_access` on `access_list`
entries: the role, where it comes from (organization, team, folder) and
whether the user can remove it.

**Done when:** direct, team and organization grants all show with their
origin.

### M2-09 Role definition fields
**Labels:** compat, rbac · **Size:** S · [#23](https://github.com/damhau/awxnext/issues/23)

`/role_metadata/` and the `*_ansible_id` fields of the role definition and
assignment API.

**Done when:** they match AWX 24.6's responses.

### M2-10 Named URLs in every format
**Labels:** compat, api · **Size:** M · [#24](https://github.com/damhau/awxnext/issues/24)

All of AWX's named URL formats, including credentials and the other formats
that span several objects. `related.named_url` on every object. The
`NAMED_URL_FORMATS` and `NAMED_URL_GRAPH_NODES` settings.

**Done when:** every resource with a named URL in AWX resolves one in
awxnext.

### M2-11 Inventory API completeness
**Labels:** compat, inventory · **Size:** S · [#25](https://github.com/damhau/awxnext/issues/25)

Inventory labels, `subset=slice` on the inventory script endpoint, and
asynchronous inventory delete: 202, `pending_deletion`, then deletion in the
background.

**Done when:** each is tested, including a delete of a large inventory.

### M2-12 Prometheus metrics parity
**Labels:** compat, instances · **Size:** M · [#26](https://github.com/damhau/awxnext/issues/26)

The subsystem metrics AWX exposes, mapped to awxnext's event writer,
dispatcher and task manager. `node` and `metric` query parameters on
`/api/v2/metrics/`.

**Done when:** dashboards built for AWX's metrics show data from awxnext.

### M2-13 Interactive API docs
**Labels:** feature, api · **Size:** S · [#27](https://github.com/damhau/awxnext/issues/27)

Replaces DRF's browsable API, which is out of scope. Serve the existing
OpenAPI document in a browsable viewer at `/api/docs/`, using the session
login, where calls can be tried out.

**Done when:** a logged-in user can browse and call any endpoint from the
page.

Found by the compatibility harness (M2-01):

### M2-14 Null for text, choice and boolean fields, as in AWX
**Labels:** compat, api · **Size:** S · [#81](https://github.com/damhau/awxnext/issues/81)

The `awx.awx` modules send `null` for every option left unset. AWX reads
null as `""` for text and choice fields and as `false` for booleans, then
validates the result; awxnext refused it, which broke `demo_data` and every
target that creates an inventory. Create-only fields compare after that
coercion.

**Done when:** null inputs are tested on create and update, including a
required name and a choice without an empty value.

### M2-15 AWX's public Galaxy credential
**Labels:** compat, credentials · **Size:** S · [#82](https://github.com/damhau/awxnext/issues/82)

The managed `Ansible Galaxy` credential (`https://galaxy.ansible.com/`) that
organizations add by name, created on the first start and read-only like
AWX's managed credentials.

**Done when:** it exists on a new install, can be added to an organization,
and cannot be changed or deleted.

### M2-16 Public API root with AWX's names
**Labels:** compat, api · **Size:** S · [#83](https://github.com/damhau/awxnext/issues/83)

`/api/v2/` is public, as the `awx` CLI reads it before logging in, and uses
AWX's keys (`inventory`, `constructed_inventory`, `applications`).
`/applications/` answers an empty list marked deprecated: OAuth2
applications stay out of scope.

**Done when:** an anonymous `GET /api/v2/` lists AWX's keys, and `awx
import` works.

### M2-17 OPTIONS like AWX: `Allow` header, `object_roles`, every list
**Labels:** compat, api · **Size:** S · [#84](https://github.com/damhau/awxnext/issues/84)

awxkit builds the CLI's `modify`, `delete`, `grant` and `revoke` and its
export and import from OPTIONS: the `Allow` header, `object_roles`, and an
OPTIONS answer on every list, `/settings/` included.

**Done when:** those CLI commands and `awx export` pass in the compat suite.

### M2-18 Workflow jobs without `event_processing_finished`
**Labels:** compat, workflows · **Size:** S · [#85](https://github.com/damhau/awxnext/issues/85)

AWX leaves the field off workflow jobs. awxnext returns it as `false`
forever, so the collection's wait on a workflow job never ends.

**Done when:** the collection's `job_wait` and `workflow_launch` targets
pass.

### M2-19 `current_update` and `last_update` on projects and inventory sources
**Labels:** compat, projects · **Size:** S · [#86](https://github.com/damhau/awxnext/issues/86)

In `summary_fields` and `related`, next to `current_job` and `last_job`. The
project module waits on `current_update` after creating a project.

**Done when:** the fields have AWX's shape, and the collection's `project`
target passes.

### M2-20 Encrypted SSH keys need their passphrase
**Labels:** bug, credentials · **Size:** S · [#87](https://github.com/damhau/awxnext/issues/87)

As in AWX: `ssh_key_unlock` must be set when `ssh_key_data` is encrypted
(PEM, PKCS#8 or OpenSSH with a cipher), and must not be set when it is not.
`$encrypted$` is checked against the stored key.

**Done when:** each key format is tested encrypted and not, on create and
update.

### M2-21 Settings refuse null like AWX
**Labels:** compat, settings · **Size:** S · [#88](https://github.com/damhau/awxnext/issues/88)

Null for a setting that is not nullable is a 400 (the collection's
`settings` module sends null for an omitted value).

**Done when:** it changes nothing, and the collection's `settings` target
passes.

### M2-22 Only superusers create users at `/users/`
**Labels:** bug, rbac · **Size:** S · [#89](https://github.com/damhau/awxnext/issues/89)

Organization admins create users through `organizations/N/users/`, as in
AWX, not at `/users/` where they would belong to no organization.

**Done when:** an organization admin gets 403 on `POST /users/` and can
still create users through their organization.

### M2-23 Deleting a group deletes its children as in AWX
**Labels:** compat, inventory · **Size:** S · [#90](https://github.com/damhau/awxnext/issues/90)

AWX's `delete_recursive`: the group and its hosts in no other group, then
each child group left without a parent, recursively.

**Done when:** the cases are tested, including children and hosts shared
with another group, which stay.

### M2-24 Text that clients match, written as AWX writes it
**Labels:** compat, api · **Size:** S · [#91](https://github.com/damhau/awxnext/issues/91)

A job's `extra_vars` in Python's `json.dumps` form, `"X" is not a valid
choice.` for an unknown ad hoc module, and `No Organization matches the
given query.` for a missing object.

**Done when:** each is tested, and the matching collection targets and
Terraform tests pass.

### M2-25 `no_truncate` and AWX's other reserved query parameters
**Labels:** compat, api · **Size:** S · [#92](https://github.com/damhau/awxnext/issues/92)

`awx ... --monitor` lists events with `no_truncate=True`. AWX's reserved
parameters are never read as field filters.

**Done when:** `awx job_templates launch --monitor` and `awx jobs stdout`
pass in the compat suite.

### M2-26 Accept unprompted launch fields that change nothing
**Labels:** compat, jobs · **Size:** S · [#93](https://github.com/damhau/awxnext/issues/93)

The Terraform provider's `aap_job` sends its stored `inventory` and
`job_slice_count` again on update. A field the template does not prompt for,
sent with the template's own value, changes nothing and is accepted; a
different value is still refused (M1-16).

**Done when:** the provider's `aap_job` update tests pass with the default
settings.

---

## M3 Jobs, workflows and notifications

**Exit:** email notifications work, launch-time choices are kept
everywhere, and workflow nodes can declare typed outputs.

### M3-01 Email notifications
**Labels:** feature, notifications · **Size:** M · [#28](https://github.com/damhau/awxnext/issues/28)

SMTP with TLS or SSL, authentication, sender, recipients and timeout, AWX's
inputs and custom messages. The test send works.

**Done when:** sends are tested against an SMTP server started by the test.

### M3-02 Relaunch keeps launch-time labels and instance groups
**Labels:** feature, jobs · **Size:** S · [#29](https://github.com/damhau/awxnext/issues/29)

Relaunching a job reuses the labels and instance groups chosen when it was
launched.

**Done when:** tested for jobs and workflow jobs.

### M3-03 Labels and instance groups through workflows and schedules
**Labels:** feature, workflows · **Size:** M · [#30](https://github.com/damhau/awxnext/issues/30)

Workflow labels are passed to the nodes that ask for labels. Nodes and
schedules can store label and instance group prompts, checked like launch
prompts (visible labels, instance groups the user may use: see M1-11), and
the editors get the fields too.

**Done when:** a scheduled workflow runs its nodes with the saved labels and
instance groups.

### M3-04 Typed workflow node outputs
**Labels:** feature, workflows · **Size:** L · [#31](https://github.com/damhau/awxnext/issues/31)

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
and project type is available, except Subversion projects (dropped on
2026-10-29, see Proposed out of scope).

### M4-01 Insights and Red Hat Virtualization credential types
**Labels:** feature, credentials · **Size:** S · [#32](https://github.com/damhau/awxnext/issues/32)

Both inventory sources exist, but no credential can be picked for them.

**Done when:** both sources sync with their credential, against fakes.

### M4-02 Inject the CA certificate for Kubernetes bearer tokens
**Labels:** bug, credentials · **Size:** S · [#33](https://github.com/damhau/awxnext/issues/33)

The "OpenShift or Kubernetes API Bearer Token" type does not inject
`ssl_ca_cert` as a file (`K8S_AUTH_SSL_CA_CERT`).

**Done when:** a job sees the CA file and the variable.

### M4-03 Cloud source defaults and host `instance_id`
**Labels:** feature, inventory · **Size:** S · [#34](https://github.com/damhau/awxnext/issues/34)

Apply AWX's per-source defaults for `enabled_var`, `enabled_value` and
`instance_id_var`, and set the host's `instance_id` on import, so hosts
renamed in the cloud are matched rather than duplicated.

**Done when:** an ec2 import keeps the same host when its name changes.

### M4-04 Inventory source variables as environment for project sources
**Labels:** bug, inventory · **Size:** S · [#35](https://github.com/damhau/awxnext/issues/35)

For `scm` sources, `source_vars` become environment variables, as in AWX.

**Done when:** a script inventory from a project reads them.

### M4-05 `file` source and `inventory-import` command
**Labels:** feature, inventory · **Size:** M · [#36](https://github.com/damhau/awxnext/issues/36)

An `awxnext inventory-import` command, the equivalent of `awx-manage
inventory_import`, with the same options (overwrite, overwrite vars, enabled
var, group and host filters). It records imports as the `file` source, as
AWX does.

**Done when:** it imports an INI and a YAML inventory, both with overwrite
on and off.

### M4-06 HashiCorp Vault TLS certificate authentication
**Labels:** feature, credentials · **Size:** S · [#37](https://github.com/damhau/awxnext/issues/37)

`client_cert_public`, `client_cert_private` and `client_cert_role` on the
Vault lookups.

**Done when:** lookups work against a Vault started by the test with cert
auth.

### M4-07 Remaining secret lookups
**Labels:** feature, credentials · **Size:** L · [#38](https://github.com/damhau/awxnext/issues/38)

One checklist item per lookup: CyberArk Central Credential Provider,
CyberArk Conjur, Thycotic DevOps Secrets Vault, Thycotic Secret Server,
Centrify Vault, AWS Secrets Manager. Check the list against AWX 24.6's
credential plugins first. Each uses AWX's inputs and metadata, has a working
test button and is mapped by the importer.

**Done when:** each lookup is tested against a fake of its API.

### M4-08 Project signature validation
**Labels:** feature, projects · **Size:** M · [#39](https://github.com/damhau/awxnext/issues/39)

When a project has a signature validation credential, sync checks the
`ansible-sign` manifest and fails if a file was changed or the signature is
wrong.

**Done when:** signed, modified and unsigned projects are all tested.

### M4-10 Archive projects
**Labels:** feature, projects · **Size:** S · [#41](https://github.com/damhau/awxnext/issues/41)

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
**Labels:** feature, auth · **Size:** M · [#42](https://github.com/damhau/awxnext/issues/42)

Make the OIDC mapping rules (authoritative, re-applied at each login, with a
dry run) work for any external provider. Accept AWX's
`SOCIAL_AUTH_ORGANIZATION_MAP`, `SOCIAL_AUTH_TEAM_MAP` and the
provider-specific maps as input. This is groundwork for M5-02 to M5-05.

**Done when:** AWX-style maps give the same memberships as in AWX, in tests.

### M5-02 LDAP login
**Labels:** feature, auth · **Size:** L · [#43](https://github.com/damhau/awxnext/issues/43)

Up to six servers (`AUTH_LDAP_*` and `AUTH_LDAP_1_*` to `AUTH_LDAP_5_*`):
bind, user search or DN template, group types, require and deny groups, user
flags by group, organization and team maps, StartTLS. The importer maps
AWX's LDAP settings.

**Done when:** tested against an LDAP server started by the test, including
group-based maps.

### M5-03 SAML login
**Labels:** feature, auth · **Size:** L · [#44](https://github.com/damhau/awxnext/issues/44)

Service provider metadata endpoint, several identity providers, attribute
mapping, organization and team maps, and AWX's `SOCIAL_AUTH_SAML_*`
settings.

**Done when:** tested against a fake identity provider, including the
metadata endpoint.

### M5-04 GitHub login
**Labels:** feature, auth · **Size:** M · [#45](https://github.com/damhau/awxnext/issues/45)

The GitHub, GitHub organization, GitHub team and GitHub Enterprise
(including enterprise organization and team) variants, with AWX's settings.

**Done when:** each variant is tested against a fake GitHub API.

### M5-05 Google and Azure AD login
**Labels:** feature, auth · **Size:** S · [#46](https://github.com/damhau/awxnext/issues/46)

Presets over the existing OIDC login, accepting AWX's
`SOCIAL_AUTH_GOOGLE_OAUTH2_*` and `SOCIAL_AUTH_AZUREAD_OAUTH2_*` settings.

**Done when:** imported AWX settings for both work without editing.

### M5-06 Password policy
**Labels:** feature, auth · **Size:** S · [#47](https://github.com/damhau/awxnext/issues/47)

`LOCAL_PASSWORD_MIN_LENGTH`, `_MIN_DIGITS`, `_MIN_UPPER` and `_MIN_SPECIAL`,
checked on user create, update and `createsuperuser`, with the rules shown
in the UI.

**Done when:** each rule is tested through the API.

---

## M6 Operations and scale

**Exit:** awxnext can be installed, observed, scaled, backed up and upgraded
in production without reading the code.

### M6-01 Instance group policies and limits
**Labels:** feature, instances · **Size:** M · [#48](https://github.com/damhau/awxnext/issues/48)

Apply `policy_instance_percentage`, `policy_instance_minimum` and
`policy_instance_list` to group membership, recomputed when instances join
or leave. Enforce `max_forks` and `max_concurrent_jobs` on every group, not
only container groups.

**Done when:** membership and placement are tested as instances join and
leave.

### M6-02 Control node capacity like AWX
**Labels:** feature, instances · **Size:** S · [#49](https://github.com/damhau/awxnext/issues/49)

Measure control node capacity from CPU and memory instead of a fixed value.
`capacity_adjustment` should interpolate between CPU and memory capacity as
AWX does, instead of scaling.

**Done when:** capacity matches AWX's formula for given CPU and memory.

### M6-03 External log aggregation
**Labels:** feature, settings · **Size:** L · [#50](https://github.com/damhau/awxnext/issues/50)

All of `LOG_AGGREGATOR_*`: logstash, splunk, loggly, sumologic and other;
HTTPS, TCP or UDP; the selected loggers (`awx`, `activity_stream`,
`job_events`, `system_tracking`); level; facts sent one by one; TLS
verification; and the `/settings/logging/test/` endpoint.

**Done when:** each protocol is tested against a receiver started by the
test, and the settings UI test button works.

### M6-04 Activity stream completeness
**Labels:** feature, api · **Size:** S · [#51](https://github.com/damhau/awxnext/issues/51)

Record job launches, and roles granted or revoked through the role
assignment API.

**Done when:** both show in the activity stream with the user who made them.

### M6-05 Publish the remaining websocket groups
**Labels:** feature, api · **Size:** S · [#52](https://github.com/damhau/awxnext/issues/52)

`jobs-summary`, `schedules-changed` and `control-limit_reached_<user>`
accept subscriptions but never send anything.

**Done when:** each group delivers its messages in a websocket test.

### M6-06 Per-job event rate caps
**Labels:** feature, jobs · **Size:** M · [#53](https://github.com/damhau/awxnext/issues/53)

A PLAN.md decision that is not built: cap how fast one job can write events,
so a very verbose job cannot starve the others. Over the cap, the extra
verbose events are folded into summaries. Stdout stays complete.

**Done when:** a noisy job leaves another job's event latency within a set
bound in a benchmark.

### M6-07 Event archives in S3 and Azure Blob
**Labels:** feature, jobs, deploy · **Size:** M · [#54](https://github.com/damhau/awxnext/issues/54)

The job event archive, filesystem only today, gets S3 (or S3-compatible)
and Azure Blob backends, using the existing credentials or the environment.

**Done when:** archive and read-back are tested against MinIO and Azurite.

### M6-08 Instance management commands
**Labels:** feature, instances · **Size:** S · [#55](https://github.com/damhau/awxnext/issues/55)

Equivalents of the `awx-manage` commands for instances and groups:
provision, deprovision, register a queue, list instances.

**Done when:** each command is tested and documented.

### M6-09 Backup and restore
**Labels:** feature, deploy · **Size:** M · [#56](https://github.com/damhau/awxnext/issues/56)

`awxnext backup` and `awxnext restore`: database, master key escrow and the
settings needed to restore. Project checkouts are rebuilt on restore.

**Done when:** CI restores a backup into an empty instance and runs a job.

### M6-10 Helm chart
**Labels:** feature, deploy · **Size:** L · [#57](https://github.com/damhau/awxnext/issues/57)

API replicas, a migration job, external or bundled PostgreSQL, master key
secret, ingress, container group service account, and an optional remote
agent. Published as an OCI chart.

**Done when:** CI installs it in kind, runs a job in a container group, then
upgrades it.

### M6-11 Benchmarks against AWX
**Labels:** chore, deploy · **Size:** M · [#58](https://github.com/damhau/awxnext/issues/58)

A reproducible script that measures event ingestion rate, list latency at
scale and launch throughput, against AWX 24.6 on the same hardware. Results
go in the docs.

**Done when:** the numbers are published and can be reproduced from the
repository.

### M6-12 Operator documentation
**Labels:** chore, deploy · **Size:** M · [#59](https://github.com/damhau/awxnext/issues/59)

Install with Compose and Helm, upgrade, migration from AWX with the
importer, HA, the master key and its rotation, and a configuration reference
generated from the settings definitions.

**Done when:** a new user can install, migrate and upgrade from the docs
alone.

### M6-13 Isolate jobs from each other on the process runtime
**Labels:** security, jobs, deploy · **Size:** L · [#80](https://github.com/damhau/awxnext/issues/80)

After M1-17, jobs on runtime `none` still share the server's user: a job can
read the private data directory (keys, passwords) of another job running at
the same time, and write to other projects' checkouts. Run each job under
its own user or in a sandbox (user namespaces, for example bubblewrap) that
sees only its own data directory and a read-only project, or make the
all-in-one image use a container runtime.

**Done when:** a job cannot read another running job's data directory, nor
write to a project checkout, on the all-in-one image.

---

## M7 UI completeness

**Exit:** every API feature can be used from the UI, and the remaining UI
goals in PLAN.md are met.

### M7-01 Missing fields on the template forms
**Labels:** feature, ui · **Size:** S · [#60](https://github.com/damhau/awxnext/issues/60)

Instance groups (ordered), `prevent_instance_group_fallback`,
`start_at_task` (including the launch prompt), and showing and rotating the
webhook key.

**Done when:** each field saves and reloads.

### M7-02 Missing fields on the inventory form
**Labels:** feature, ui · **Size:** S · [#61](https://github.com/damhau/awxnext/issues/61)

Instance groups, `prevent_instance_group_fallback`, and labels (after
M2-11).

**Done when:** each field saves and reloads.

### M7-03 Schedules tab on inventory sources
**Labels:** feature, ui · **Size:** S · [#62](https://github.com/damhau/awxnext/issues/62)

The API supports schedules on inventory sources, but the UI has no tab for
them.

**Done when:** a source schedule can be created, edited and switched off.

### M7-04 Credential types page
**Labels:** feature, ui, credentials · **Size:** M · [#63](https://github.com/damhau/awxnext/issues/63)

List the types. Create and edit custom types with input and injector
editors, validated by the API. Managed types are read-only.

**Done when:** a custom type made in the UI is used by a credential and a
job.

### M7-05 Execution environment and passwords in the ad hoc dialog
**Labels:** feature, ui · **Size:** S · [#64](https://github.com/damhau/awxnext/issues/64)

The ad hoc command dialog has no execution environment choice and does not
ask for credential passwords.

**Done when:** an ad hoc command runs with a chosen EE and an ASK password.

### M7-06 Credentials and labels on workflow nodes
**Labels:** feature, ui, workflows · **Size:** S · [#65](https://github.com/damhau/awxnext/issues/65)

The workflow editor does not offer the credential and label prompts of a
node, or instance groups (after M3-03).

**Done when:** they save, reload and are used at run time.

### M7-07 Execution environment contents
**Labels:** feature, ui · **Size:** M · [#66](https://github.com/damhau/awxnext/issues/66)

Promised in PLAN.md. Show an EE's collections, ansible-core and Python
versions, and `ansible.cfg`. They are read once per image digest by running
the image, then cached.

**Done when:** the EE page lists the contents of `awx-ee`.

### M7-08 Host metrics page
**Labels:** feature, ui · **Size:** S · [#67](https://github.com/damhau/awxnext/issues/67)

After M2-04: automated hosts with their first and last automation dates and
counts, a monthly summary, and soft delete.

**Done when:** the page lists hosts automated by test jobs.

### M7-09 Saved views and stars stored on the server
**Labels:** feature, ui, api · **Size:** M · [#68](https://github.com/damhau/awxnext/issues/68)

Saved views, starred templates and list preferences are kept per user on the
server instead of in the browser. What a browser already stores is uploaded
the first time.

**Done when:** a saved view shows up in a second browser.

### M7-10 Bulk actions on every list; column chooser
**Labels:** feature, ui · **Size:** M · [#69](https://github.com/damhau/awxnext/issues/69)

Turn the template bulk bar into a shared component. Use it on inventories,
projects, credentials, schedules (turn on and off) and users. Add a column
chooser to the lists.

**Done when:** each list supports select all and its bulk actions.

### M7-11 "Used by" everywhere, and before delete
**Labels:** feature, ui · **Size:** M · [#70](https://github.com/damhau/awxnext/issues/70)

Credentials and projects have a "Used by" panel. Add one to inventories,
execution environments and templates (the workflows and schedules that use
them), and list those dependents in the delete confirmation.

**Done when:** deleting something that is in use says what it is used by.

### M7-12 Diff view for changed tasks
**Labels:** feature, ui, jobs · **Size:** S · [#71](https://github.com/damhau/awxnext/issues/71)

Job output shows the before and after diff of changed tasks when a job ran
with diff mode on.

**Done when:** a job with `diff_mode` shows the diff of a template task.

### M7-13 Dark mode
**Labels:** feature, ui · **Size:** M · [#72](https://github.com/damhau/awxnext/issues/72)

Follows the system setting, with a manual switch, built on colour tokens
across the whole app. The job output terminal is already dark.

**Done when:** every page passes a contrast check in both themes.

### M7-14 Responsive layout
**Labels:** feature, ui · **Size:** M · [#73](https://github.com/damhau/awxnext/issues/73)

The UI has two media queries. Make approvals, job output, the operator
catalog and launch usable on a phone, and the rest usable on a tablet.

**Done when:** those pages work at phone width (375 px).

### M7-15 About dialog and keyboard shortcut help
**Labels:** feature, ui · **Size:** S · [#74](https://github.com/damhau/awxnext/issues/74)

Version, build and AWX compatibility version. A `?` overlay lists the
shortcuts.

**Done when:** both open from the user menu and the keyboard.

### M7-16 Translations
**Labels:** feature, ui · **Size:** L · [#75](https://github.com/damhau/awxnext/issues/75)

Make the UI translatable: extract strings and format dates and numbers by
locale. Ship English plus one language as a check, and document how to add
more. AWX ships es, fr, ja, ko, nl and zh.

**Done when:** the second language covers every page, and CI reports any
untranslated string.

### M7-17 Custom role definitions
**Labels:** feature, ui, rbac · **Size:** M · [#76](https://github.com/damhau/awxnext/issues/76)

The role definition API supports custom roles, but the UI cannot create
them. Add a page to create and edit them (pick permissions by type) and
offer them in the access tabs.

**Done when:** a custom role made in the UI can be granted and gives exactly
its permissions.

---

## M8 Settings that make sense

**Exit:** an admin can set up every login method, and find and change any
setting, without reading AWX's docs or writing JSON. The settings API is
unchanged: AWX's clients and the compat suites pass as before.

### M8-01 Settings laid out by task, with mockups first
**Labels:** feature, ui, settings · **Size:** M · [#94](https://github.com/damhau/awxnext/issues/94)

The settings page shows AWX's categories as pages and their keys as fields
sorted by name: 18 entries in its menu, 12 of them about signing in. Lay it
out by what an admin sets up instead: sign-in, jobs, system, logging, and
the login page. One declarative layout file in the UI places each of the 263
settings (page, section, order, label, control), and the page is drawn from
it instead of the per-category section maps it uses today. The API's
categories and keys do not change. Mockups of the two hardest screens, the
sign-in overview and the jobs page, are approved before the rest of the
milestone is built.

**Done when:** the mockups of the sign-in overview and the jobs page are
approved. The settings page is drawn from the layout file, and every setting
is reachable from it.

### M8-02 Sign-in overview
**Labels:** feature, ui, auth, settings · **Size:** L · [#95](https://github.com/damhau/awxnext/issues/95)

One page for everything about signing in: local accounts, sessions and the
password policy, then a card for each login method (OIDC, LDAP, SAML,
GitHub, Google, Azure AD) saying whether it is on, and opening its form.
GitHub's six categories become one card: github.com or GitHub Enterprise,
then any account, the members of an organization, or the members of a team;
it writes the matching AWX category. Each form is in the order the provider
is set up, and ends with what to give the provider (callback URL, SAML
service provider metadata), with copy buttons.

**Done when:** each login method can be set up from its card, and the
overview shows which ones are on. The GitHub card covers its six AWX
categories.

### M8-03 One mapping editor and tester for every login method
**Labels:** feature, ui, auth, api · **Size:** L · [#96](https://github.com/damhau/awxnext/issues/96)

Organization and team maps are JSON text boxes for every login method but
OIDC: `SOCIAL_AUTH_*_ORGANIZATION_MAP` and `_TEAM_MAP`, each LDAP server's
maps and user flags, SAML's attribute maps. Edit them all with one editor:
an organization or team; who gets it (everyone, these users, names matching
a pattern, members of these groups); as admin or member; and whether they
lose it when no longer matched. Test them against a sample user with a dry
run evaluated by the server's own mapping code (`internal/auth/awxmap.go`),
as `settings/oidc/test/` does for OIDC.

**Done when:** every organization and team map can be edited without writing
JSON. Each can be tested against a sample user, with the server's own
evaluation. A map saved by the editor is stored in AWX's format, so AWX's
clients read it unchanged.

### M8-04 Jobs settings by purpose
**Labels:** feature, ui, jobs, settings · **Size:** M · [#97](https://github.com/damhau/awxnext/issues/97)

The 39 jobs settings are one form whose "General" section mixes security
switches with display options. Split them by purpose: launch safety
(`ALLOW_ANSIBLE_VARS_AT_LAUNCH`, `IGNORE_UNPROMPTED_LAUNCH_FIELDS`,
`ALLOW_JINJA_IN_EXTRA_VARS`, `PROJECT_ALLOW_FILE_URLS`), each saying what
turning it on lets a launcher or a project do; limits and timeouts; content
(Galaxy, collections, roles); execution and paths; output, events and
archives.

**Done when:** every jobs setting is in a section named for its purpose.
Each launch safety switch says what turning it on allows.

### M8-05 A control for each kind of value
**Labels:** feature, ui, settings · **Size:** M · [#98](https://github.com/damhau/awxnext/issues/98)

81 settings are edited as raw JSON and timeouts as bare numbers. Give each
kind of value its control: durations with their unit and "0: no limit"; key
and value editors for environment variables (`AWX_TASK_ENV`,
`GALAXY_TASK_ENV`); list editors; secrets shown as set or not, and replaced
rather than edited; read-only values (callback URLs, SAML metadata) as text
with a copy button. Fields follow the layout's order, not their names.

**Done when:** no setting is edited as raw JSON except those whose shape is
free-form, listed in the closing comment. Durations show their unit, and
what 0 means.

### M8-06 Search and changes across settings
**Labels:** feature, ui, settings · **Size:** M · [#99](https://github.com/damhau/awxnext/issues/99)

Search every setting by label, key or help text, from the settings page and
the command palette. Mark settings changed from their default where they
are, with the default shown and a reset for that one setting. Warn before
leaving a form with unsaved changes. A page saves all its changes at once,
across the AWX categories it shows. This is the PLAN.md goal "searchable
sections showing non-default values".

**Done when:** any setting can be found by its label or its key. Changed
settings, and their defaults, show where they are. Leaving a form with
unsaved changes asks first.

### M8-07 Hide what has no effect; labels and help in plain words
**Labels:** feature, ui, settings · **Size:** M · [#100](https://github.com/damhau/awxnext/issues/100)

15 settings exist only so AWX's clients can read and write them, and do
nothing in awxnext; they are editable fields among the others. Hide them
behind "Show AWX settings with no effect". Label every setting by what it
does ("Keepalive for quiet remote jobs" rather than "K8S Ansible Runner
Keep-Alive Message Interval"), with its key still shown, and write help for
the 32 that have none. The API keeps AWX's labels, which clients may match;
help added on the server also shows in OPTIONS.

**Done when:** settings with no effect are hidden unless asked for. Every
setting has a plain label and help text.

### M8-08 Connection tests for LDAP, OIDC and SAML
**Labels:** feature, api, auth, ui · **Size:** M · [#101](https://github.com/damhau/awxnext/issues/101)

Logging is the only part of the settings that can be tested. Add tests for
the login methods, run with the form's values so a configuration can be
tried before it is saved: LDAP binds and looks up a user, and reports the
user's DN, groups, and the organizations and teams they would get; OIDC
fetches the discovery document and the signing keys; SAML reads each
identity provider's metadata and checks its certificate. Superusers only,
like the logging test.

**Done when:** each test reports success, or the step that failed and why.
Each runs in Go tests against a real server or provider.

### M8-09 Every setting placed in the layout
**Labels:** chore, ui, settings · **Size:** S · [#102](https://github.com/damhau/awxnext/issues/102)

A test fails CI when a setting the API has is missing from the layout, or
the layout names one the API does not have, as the server's tests already do
for `docs/configuration.md` and for settings with no effect. The list of
settings the UI test reads is generated from the settings definitions, and
checked to be current.

**Done when:** adding a setting without placing it in the layout fails CI.

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
| Slack, PagerDuty, Twilio, Grafana, IRC, Mattermost, Rocket.Chat and AWS SNS notifications | Email and webhook cover the need. Left out by decision on 2026-10-27. |
| Browser tests in CI | UI logic is covered by Vitest. Left out by decision on 2026-10-27. |
| Subversion projects (was M4-09, #40) | Not needed. Left out by decision on 2026-10-29. |

## Decisions

Taken on 2026-10-27:

1. Credential type injectors are rendered with gonja v2, in Go (M1-09).
2. The Terraform provider tested is `ansible/aap` (M2-01).
3. The milestone order stays as above.
4. RADIUS and TACACS+ are dropped.
5. Launching is safe by default: `ansible_*` variables from launchers and
   launch fields a template does not prompt for are refused. Settings
   restore AWX's behaviour for clients that rely on it (M1-15, M1-16).
6. Notifications: only email is added (M3-01); the other types are dropped.
7. Browser tests in CI are dropped.

Taken on 2026-10-29:

8. Subversion projects are dropped (M4-09, issue #40 closed as not planned).

Taken on 2026-10-31:

9. M8 redoes the settings page, which was a port of AWX's. The UI lays the
   settings out by task; the settings API keeps AWX's categories, keys and
   labels, so its clients are unaffected.

## On GitHub

Created on 2026-10-27 on `damhau/awxnext`: the type, area and size labels,
one milestone per section, and one issue per task (#1 to #80), each with a
**Done when** checklist (one box per lookup for M4-07). M2-14 to M2-26
(#81 to #93) were added on 2026-10-28 from the compatibility harness's first
runs. M8 (#94 to #102) was added on 2026-10-31. This file stays the index;
when a task changes, change its issue too.
