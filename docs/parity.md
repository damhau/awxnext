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
and Vault/Azure Key Vault lookups; git projects; job templates with every
launch prompt, surveys, slicing, callbacks and webhooks; ad hoc commands;
workflows with approvals and convergence; schedules; management jobs; remote
execution nodes and Kubernetes container groups; HA on PostgreSQL alone.

The gaps fall in four groups:

1. **Settings that do nothing** (fixed in roadmap M1, 2026-10-28). Every
   setting now has an effect, or says "No effect in awxnext" and why in its
   help (analytics, subscriptions, OAuth2, custom virtualenvs, and
   `LOG_AGGREGATOR_*` until M6-03). A test fails CI when a setting is neither.
2. **API endpoints that AWX clients call**: the bulk API, dashboard, host
   metrics, subscriptions/config POST, instance health checks. The `awx.awx`
   collection's `bulk_*`, `license` and `subscriptions` modules would fail.
   Compatibility with the collection, awxkit and the Terraform provider is
   tested nightly since roadmap M2-01 ([compat/](../compat/README.md)); the
   gaps those suites found are roadmap M2-14 to M2-26.
3. **Deferred by plan**: LDAP, SAML, social, RADIUS and TACACS+ login;
   notification types other than webhook; SVN and archive projects; CyberArk,
   Thycotic, AWS Secrets Manager and Centrify lookups; Helm chart; backup.
4. **UI holes over a finished API**: inventory
   instance groups, credential type management, inventory source schedules,
   `start_at_task`, a few more listed below.

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

6. Bulk API: `bulk/job_launch`, `bulk/host_create`, `bulk/host_delete`.
7. ~~`?host_filter=` on `/hosts/`, `chain__` filters~~ (done, roadmap M1-13).
8. Host metrics API and page (the data is already collected per job), `host_metric_summary_monthly`.
9. Instance group policies (percentage, minimum, list) and `max_forks`; `max_concurrent_jobs` outside container groups.
10. External log aggregation (logstash, splunk, ...) and its test endpoint.
11. Password policy settings; activity stream entries for job launches and for role grants made through the role assignment API.
12. Dashboard endpoints (`/dashboard/`, `/dashboard/graphs/jobs/`).
13. Managed credential types for Insights and RHV (their inventory sources exist but no credential can be picked); CA file injection for the OpenShift/Kubernetes token type.
14. Project refspec is not used when a job checks out an overridden branch, so jobs on refs only the refspec fetches (pull requests) can fail.

**P3: completeness**

15. Relaunch keeps launch-time labels and instance groups; workflow labels passed to nodes; labels and instance group prompts on schedules and nodes.
16. Typed workflow node outputs (the remaining phase 6 item).
17. Named URLs for credentials and other multi-hop formats, `related.named_url`.
18. Websocket groups `jobs-summary`, `schedules-changed` and `control-limit_reached_<user>` (subscribable, never published).
19. UI: `start_at_task`, `prevent_instance_group_fallback` and instance groups on templates and inventories, webhook key display, credential types page, inventory source schedules tab, execution environment and passwords in the ad hoc dialog, node credentials and labels in the workflow editor, EE contents view (promised in PLAN.md).
20. Plan items not built yet: per-job event rate caps, S3/Azure Blob event archives, benchmarks, Helm chart, dark mode, saved views stored per user on the server.

## Authentication, users, organizations, RBAC

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| Local users, superuser and system auditor | Yes | Done | `createsuperuser` CLI also resets a password |
| Password policy settings | Yes | Missing | |
| Organizations: max_hosts, default EE, Galaxy credentials, instance groups | Yes | Done | max_hosts also stops inventory source imports. AWX's public `Ansible Galaxy` credential exists from the start, read-only |
| Organization notification attachments | Yes | Done | |
| Teams | Yes | Done | Plus nested teams |
| `teams/N/projects/`, `teams/N/credentials/`, `users/N/projects/`, `users/N/access_list/` | Yes | Missing | |
| Object roles (`/roles/`, `object_roles`, users/teams roles, role fields per type) | Yes | Done | |
| `access_list` | Yes | Partial | No `direct_access`/`indirect_access` details |
| Role definitions and assignments (DAB RBAC) | Yes | Done | No UI to create custom roles; `role_metadata` and `*_ansible_id` missing |
| Personal access tokens with read/write scope | Yes | Done | Stored hashed, shown once |
| OAuth2 applications, `/api/o/` | Deprecated | By design | Not advertised by `/api/` |
| Session login, CSRF, basic auth, `/me/` | Yes | Done | |
| OIDC login | Yes | Done | Ahead of AWX: rules re-applied at every login, bearer JWTs, dry-run endpoint |
| LDAP, SAML, GitHub/Google/Azure AD | Yes | Later | |
| RADIUS, TACACS+ | Yes | Missing | Not in the plan |
| Session, token and login settings | Yes | Partial | Stored but ignored, see P1 |
| UI: users, teams, organizations, access tabs | Yes | Done | |

## Inventories and credentials

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| Regular, smart and constructed inventories | Yes | Done | Smart filters compiled to SQL and checked when saved |
| Hosts, groups (nested), variables, facts | Yes | Done | `instance_id` is not set by imports |
| Inventory script endpoint | Yes | Partial | No `subset=slice` parameter |
| Inventory instance groups, `prevent_instance_group_fallback` | Yes | Done (API only) | Not in the inventory form |
| Inventory copy | Yes | Done | Groups, hosts, memberships and instance groups |
| Inventory labels, `inventories/N/job_templates/`, `groups/N/job_events/` | Yes | Missing | |
| Asynchronous inventory delete | Yes | Partial | Deletes are synchronous |
| Cloud sources: ec2, gce, azure_rm, vmware, openstack, satellite6, controller, terraform, openshift_virtualization | Yes | Done | AWX's per-source default enabled/instance-id variables are not applied |
| Project file (scm) source | Yes | Partial | `source_vars` not exported as environment |
| rhv, insights sources | Yes | Partial | No managed credential type |
| `file` source, `inventory_import` | Yes | Missing | GitOps inventory `content` covers part of it |
| overwrite, overwrite_vars, enabled_var, host_filter, update on launch with cache | Yes | Done | |
| Inventory updates: cancel, stdout, events, notifications, schedules | Yes | Done | UI has no schedules for sources; notifications are in the source's menu |
| Bulk host create/delete | Yes | Missing | |
| Host metrics | Yes | Partial | Collected per job and cleaned up; no API or UI |
| Managed credential types | Yes | Partial | All but Insights and RHV; Kubernetes CA not injected |
| Custom credential types | Yes | Done | Injectors are Jinja2 (gonja), sandboxed like AWX and checked when saved; `2 ** n` gives a float and `None` prints empty |
| Lookups: HashiCorp Vault KV and SSH, Azure Key Vault | Yes | Done | Vault TLS cert auth missing; Azure falls back to managed identity |
| Lookups: CyberArk, Conjur, Thycotic, AWS Secrets Manager, Centrify | Yes | Later | |
| Credentials: owners, input sources, test, copy, ASK | Yes | Done | |
| Execution environments: pull policy, registry auth, org scope, resolution order | Yes | Done | |
| UI: inventories, hosts, groups, sources, credentials, EEs | Yes | Done | No credential types page |

## Projects, jobs and workflows

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| Git and manual projects | Yes | Done | Native Go sync, one immutable checkout per commit |
| SVN, archive | Yes | Later | Insights projects: by design |
| Branch override, refspec, submodules, delete on update, clean, update on launch with cache | Yes | Done | |
| Requirements (roles, collections) | Yes | Done | Installed in the EE at run time, cached by content |
| Content signature validation | Yes | Partial | Field only, nothing verifies |
| Job template fields, multiple credentials, labels, instance groups, EE | Yes | Done | `MAX_FORKS` not enforced |
| All 16 `ask_*_on_launch` prompts | Yes | Done | |
| Launch fields the template does not prompt for | Yes | Done | Deliberate difference: refused with a 400 instead of ignored (`ignored_fields`); `IGNORE_UNPROMPTED_LAUNCH_FIELDS` gives AWX's behaviour |
| `ansible_*` variables at launch (extra vars, survey questions, schedules, ad hoc) | Yes | Done | Deliberate difference: refused, as they set how Ansible connects and runs; `ALLOW_ANSIBLE_VARS_AT_LAUNCH` gives AWX's behaviour. Template, inventory and host variables are not affected |
| Surveys (all types, password encryption) | Yes | Done | Plus choices from the system, conditional questions, answer formats |
| Relaunch (all, failed hosts) | Yes | Done | Launch-time labels/IGs not kept (roadmap M3-02) |
| `jobs/N/create_schedule/` | Yes | Missing | |
| Job slicing | Yes | Done | `slice_workflow_jobs` sublist missing |
| Job events, stdout formats, host summaries, artifacts | Yes | Done | `job_events/N/children/`, `playbook_counts` missing |
| Ad hoc commands | Yes | Done | UI dialog lacks EE and passwords |
| Workflows: edges, convergence, nested, approvals, artifacts, prompts | Yes | Done | Plus `when` conditions and loops |
| Management-job workflow nodes | Yes | Done | Fixed during the audit |
| Node instance groups prompt; workflow labels passed to nodes | Yes | Missing | |
| Schedules: rrule with exclusions, timezones, saved prompts, preview | Yes | Done | No labels/IG prompts |
| Notifications: webhook, started/success/error/approval events, custom messages | Yes | Done | Attaching needs change on the notification too, as in AWX; `used_by` (awxnext) lists where one is attached |
| Email | Yes | Later | Roadmap M3-01 |
| Slack, PagerDuty, Twilio, Grafana, IRC, Mattermost, Rocket.Chat, AWS SNS | Yes | Out of scope | Dropped on 2026-10-27 |
| GitHub/GitLab/Bitbucket DC webhooks with status posting | Yes | Done | Plus a generic JSONPath-mapped webhook |
| Provisioning callbacks | Yes | Done | |
| Management jobs (cleanups) with schedules | Yes | Done | `host_metric_summary_monthly` missing |
| Dependencies, capacity-aware placement, `allow_simultaneous` | Yes | Done | |

## Platform and operations

| Feature | AWX 24.6 | awxnext | Notes |
|---|---|---|---|
| API root, ping, config, me | Yes | Done | The version root is public with AWX's keys (`inventory`, `constructed_inventory`); `applications/` is always empty, OAuth2 applications being out of scope. `config` has no license POST/DELETE |
| Compatibility suites: `awx.awx` integration targets, `awx` CLI, Terraform provider `ansible/aap` | Yes | Done | Nightly and on demand in CI against a throwaway install over HTTPS ([compat/](../compat/README.md)); each expected failure is listed with its issue in `compat/known-failures.yml` |
| Field validation like AWX's serializers | Yes | Done | Null reads as `""` for text and choice fields and as `false` for booleans, then is validated, as the `awx.awx` modules expect |
| OPTIONS, pagination, filtering, ordering | Yes | Done | Plain filters crossing a relation match the same related row and `chain__` ones each their own, as in AWX; `role_level` needs every permission of the role |
| Named URLs | Yes | Partial | Common forms only |
| Browsable HTML API | Yes | Missing | JSON only |
| Dashboard endpoints | Yes | Missing | The UI's "Now" page replaces the dashboard |
| Bulk API | Yes | Missing | |
| Activity stream | Yes | Partial | No entries for job launches or role assignment API grants |
| Settings API and UI | Yes | Done | Settings with no effect say so in their help ("No effect in awxnext": analytics, subscriptions, OAuth2, custom virtualenvs, `LOG_AGGREGATOR_*` until M6-03); a test keeps it that way |
| External log aggregation | Yes | Missing | JSON logs on stderr instead |
| Prometheus metrics | Yes | Partial | No subsystem metrics or `node`/`metric` parameters; OpenTelemetry traces added |
| Analytics, subscriptions | Yes | By design | Collection `license`/`subscriptions` modules fail |
| Instances: capacity, drain, jobs | Yes | Done | Control nodes use a fixed capacity; `capacity_adjustment` scales instead of interpolating |
| Instance health check endpoint | Yes | Missing | Health is passive (heartbeats, agent registration) |
| Receptor mesh, peers, hop nodes, topology view | Yes | By design | Dial-back agent instead; install bundle replaced by an agent token |
| Instance group policies, `max_forks` | Yes | Partial | Stored, not applied |
| Container groups with `pod_spec_override` | Yes | Done | Push-based shim, no log tailing |
| Job isolation | Yes | Partial | AWX always runs jobs in containers. awxnext's `none` runtime runs them as processes of the server's user: the server's environment is hidden from them, other jobs' files are not (roadmap M6-13) |
| Websockets | Yes | Partial | Three groups never published |
| Event processing | Yes | Done | Batched COPY into daily partitions, archived after N days |
| HA, task and dependency manager | Yes | Done | PostgreSQL only, no Redis |
| `awx-manage` commands | Yes | Partial | No `inventory_import` or instance provisioning commands |
| Backup/restore, operator, Helm | Yes | Missing | Docker image and Compose only; key escrow commands |
| UI: dashboard, approvals bell, instances, management jobs | Yes | Done | Redesigned dashboard; no topology |
| UI: dark mode, translations, about dialog | Yes | Missing | |

## Only in awxnext

- Version history with diffs and one-click revert on every object.
- Folders inside organizations, with roles inherited by everything below; nested teams.
- GitOps sources applying YAML declarations atomically, with dry-run plans, pruning and locked managed objects.
- Importer reading an AWX database and re-encrypting its credentials.
- Envelope encryption of secrets with local, Vault Transit or Azure Key Vault master keys, rotation and escrow.
- Workflow `when` conditions and loops; survey choices from inventories, git refs and the API, conditional questions, answer formats.
- Operator catalog (`/run`), launch page with "what will happen" and each variable's source, host × task grid, "why it failed" summary, ⌘K palette, query-language filters.
- Native git sync with per-commit checkouts and content-addressed requirements cache.
- Push-based execution for remote nodes and container groups; job event archive.
- OIDC bearer JWTs, regex group rules and mapping dry-run.
- Generic webhooks with JSONPath mapping; OpenAPI document; OpenTelemetry traces.
