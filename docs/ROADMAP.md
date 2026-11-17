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
| [M4 Inventories, credentials and projects](#m4-inventories-credentials-and-projects) | Every inventory source, credential type, lookup and project type (except Subversion), and a source for rvav | 10 |
| [M5 Authentication](#m5-authentication) | Every login method AWX has, except RADIUS and TACACS+ | 6 |
| [M6 Operations and scale](#m6-operations-and-scale) | Run awxnext in production: policies, logging, Helm, backup, job isolation | 13 |
| [M7 UI completeness](#m7-ui-completeness) | Every API feature reachable from the UI; the remaining UI goals | 17 |
| [M8 Settings that make sense](#m8-settings-that-make-sense) | Settings laid out by task, without JSON or AWX's jargon; the settings API unchanged | 10 |
| [M9 Security](#m9-security) | Known vulnerabilities found by CI and fixed through reviewed PRs | 5 |
| [M10 Small fixes](#m10-small-fixes) | The importer tested on a real AWX, parity.md matching the code, the deployed image scanned, the AWS lookup tested on moto, M14-10's leftovers, a job's first lines live, M13's leftovers | 8 |
| [M11 Notifications](#m11-notifications) | Email tested on a real mail server, Slack as in AWX, a Teams type, notifications when a result changes | 4 |
| [M12 Beyond AWX](#m12-beyond-awx) | What changed before a failure, secrets kept out of job output, an MCP server, drifted hosts | 4 |
| [M13 Collection hub](#m13-collection-hub) | A private Galaxy in awxnext: publish, approve, sign, mirror and install collections | 5 |
| [M14 Event-driven automation](#m14-event-driven-automation) | Event streams into a durable log, rules evaluated by awxnext with guardrails, connectors, rulebooks imported | 10 |
| [M15 Self-service portal](#m15-self-service-portal) | A catalog of requestable items, with approvals, quotas and environments that expire | 5 |

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

### M4-11 A native rvav inventory source
**Labels:** feature, inventory, credentials · **Size:** M · [#122](https://github.com/damhau/awxnext/issues/122)

awxnext's own source type, not in AWX. [rvav](https://github.com/damhau/rvav)
lists the VMs it manages at `/api/v1/ansible/inventory`, in the format of
`ansible-inventory --list`; on AWX its `rvav.platform` collection reads it
from a project, in an execution environment. An `rvav` source has awxnext
read it itself and import it, with a managed `rvav` credential type (address
and token) and no project, collection or execution environment. The request
refuses awxnext's own addresses, as secret lookups do (M9-06), unless
`INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES` lists them.

**Done when:** a sync against a fake rvav imports its hosts and groups; a
missing credential, a refused token and a refused address fail with a message
that says what to do; the form offers the source without plugin options.

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
is set up, and shows what to give the provider where the provider asks for
it (callback URL, SAML service provider metadata), with copy buttons.

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
free-form, listed in the closing comment, and the organization and team maps,
which M8-03 edits. Durations show their unit, and what 0 means.

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

### M8-10 Mark lost nodes, and deprovision lost pods
**Labels:** bug, instances, deploy · **Size:** S · [#103](https://github.com/damhau/awxnext/issues/103)

Found on the permanent instance: each rollout leaves the old pod's instance
**Ready** for good, and group capacity counts it. Dispatch already skips
nodes silent for 2 minutes, but nothing marks them lost. As AWX does, the
dispatcher leader marks control and hybrid nodes silent for 2 minutes
`unavailable` with capacity 0, and `AWXNEXT_AUTO_DEPROVISION_INSTANCES` (on
in the chart) deletes them.

**Done when:** a test sees a silent node marked unavailable, its groups'
capacity drop, the node back when its heartbeats resume, and deleted with
auto-deprovisioning on.

---

## M9 Security

**Exit:** secubot scans awxnext daily (Trivy, govulncheck, gosec,
Semgrep), Claude triages what it finds and proposes fixes as draft PRs, or
issues for a human, and none that has a fix is left open. GitHub's security
features are on: Dependabot proposes the plain upgrades and CodeQL analyzes
each push. The owner reviews and merges every PR. `make vuln` scans both
images by hand. `main` has no branch protection (M9-03 dropped on
2026-11-01, see Proposed out of scope).

### M9-01 Scan for known vulnerabilities
**Labels:** chore, deploy · **Size:** M · [#104](https://github.com/damhau/awxnext/issues/104)

Nothing checks awxnext for known vulnerabilities. As in rvav, the Makefile
gets `vuln-go` (govulncheck, which reports only what the code reaches),
`vuln-web` (`npm audit` in `ui/`), `vuln-images` (Trivy on `awxnext` and
`awxnext-all-in-one`: no HIGH or CRITICAL vulnerability that has a fix) and
`sbom` (CycloneDX SBOMs of both images and of the UI's npm dependencies),
each tool pinned. A `security.yml` workflow runs the Go and UI checks on
every push to main and daily, and the images and SBOMs daily and by hand,
since building both images takes minutes. What the first runs find is fixed,
or accepted with its reason.

**Done when:** `security.yml` is green on main, on a push and on a manual
run that includes the image job, and the SBOMs are uploaded as an artifact.

`security.yml` was removed on 2026-11-01: secubot scans instead (M9-04,
Decision 15). The Makefile targets stay, for scanning by hand.

### M9-02 GitHub security features, Dependabot and pinned actions
**Labels:** chore, deploy · **Size:** M · [#105](https://github.com/damhau/awxnext/issues/105)

Dependabot alerts and security updates, private vulnerability reporting and
CodeQL's default setup are turned on. `SECURITY.md` says what is supported
(the latest `main` image), how to report (GitHub's **Report a
vulnerability**, not an issue) and promises an acknowledgement within 7
days. `dependabot.yml` updates Go modules, npm, actions and base images
weekly, grouped by ecosystem. Every action in the workflows is pinned to a
commit SHA. CodeQL's first findings are fixed, filed or dismissed with a
reason.

**Done when:** the four settings are on, Dependabot's first runs succeed,
CodeQL's first analysis is triaged, every `uses:` is a SHA, and CI is green.

### M9-04 Onboard secubot, in place of security.yml
**Labels:** chore, deploy · **Size:** M · [#107](https://github.com/damhau/awxnext/issues/107)

Rewritten on 2026-11-01 (Decision 15). secubot (`damhau/secubot`, release
v1.0.0) is the owner's reusable workflow:
- scanners: Trivy on the files and on the image, govulncheck, gosec and
  Semgrep, with their results in Code Scanning;
- Claude triages each finding, read-only, in two independent sessions;
- Claude fixes it with narrow tools, then the build, the tests and a rescan
  run;
- a step with no model opens **draft** PRs on `secfix/<id>` branches, or
  issues for what needs a human, and never merges.

`security.yml` (M9-01) is removed. The Makefile's `vuln-*` and `sbom`
targets and `.trivyignore` stay, for scanning by hand: until M10-03, they are
the only scan of the all-in-one image, since secubot builds only the
Dockerfile's default target. The caller, `secfix.yml`, is pinned to v1.0.0's commit,
runs by hand, as a dry run, with egress audited, on the `ANTHROPIC_API_KEY`
secret. `.secfix.yml` sets the setup, build and test commands, skips tests,
docs, `compat/` and the built UI, protects `.trivyignore` and
`SECURITY.md`, scans the image, and starts slowly (one PR and three
triaged findings a run). A dry run's summary is read and the configuration
tuned. The docs say how scanning works now.

**Done when:** `security.yml` is gone, a secubot dry run on awxnext is
green and its summary has been reviewed, the docs say how scanning works
now, and CI is green.

### M9-05 Run secubot live on awxnext, daily
**Labels:** chore, deploy · **Size:** S · [#108](https://github.com/damhau/awxnext/issues/108)

Rewritten on 2026-11-01. secubot goes live after M9-04's dry run:
`dry-run` is removed and a run checked (the owner removed it on 2026-11-02,
before any dry run, so the first live run stands for both): each handled finding ends as a
draft PR, an issue or a suppression, nothing is merged, and a second run
skips what is in flight. If a run on a branch opens its PR against that
branch, a dependency with a known reachable vulnerability is added on a
test branch to check that secubot proposes the fix; otherwise this is
skipped, since a vulnerable dependency on main would deploy. secubot opens
its PRs as the owner's App, so CI should start on them by itself; the first
PR's CI run is checked. The schedule is turned on, daily, at a time of
its own. After a few runs, harden-runner's log of outbound connections is
read and the egress policy switched to `block`. awxnext needs hosts secubot
does not list: repo1.maven.org for the tests' PostgreSQL, registry.npmjs.org
for the UI, and Docker Hub and gcr.io for the image build. If the caller
cannot add them, the policy stays `audit` and the reason is recorded. Each
run is recorded (trigger, PR or issue, cost), and the weeks that follow
decide whether to keep it.

**Done when:** a live run has finished green, with its results reviewed;
the daily schedule is on; the egress policy is `block`, or the reason it is
not is recorded.

Closed on 2026-11-05 by the owner's decision, with M9 and M9-04, and two
points left as they are. The two live runs of 2026-11-02 were green; the
second, on v1.1.0, triaged 10 of 41 high findings, suppressed all 10 as
false positives, cost $1.01 and published no PR or issue, so CI on a
secubot PR is still unchecked. The schedule is written in `secfix.yml` but
commented out, so secubot runs by hand. Egress stays `audit`: secubot
v1.1.0 lets a caller add no allowed host, and its lists lack repo1.maven.org
and Docker Hub. v1.2.0 then added `extra-allowed-endpoints`, so `block` is
now possible; it has not been switched.

### M9-06 Keep credential lookups from sending awxnext's own identity
**Labels:** security, credentials · **Size:** M · [#120](https://github.com/damhau/awxnext/issues/120)

Found by CodeQL's first analysis (M9-02). A HashiCorp Vault lookup with
`kubernetes_role` posts the pod's service account token to the Vault URL in
the credential's inputs, and an Azure Key Vault lookup without a secret
posts the workload identity's federated token to the `authority_host`
input. Any signed-in user can run a lookup with their own inputs through
`credential_types/N/test/`, which checks no permission. The server's own
identity is used only when an administrator allows it, towards the hosts
they list; testing a type is for superusers, as in AWX (Decision 13); and
lookups refuse loopback and link-local addresses unless allowed.

**Done when:** no lookup sends awxnext's token to a host an administrator
has not listed, the type test refuses a user who is not a superuser,
CodeQL's alerts 15 to 17 are closed, and CI is green.

---

## M10 Small fixes

Small fixes found after M9: the AWX importer has never run against a real
AWX, parity.md disagrees with the code in places, secubot doesn't scan the
image `main` deploys, and the AWS Secrets Manager lookup has only met a
fake (added on 2026-11-08). **Exit:** an import from a real AWX 24.6.1 is
tested end to end, parity.md agrees with the code and the closed issues,
the all-in-one image is scanned automatically, and the AWS Secrets Manager
lookup is tested against moto.

### M10-01 Test the AWX importer against a real AWX 24.6.1
**Labels:** chore, deploy · **Size:** M · [#124](https://github.com/damhau/awxnext/issues/124)

`awxnext import-awx` reads AWX's database directly, and its only test
(`test/importer_test.go`) builds a hand-written copy of the AWX 24 tables it
reads. Schema drift, the data AWX itself writes (managed credential types,
implicit roles, encrypted survey answers, SSO settings) and size are
untested.

A workflow reuses `bench.yml`'s install of AWX 24.6.1 in kind. It fills AWX
through its API, runs `import-awx --dry-run`, then the import, compares the
two through `/api/v2` and launches an imported template. It runs by hand,
and from `full.yml`. What it finds is fixed, or documented.

**Done when:** the workflow passes, the imported objects and roles match
AWX's, an imported credential works in a job, and every gap found is fixed
or documented.

### M10-02 Bring parity.md in line with the code
**Labels:** chore · **Size:** S · [#125](https://github.com/damhau/awxnext/issues/125)

Some rows contradict the code and the closed issues:

- password policy settings, Missing (M5-06);
- session and login settings, Partial (M1-02);
- the second secret lookups row, Later (M4-07);
- email, Later (M3-01);
- the UI row with no credential types page (M7-01);
- `host_metric_summary_monthly` missing (M2-04);
- priority item 14, the refspec on branch overrides (M1-12).

Each is checked against the code and its tests, then corrected or removed,
and "Where it stands" follows.

**Done when:** no row says Missing, Later or Partial for something that is
done, and no finished priority item is left unstruck.

### M10-03 Scan the all-in-one image automatically
**Labels:** chore, deploy · **Size:** S · [#126](https://github.com/damhau/awxnext/issues/126)

secubot's `trivy_image` builds the Dockerfile's default stage, the
distroless `awxnext` image, and has no `target` option. So since M9-04 the
all-in-one image `main` deploys is scanned only by `make vuln-images`, by
hand. secubot (the owner's repository) gets a `target` option. Then awxnext
pins that release and sets `trivy_image.target: all-in-one`, which covers
the binary too.

**Done when:** a secubot run on awxnext scans the all-in-one image, and the
docs say so.

### M10-04 Test the AWS Secrets Manager lookup against moto
**Labels:** chore, credentials · **Size:** S · [#129](https://github.com/damhau/awxnext/issues/129)

The lookup calls AWS through aws-sdk-go-v2, and its only test runs against a
fake written in the test. `full.yml`'s go job starts moto's server with its
authentication on, so it checks the access key and the signature. A Go test,
skipped without `AWXNEXT_TEST_MOTO_ENDPOINT`, looks up a string and a binary
secret, and checks that an unknown secret, a wrong key and another region
fail with AWS's error. What it finds is fixed, and the fake corrected.

**Done when:** the test passes in `full.yml` against moto and fails with a
wrong secret key, and what it found is fixed.

### M10-05 UI for the EDA fields, and a clear 403 on activations
**Labels:** ui, rbac · **Size:** S · [#176](https://github.com/damhau/awxnext/issues/176)

M14-10 left some fields with no UI (added on 2026-11-16): a project's
`scm_proxy` and `scm_verify_ssl`, an event stream's `additional_data_headers`,
and a rulebook's `source_streams`. The project, event stream and rulebook
pages get them. Also, an Organization Project Admin who creates or enables an
activation gets a 403 that doesn't say why. The activation changes the
connectors and rules its import made, which only an Organization Admin may
change. The refusal names the object and the missing role.

**Done when:** the four fields can be read and set in the UI, and the 403
names what is missing.

### M10-06 Show a job's first output lines live
**Labels:** ui, bug · **Size:** S · [#177](https://github.com/damhau/awxnext/issues/177)

Found during M13-04 (added on 2026-11-17), the bug is older. When a
relaunched job's page opens while the job runs, its live output can miss its
first lines until the page is reloaded: the websocket's events start at
counter 2 or later, and `firstGap` in `ui/src/pages/jobs/events.ts` doesn't
count a missing start as a gap. When the first event received isn't the
output's first, the page fetches the lines before it, once, and a long job's
page still doesn't refetch its whole output.

**Done when:** a Vitest test covers a stream that starts at counter 2 and
checks that the missing line is fetched, and in the browser a relaunched
job's live output shows its first line without a reload.

### M10-07 Read collections' module docs in a pod on Kubernetes
**Labels:** feature, deploy · **Size:** M · [#178](https://github.com/damhau/awxnext/issues/178)

Left over from M13-05 (added on 2026-11-17). The hub runs `ansible-doc`
only where jobs run isolated (Decision 20). The Helm chart's replicas run
jobs neither in containers nor as job users, so on Kubernetes no version's
docs are read. A node that can't run `ansible-doc` isolated reads the docs
in a pod instead, as a container group runs a job. The pod uses the
organization's execution environment, has the same limits, and is deleted
afterwards. A NetworkPolicy shipped by the chart denies it egress, where the
cluster enforces NetworkPolicy.

**Done when:** in the kind test, a collection uploaded to the chart's
install has its module docs read in a pod and served on its `docs/`. The pod
is gone afterwards, and the chart's NetworkPolicy selects it.

### M10-08 Re-check a remote's versions when its signature credential is set
**Labels:** security · **Size:** S · [#179](https://github.com/damhau/awxnext/issues/179)

Left over from M13-04 (added on 2026-11-17). Versions mirrored before a
remote's `signature_validation_credential` was set, or before it changed,
stay in its repository unverified: they are installable, and they meet
requirements. A sync with a credential checks the versions already in the
repository as it checks new ones. It drops the signatures the keys don't
verify, and takes out of the repository each version left with none, listed
with the skipped versions.

**Done when:** `TestHubRemoteSync` mirrors a version without a credential,
then sets a credential that doesn't verify it. The next sync takes the
version out of the repository, lists it, and fails when it was its
requirement's only version.

---

## M11 Notifications

Notifications after M10: email is tested only against a fake server, Slack
was left out, there is no Teams type (Decision 17), and every failure sends a
message (Decision 18). **Exit:** email notifications pass against Mailpit in
CI, AWX's Slack notifications work and import, Teams notifications post a
card, and notifications can fire only when a result changes.

### M11-01 Test email notifications against a real mail server
**Labels:** chore, notifications · **Size:** S · [#130](https://github.com/damhau/awxnext/issues/130)

Email (M3-01) is tested against an SMTP server written in the test.
`full.yml`'s go job starts Mailpit in clear, with STARTTLS and with TLS from
the start, each requiring a login. A Go test sends a test notification and a
job's notifications through each, with non-ASCII text and a long line, reads
them back from Mailpit's API, and checks that a wrong password and an
untrusted certificate fail.

**Done when:** the test passes in `full.yml` in the three modes, and what it
found is fixed.

### M11-02 Slack notifications, as AWX has them
**Labels:** feature, notifications, ui · **Size:** M · [#131](https://github.com/damhau/awxnext/issues/131)

AWX 24.6.1's `slack` type: `token`, `channels` (a thread with
`#name, <message ts>`) and `hex_color`, its validation, help texts and
default messages, a `chat.postMessage` per channel, and custom messages as
for the other types. The importer brings AWX's Slack notifications, so
`import-awx.yml`'s `ops-slack` compares equal; the UI form has the Slack
fields. Tested against a fake Slack API in the test.

**Done when:** a Slack notification template works through the API, the UI
and the collection, posts as AWX does, and an AWX one imports and compares
equal.

### M11-03 Microsoft Teams notifications
**Labels:** feature, notifications, ui · **Size:** M · [#132](https://github.com/damhau/awxnext/issues/132)

awxnext's own `teams` type, since AWX has none: the channel's webhook `url`
(a secret), made with Teams' Workflows template "Post to a channel when a
webhook request is received". Each event posts an Adaptive Card with the
message, the job's status, template, launcher and times, and a button to the
job or approval. The `awx.awx` collection checks the type against its own
list, so it cannot manage these; the docs say so. Tested against a fake
receiver in the test.

**Done when:** a Teams notification template works through the API and the
UI, posts the card for each event, and the docs say how to make its URL.

### M11-04 Notify when a result changes
**Labels:** feature, notifications, ui · **Size:** M · [#147](https://github.com/damhau/awxnext/issues/147)

AWX notifies every failure, so a template that fails each night sends the
same message each night, and nothing says when it works again. awxnext adds
three events of its own, attached like AWX's
(`notification_templates_<event>/` on templates, projects, inventory sources
and organizations), which AWX's clients don't see: `first_error` (a failure
after a success), `recovered` (a success after a failure) and `slow` (more
than twice the median of the template's last 10 successful runs, once it has
5). Each has a default message and custom messages.

**Done when:** each event fires once when the result changes, not on the
later runs that keep it, through the API and the UI's Notifications tabs,
and the docs say what each means.

---

## M12 Beyond AWX

Features AWX lacks, proposed on 2026-11-15 (Decision 18). **Exit:** a failed
job says what changed since the last success, credential secrets don't appear
in job output, an assistant can use awxnext over MCP with its user's rights,
and hosts that drift from their playbooks are listed.

### M12-01 What changed since the last good run
**Labels:** feature, jobs, ui · **Size:** M · [#148](https://github.com/damhau/awxnext/issues/148)

The "Why it failed" panel links to the template's previous successful run
but doesn't say what differs. `jobs/N/changes/` (and `workflow_jobs/N/`,
awxnext's own) and the panel list: edits to the template, inventory,
credentials and execution environment from their version history; the
project's commits between the two revisions; a different image digest
(recorded with each job from now on); different launch inputs; hosts added or
removed. Secrets stay `$encrypted$`, and rights on each object apply.

**Done when:** a test changes each of these between two runs and each is
listed, an unchanged rerun lists nothing, and no secret appears.

### M12-02 Keep credential secrets out of job output
**Labels:** security, jobs · **Size:** M · [#149](https://github.com/damhau/awxnext/issues/149)

A task that prints a secret (`debug`, `-vvv`, a failing `uri`) puts it in the
output, for anyone who may read the job; AWX hides only the SCM password in
project updates. awxnext replaces every secret it handed the job (secret
credential inputs and lookups, survey passwords, prompted passwords) with
`$encrypted$` in stdout and event data, before they are stored, published,
sent to the log aggregator or archived: as given, JSON-escaped, and line by
line. Values under 4 characters are left alone. The cost is measured with
`cmd/awxbench`.

**Done when:** a test job prints each kind of secret in each form and none
is stored, published, sent or archived, the benchmark is in
[benchmarks.md](benchmarks.md), and the docs say what isn't masked.

### M12-03 An MCP server for assistants
**Labels:** feature, api, auth · **Size:** M · [#150](https://github.com/damhau/awxnext/issues/150)

`/api/mcp/` (Streamable HTTP, the official Go SDK), signed in with a personal
access token or an OIDC bearer token, with its owner's rights. Tools by task:
find objects, a job's status, failure and output, what changed (M12-01), a
template's prompts, launch and relaunch on failed hosts, approve or deny, a
host's recent results. Read-scope tokens get only the reading tools; the
others carry MCP's `destructiveHint`. A setting turns it off.

**Done when:** a Go test calls each tool through an MCP client, a read token
cannot launch or approve, and the docs show the setup for Claude Code.

### M12-04 Host history and drift
**Labels:** feature, jobs, inventory, ui · **Size:** L · [#151](https://github.com/damhau/awxnext/issues/151)

The design's Host screen ([DESIGN.md](awxnext-design/DESIGN.md), section 7):
per-host, per-task results across jobs, in a table filled as events are
stored, with its own retention. Its flags: changes every run, new failure,
drift (a `changed` between `ok` runs with no template or revision change). A
check-mode run that would change a host also marks it drifted, with the diff,
until a run leaves it unchanged. A Drift page lists drifted hosts.

**Done when:** a test's runs raise each flag, a check-mode run marks drift
and an unchanged run clears it, and the Host screen and the Drift page show
them.

---

## M13 Collection hub

A private Galaxy in awxnext, as Red Hat's Private Automation Hub is beside
AWX (Decision 18): teams publish their collections, approved and signed ones
are installed by project syncs and image builds, and public ones are
mirrored. It serves Galaxy's v3 API, so `ansible-galaxy` and
`ansible-builder` use it unchanged; images stay in a container registry.
**Exit:** a collection published with `ansible-galaxy collection publish` is
approved, signed, and installed by a project sync that requires signatures,
and galaxy.ansible.com collections are mirrored from a requirements file.

### M13-01 Serve collections on Galaxy's API
**Labels:** feature, api, projects · **Size:** M · [#152](https://github.com/damhau/awxnext/issues/152)

Galaxy's v3 API under `/api/galaxy/`, read side, with galaxy_ng's
repository URLs. Namespaces belong to organizations, read as awxnext's RBAC
allows; `Authorization: Token` is accepted, as `ansible-galaxy` sends it.
Files are kept as event archives are (the database, S3 or Azure Blob), under
settings of their own (`HUB_STORAGE_*`). A managed Galaxy credential lets
project syncs install from it, with a token minted per sync. A collection a
user can't read answers `404`, as if it weren't there: namespace names are
global, and Galaxy clients then go on to their next server.

**Done when:** the real `ansible-galaxy` installs and downloads from it, a
project sync installs through the managed credential, and a user without
rights sees nothing.

### M13-02 Publish and approve collections
**Labels:** feature, api, rbac · **Size:** M · [#153](https://github.com/damhau/awxnext/issues/153)

`ansible-galaxy collection publish` uploads to `staging`, with galaxy_ng's
import task and log: layout, `MANIFEST.json` and `FILES.json` checksums,
metadata, the namespace's organization, no duplicate version. Approval moves
it to `published`, rejection to `rejected`; a namespace can approve on
upload. Namespace owner, uploader and approver roles; deprecation, and
deleting an unused version.

**Done when:** a published collection installs once approved and not once
rejected, only uploaders publish and approvers approve, and a bad upload is
refused with its reason in the log.

### M13-03 Sign collections and check their signatures
**Labels:** feature, security, projects · **Size:** M · [#154](https://github.com/damhau/awxnext/issues/154)

Approval signs `MANIFEST.json` with a GPG key awxnext keeps under its master
key, and the v3 API serves each version's `signatures`, as `ansible-galaxy`
reads them. Signatures made elsewhere can be uploaded; keys rotate. Project
syncs and image builds can require N valid signatures, with the keyring from
a GPG Public Key credential (M4-08).

**Done when:** `ansible-galaxy collection install --keyring` and `verify`
pass on a signed collection and fail on a tampered one, and a sync that
requires signatures fails on an unsigned collection.

### M13-04 Mirror collections from other servers
**Labels:** feature, projects · **Size:** M · [#155](https://github.com/damhau/awxnext/issues/155)

Remotes (galaxy.ansible.com, Red Hat's Automation Hub with its token,
another galaxy_ng) sync the collections a requirements file lists, with
their dependencies and signatures, into a repository of their own, on demand
or on a schedule, each sync a job. `awxnext hub import` brings in what
`ansible-galaxy collection download` wrote elsewhere, for hubs without
internet.

**Done when:** a test syncs from a fake Galaxy written in the test,
`full.yml` mirrors one small collection from galaxy.ansible.com, and an
import of a downloaded directory works.

### M13-05 Collections in the UI
**Labels:** feature, ui · **Size:** M · [#156](https://github.com/damhau/awxnext/issues/156)

Browse and search collections; a collection's page with its versions,
dependencies, README, each plugin's docs (read at import with `ansible-doc`),
the install command, and the projects and execution environments that use
it. Namespaces and their roles, the approval queue with a diff against the
previous version, remotes and their syncs, the signing key.

**Done when:** each page works in the browser for an owner and for a reader,
`tsc` and Vitest pass, and a module's docs show on its page.

---

## M14 Event-driven automation

Events (an alert, a webhook, a Kafka message) that launch templates, as
Event-Driven Ansible's controller does, built for safe remediation (Decision
19): event streams write to a durable log in PostgreSQL, and awxnext's nodes
evaluate rules written in CEL, with their state in PostgreSQL, guardrails
before any action, and a decision log that explains every event.
`ansible.eda` source plugins run as connectors, and rulebooks are translated
on import. Four stages: streams, rules, guardrails and the decision log
(M14-01 to M14-05); connectors (M14-06); windows and fix checks (M14-07,
M14-08); rulebooks and EDA's API (M14-09, M14-10). **Exit:** an Alertmanager
alert launches its remediation once per key, inside its guardrails, after a
backtest in shadow mode, and the decision log says why each event did or
didn't launch anything; a Kafka topic feeds a rule through a connector; an
EDA rulebook imports, and the `ansible.eda` collection manages it.

### M14-01 Event streams and the event log
**Labels:** feature, api, auth · **Size:** M · [#157](https://github.com/damhau/awxnext/issues/157)

Streams with their own URL receive CloudEvents (binary, structured, batch)
and common payloads through adapters (Alertmanager, split into one event per
alert; GitHub, GitLab, Grafana, ServiceNow, generic JSON), authenticated by
HMAC, a token, mTLS, or OIDC tokens checked against their issuer. `202`
only once the event is committed; duplicate IDs are dropped; payload and
rate limits apply. Events go to a log partitioned by day, with each stream's
retention; test mode keeps them from the rules.

**Done when:** each authentication method and format works and bad
credentials are refused, duplicates are dropped, `202` means stored, and
test mode keeps events from the rules.

### M14-02 Rules and their actions
**Labels:** feature, jobs, rbac · **Size:** L · [#158](https://github.com/damhau/awxnext/issues/158)

A rule has a CEL filter (cel-go), type-checked when saved against the
stream's inferred schema, a key, deduplication and throttling per key, and
an action: launch a job or workflow template with variables mapped from the
event, notify, or post to another stream. It runs as its owner, with rights
checked when saved and at launch, and the rule and event ID are the
launch's idempotency key. Each stream is evaluated in order by one node
under a lock, its position saved with its decisions, so a failover loses
and repeats nothing.

**Done when:** a test's alert launches its template once per key in the
throttle window, a redelivered event or a failover launches nothing twice,
and a rule with a type error, or whose owner lacks rights, launches nothing.

### M14-03 Guardrails and rule modes
**Labels:** feature, jobs, workflows · **Size:** M · [#159](https://github.com/damhau/awxnext/issues/159)

Checked before each action: at most N runs at once, no launch on a host a
running job uses, a cap per inventory group, a circuit breaker after K
firings of one key in a window, and maintenance windows and change freezes
(calendars, as schedules have them). Modes: off, shadow (records what it
would do), approval (through workflow approvals) and auto (a role of its
own).

**Done when:** each guardrail holds an action back with its reason, the
breaker trips and is reset, shadow launches nothing, and approval launches
once approved.

### M14-04 Decision log, rule tests and backtests
**Labels:** feature, api · **Size:** M · [#160](https://github.com/damhau/awxnext/issues/160)

For each event and rule: matched or not (with the part of the filter that
failed), throttled, held back by which guardrail, waiting, or launched job
N. `awxnext rules test` runs sample events through rules offline, for a
GitOps repository's CI, and `event_rules/N/backtest/` runs a rule over a
period of the log. Prometheus metrics per stream and rule, and one
OpenTelemetry trace from the event's `traceparent` to the job.

**Done when:** each kind of decision is logged with its reason, rule tests
pass and fail, a backtest gives its counts, and one trace runs from the
event to the job.

### M14-05 Streams and rules in the UI
**Labels:** feature, ui · **Size:** M · [#161](https://github.com/damhau/awxnext/issues/161)

Streams with setup snippets and their live events; a rule editor with CEL
completion from the inferred schema, type errors as you type, and the draft
run on recent events; the decision log from an event and from a rule;
backtests, the breaker, and the success rate (M14-08).

**Done when:** a rule is written against a stream's real events and saved
in the browser, and `tsc` and Vitest pass.

### M14-06 Connectors on the agent
**Labels:** feature, instances, credentials · **Size:** M · [#162](https://github.com/damhau/awxnext/issues/162)

A small Python runner hosts any `ansible.eda` source plugin (`async def
main(queue, args)`), without `ansible-rulebook` or Java, and posts its
events to a stream over HTTPS, buffering while awxnext is out of reach. It
runs where jobs run (an execution node's agent, a container group pod,
podman or docker), in any image with the plugin's collection, decision
environments included, on one node at a time, with restarts and credentials
from awxnext.

**Done when:** a test's connector runs `ansible.eda`'s `range` and
`generic` sources, `full.yml` runs the Kafka source against Kafka, and a
connector buffers through an outage and moves off a lost node.

### M14-07 Windows, sequences and absence
**Labels:** feature, jobs · **Size:** L · [#168](https://github.com/damhau/awxnext/issues/168)

Per key: a count in a sliding window, all of several conditions within T, a
sequence, and absence, fired by a timer at its deadline; the action gets
every event that matched. State and timers are kept in PostgreSQL, so a
restart or a failover loses no window. The decision log shows a window's
progress.

**Done when:** each primitive works in a test, absence fires at its
deadline, and a restart or failover mid-window keeps its state.

### M14-08 Check that a fix worked
**Labels:** feature, jobs, notifications · **Size:** M · [#169](https://github.com/damhau/awxnext/issues/169)

After its action, a rule can wait for the matching resolved event within T,
or run a check template: fixed, not fixed, or failed. A fix that doesn't
work escalates and counts towards the breaker. Each rule's success rate
shows through the API, metrics and the UI.

**Done when:** a fix that resolves its alert is recorded as fixed, one that
doesn't escalates, and the success rate shows.

### M14-09 Import rulebooks
**Labels:** feature, projects · **Size:** M · [#170](https://github.com/damhau/awxnext/issues/170)

A project's rulebooks (where EDA's controller looks for them) or an uploaded
file become streams, connectors and rules: conditions as CEL; `throttle`,
`all`, `any`, `not_all` and `timeout` as M14-07's primitives; the
template actions as actions. `run_playbook` and `run_module` aren't
translated. A report lists each rule, translated or not, with the reason.

**Done when:** `ansible-rulebook`'s and `ansible.eda`'s example rulebooks
import with their reports, and an imported Alertmanager rulebook behaves as
it does under `ansible-rulebook`.

### M14-10 EDA's API for the `ansible.eda` collection
**Labels:** compat, api · **Size:** L · [#171](https://github.com/damhau/awxnext/issues/171)

The `/api/eda/v1/` endpoints the `ansible.eda` collection calls, over
awxnext's objects: its projects and credentials, connector images for
decision environments, imported rulebooks for activations. Tested by the
collection's integration targets in `compat/`.

**Done when:** the collection's modules run in the compat suite, and what
still fails is in `compat/known-failures.yml` with its reason.

---

## M15 Self-service portal

The operator view (`/run`) lists every template a user may launch. M15
makes it a governed catalog (Decision 18): chosen items, requestable without
access to their templates, approvals before they run, quotas, and
environments that their owners manage and that expire. **Exit:** a user with
no access to a template requests its catalog item, a team approves, the
environment is provisioned with its outputs, extended once, and torn down
when it expires.

### M15-01 Catalog items
**Labels:** feature, api, rbac · **Size:** M · [#163](https://github.com/damhau/awxnext/issues/163)

An item publishes a job or workflow template with its own name, description,
icon, category and owner team, and its form: the questions to show, defaults
and fixed answers. Its request role gives no role on the template, its
inventory, project or credentials; the requester shows as the launcher.
Version history, `used_by` and GitOps, as for other objects.

**Done when:** a user with only the request role launches through the item
but not the template, and a request that overrides a fixed answer is
refused.

### M15-02 Approvals before a request runs
**Labels:** feature, workflows, notifications · **Size:** M · [#164](https://github.com/damhau/awxnext/issues/164)

An item can make its requests wait for one or N approvers of its approver
team, who see the answers and the requester, with a timeout that denies.
Built on workflow approvals, so the bell, the approvals page,
`workflow_approvals/` and their notifications work unchanged. The requester
is notified of the outcome.

**Done when:** a request runs once approved, doesn't once denied or timed
out, and approvers and requester are notified.

### M15-03 Environments and their lifecycle
**Labels:** feature, workflows, jobs · **Size:** L · [#165](https://github.com/damhau/awxnext/issues/165)

What an item's run provisioned is recorded as an environment, owned by the
requester and their team, with the workflow's outputs (M3-04). Day-2 actions
run the item's templates on it with those outputs. It expires after the
item's lifetime, extendable N times, with warnings before; expiry or delete
runs the teardown template, and a failed teardown leaves it flagged.

**Done when:** a test's item provisions an environment with its outputs,
runs a day-2 action, is extended, and is torn down at expiry, and a failed
teardown is flagged.

### M15-04 Quotas
**Labels:** feature, rbac · **Size:** S · [#166](https://github.com/damhau/awxnext/issues/166)

Limits per item and per team or user: active environments and requests per
day. Over quota, a request is refused with the reason or waits for approval,
as the item says. Admins see usage against quotas.

**Done when:** an over-quota request is refused or waits, as the item says,
and usage shows through the API and the UI.

### M15-05 The portal
**Labels:** feature, ui · **Size:** M · [#167](https://github.com/damhau/awxnext/issues/167)

The operator view grows into the portal: the catalog by category, an item's
request form with its preview and the quota left, My requests (status,
approval, the run in plain words), My environments (outputs, day-2 actions,
expiry). Users with only request roles land there after sign-in, without the
admin navigation.

**Done when:** a requester finds, requests and follows an item and manages
its environment in the browser, and `tsc` and Vitest pass.

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
| PagerDuty, Twilio, Grafana, IRC, Mattermost, Rocket.Chat and AWS SNS notifications | Email, webhook, Slack and Teams cover the need. Left out by decision on 2026-10-27; Slack was taken back on 2026-11-08 (M11-02). |
| Browser tests in CI | UI logic is covered by Vitest. Left out by decision on 2026-10-27. |
| Subversion projects (was M4-09, #40) | Not needed. Left out by decision on 2026-10-29. |
| Rulesets and code owners on `main` (was M9-03, #106) | awxnext is personal code, and secubot's PRs, then opened with `GITHUB_TOKEN`, started no CI, so required checks would have waited for ever. Left out by decision on 2026-11-01. |
| Running rulebooks under `ansible-rulebook` | awxnext evaluates rules itself; rulebooks are translated on import (M14-09), and what doesn't translate is reported. Left out by decision on 2026-11-15 (Decision 19), unless real rulebooks need it. |

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

Taken on 2026-11-01:

10. M9 Security is added, starting with vulnerability scanning in CI on the
    model of rvav's `security.yml`.
11. M9 grows into a hardening plan (M9-02 to M9-05):
    - GitHub's security features and SHA-pinned actions;
    - rulesets on `main` with a bypass for the repository admin only, so
      the owner and local Claude Code keep pushing to `main` while the
      Claude App and Dependabot go through PRs (withdrawn, Decision 14);
    - code owner review on `.github/`, `.trivyignore` and `SECURITY.md`
      (withdrawn, Decision 14);
    - a Claude autofix job on the owner's subscription token, with a CI
      guard on the paths it must not touch (replaced by secubot, Decision
      15).

    `SECURITY.md` promises an acknowledgement within 7 days. After M9-05,
    autofix (secubot since Decision 15) runs for 3 to 4 weeks, and each run
    is recorded (trigger, PR or issue, merged as is, edited or closed, cost)
    to decide whether to keep it.
12. The Dockerfile's base images keep floating tags, not digests (M9-02):
    each build takes their latest patch release, so a fix in Go's standard
    library or in Debian reaches the next image without a PR, and the
    all-in-one image is not reproducible anyway, since it upgrades its
    Debian packages as it builds. Dependabot proposes the next lines.
13. Credential lookups use awxnext's own identity (Vault's Kubernetes login,
    Azure workload or managed identity) only towards the hosts listed in
    `CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS`, empty by default, and reach
    loopback, link-local and cloud metadata addresses only when listed in
    `CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES` (M9-06). Key Vault domains
    are not allowed implicitly: listing `*.vault.azure.net`, or only the
    vaults meant for lookups, is the administrator's choice.
    `credential_types/N/test/` is for superusers, as in AWX, whose view
    checks the right to add a credential type: any signed-in user may create
    a personal credential, so "the right to create a credential" would have
    refused no one. Others test a saved credential at `credentials/N/test/`.
14. `main` has no branch protection: M9-03 (#106) is dropped. Its two
    rulesets were created, then removed by the owner the same day, along
    with CODEOWNERS, which nothing enforces without them. awxnext is
    personal code, and secubot's PRs, then opened with the workflow's
    `GITHUB_TOKEN`, started no CI (GitHub runs no workflow on that token's
    events), so they would have waited for ever on required checks. Pushes go
    straight to `main`; Dependabot's PRs are reviewed and merged by the
    owner. `ci.yml` still runs on every PR, whatever it changes, docs
    included.
15. secubot replaces `security.yml` (M9-04, M9-05). It is the owner's
    reusable workflow (`damhau/secubot`): pinned scanners, then Claude
    triages each finding and proposes a verified fix as a draft PR, or an
    issue. It leaves the plain upgrades to Dependabot, which stays, as does
    CodeQL. Its credential is the `ANTHROPIC_API_KEY` secret. Since
    2026-11-02 it also publishes as the owner's secubot GitHub App
    (`SECUBOT_APP_ID`, `SECUBOT_APP_PRIVATE_KEY`): its PRs start CI, and
    Actions need no right to create PRs. No scan fails CI any more. The
    Makefile's `vuln-*` and `sbom` targets stay for scanning by hand. Until
    M10-03 they were the only scan of the all-in-one image, since secubot
    built only the Dockerfile's default target.

Taken on 2026-11-07:

16. The AWX import is compared with a real AWX 24.6.1 (M10-01). What differs
    on purpose is listed with its reason in
    `test/awximport/known-differences.yml`, and anything else fails the run.
    An execution environment of an existing name is kept, with a warning. An
    empty value that reads `""` where AWX has null stays as it is, since no
    client treats them differently and null would need a migration.

Taken on 2026-11-08:

17. Notifications grow again (M11, amends Decision 6). Slack comes back as
    AWX's type, so AWX's Slack notifications import and AWX's clients manage
    them. Teams is added as awxnext's own type, since AWX has none.
    PagerDuty, Twilio, Grafana, IRC, Mattermost, Rocket.Chat and AWS SNS stay
    out. Two features tested only against fakes written in their tests are
    also tested against servers from elsewhere, in `full.yml`: the AWS
    Secrets Manager lookup against moto (M10-04), chosen over a real AWS
    account because it needs no account or cost, and email against Mailpit
    (M11-01).

Taken on 2026-11-15:

18. awxnext grows past AWX (M11-04, M12 to M15). Five proposals of Claude
    Code's are added: notifications when a result changes (M11-04), and in
    M12 what changed before a failure, secrets masked in job output, an MCP
    server, and host history with drift. The three products sold around AWX (a private
    automation hub, an event-driven controller, a self-service portal) are
    built into awxnext, with one server, API, RBAC and UI, rather than as
    services of their own:
    - **Collections** (M13) are served on Galaxy's v3 API, so
      `ansible-galaxy` and `ansible-builder` use the hub unchanged.
      Execution and decision environment images stay in a container
      registry: awxnext hosts none.
    - **Rulebooks** (M14; replaced by Decision 19) run in `ansible-rulebook`,
      in decision environments, as EDA's controller runs them, so existing
      rulebooks and source plugins work; awxnext writes no rule engine.
      Their objects use awxnext's projects, credentials and organizations.
      EDA's own API, for the `ansible.eda` collection, comes last (M14-06,
      now M14-10).
    - **The portal** (M15) grows from the operator view (`/run`), not as a
      separate portal such as Red Hat's, which is built on Backstage.
    - A sixth proposal, alert-driven runs (Alertmanager alerts launching
      templates, deduplicated and throttled), is M14's event streams and
      their rules, not a mechanism of its own.
19. M14 is redesigned the same day: awxnext evaluates events itself
    instead of running `ansible-rulebook` (replacing Decision 18's item on
    rulebooks). Running it in activations had two flaws:
    - an activation fed by an event stream needs a database login, as
      EDA's `pg_listener` source has, and PostgreSQL has no permission per
      notification channel: with any login, an activation could send
      awxnext's own notifications, such as `awx_cancel`, which cancels a
      job on every node;
    - events sent while an activation restarts or moves are lost.

    Instead:
    - event streams write to a durable log in PostgreSQL, and `202` means
      stored;
    - awxnext's nodes evaluate rules written in CEL, with their state in
      PostgreSQL, guardrails before any action (blast radius, host locks, a
      circuit breaker, maintenance windows, shadow and approval modes), a
      decision log, rule tests and backtests;
    - `ansible.eda` source plugins run as connectors, on the agent among
      other places, without `ansible-rulebook`;
    - rulebooks are translated on import, and what doesn't translate is
      reported (M14-09). Running them under `ansible-rulebook` is left out.

    #157 to #162 are rewritten for this design, and M14-07 to M14-10 (#168
    to #171) are added.
20. The hub reads a collection's docs (M13-05, on 2026-11-17) with
    `ansible-doc`, as galaxy-importer does, which imports some of the
    collection's Python code. So it runs after the import, in the
    background, where jobs run: in the organization's execution environment
    with no network, or as a job user. It runs as the server's user only
    when `HUB_COLLECTION_DOCS` is `server_user`, so the Helm chart's
    replicas read no docs by default. A version whose docs can't be read
    still installs. galaxy_ng's `docs-blob/` is served from the same docs,
    with the README as text rather than rendered HTML.

## On GitHub

Created on 2026-10-27 on `damhau/awxnext`: the type, area and size labels,
one milestone per section, and one issue per task (#1 to #80), each with a
**Done when** checklist (one box per lookup for M4-07). M2-14 to M2-26
(#81 to #93) were added on 2026-10-28 from the compatibility harness's first
runs. M8 (#94 to #102) was added on 2026-10-31, and M8-10 (#103) the same day, from the permanent instance. M9 (#104) was added on
2026-11-01, M9-02 to M9-05 (#105 to #108) the same day, and M9-06 (#120)
from CodeQL's first analysis. M4-11 (#122) was added the same day, for rvav. M9-04 and M9-05
(#107, #108) were rewritten the same day for secubot (Decision 15). M10 (#124 to #126) was added on 2026-11-02 for the small fixes found
after M9. M10-04 (#129) and M11 (#130 to #132) were added on 2026-11-08
(Decision 17). M11-04 (#147) and M12 to M15 (#148 to #167) were added on
2026-11-15 (Decision 18), and M14 was redesigned the same day (Decision
19): #157 to #162 were rewritten, and M14-07 to M14-10 (#168 to #171)
added. This file stays the index;
when a task changes, change its issue too.
