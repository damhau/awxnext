# AWX Next — Plan

A re-implementation of AWX with near feature parity, a faster control plane and a modern UI.

## Decisions

| Area | Decision |
|---|---|
| Control plane | Go (single binary, subcommands: `serve`, `migrate`, `agent`, `createsuperuser`) |
| Database | PostgreSQL only — no Redis/NATS required. Dispatch via `FOR UPDATE SKIP LOCKED` + `LISTEN/NOTIFY` wake-ups |
| API | Drop-in compatible with AWX `/api/v2` (pagination, filtering, `related`/`summary_fields`, launch endpoints) so `awx` CLI, `awx.awx` collection and Terraform provider keep working |
| Execution | `ansible-runner` inside Execution Environment containers (pods on k8s, podman/docker on VMs) |
| Remote nodes | Own lightweight Go agent dialing back to control plane (mTLS, streaming) — not Receptor |
| Deployment | k8s-first (Helm), docker-compose for dev / small installs |
| UI | Built from scratch: React + TypeScript + Vite, TanStack Query/Router, Tailwind, React Flow for workflows |
| Data access | Metadata-driven generic resource engine + pgx (no ORM); sqlc optional for hand-written queries |
| Execution environments | Unchanged from AWX: stock `awx-ee` / `ansible-builder` images; ansible-runner transmit/worker/process protocol implemented in Go |
| Job events | Hot path: batched `COPY` into daily partitions with per-job rate caps. After completion: stdout/events archived as compressed blobs (filesystem, optional S3/Azure Blob); Postgres keeps only summaries/metadata |
| HA | All replicas serve API; scheduler/dispatcher leader via Postgres advisory lock |
| Secrets at rest | Envelope encryption: AES-256-GCM data keys wrapped by a master key from env, Vault Transit or Azure Key Vault; key ids for rotation; documented key export |
| Compatibility | Reports AWX 24.6.x version; parity measured by `awx.awx` collection integration tests + awxkit |
| License | Apache-2.0 |

### OIDC

- Group claim configurable (default `groups`); mapping rules such as `{group, superuser}` or `{group, organization, team, role: member|admin}`.
- Mapping is authoritative: memberships granted by rules are revoked at next login when the group disappears from the token.
- The API also accepts bearer JWTs issued by the OIDC provider (machine clients / client-credentials), validated via JWKS.
- A local admin account remains for bootstrap and break-glass.

- Mapping dry-run endpoint: submit sample claims, see resulting superuser/auditor flags, orgs, teams and roles.

### Design changes vs AWX (from the pain-point review)

- **Version history**: every write through the engine stores a snapshot; history tab, diffs and one-click revert for every object.
- **Folders**: optional hierarchical folders inside organizations; role assignments on a folder inherit to everything below. Exposed as an extra `folder` field (ignored by existing AWX clients).
- **Nested teams**: teams can be members of teams; membership is resolved recursively.
- **GitOps (later phase)**: objects carry a reserved `managed_by` field; a reconciler (opt-in per folder) syncs from Git and edits to managed objects are rejected or turned into PRs.
- **Execution is push-based**: an init container drops a static Go shim into the job pod; it runs `ansible-runner worker` and streams output back over a reconnecting connection — no kubelet log tailing (fixes the "killed after ~5 min" problem). The same shim serves remote nodes. Stock EE images keep working.
- **Native git sync**: project checkouts done in Go/git on the control plane, cached per revision and shared by jobs; `requirements.yml` installs cached by content hash; only the dependency install needs an EE. UI shows EE contents (collections, ansible.cfg).
- **API ergonomics**: accurate OpenAPI generated from resource metadata; a simple query language (`status:failed org:Ops`) alongside Django lookups; generic webhooks with JSONPath payload mapping (plus GitHub/GitLab).
- **Workflows/surveys (later phases)**: loop nodes, typed node outputs, edge conditions; dynamic survey dropdowns (inventory/API), conditional fields, validation; "resolved variables" preview with the source of each variable.
- **Observability**: correct Prometheus metrics, OpenTelemetry traces, structured JSON logs.
- Explicitly **not** planned: job-scoped OIDC identity tokens, credential branch/signature policies. Multi-site active-active is out of v1.

### Not carried over from AWX

OAuth2 applications (deprecated in AWX 24; personal tokens remain), custom virtualenvs, Insights projects, isolated nodes.

### Migration from AWX

Importer reading the AWX database directly, plus the AWX `SECRET_KEY` to re-encrypt credentials.

## v1 scope cuts

| Feature | v1 | Later |
|---|---|---|
| Secret lookups | HashiCorp Vault (KV v1/v2), Azure Key Vault | CyberArk, AWS SM, GCP, Thycotic… |
| Projects | Git only (+ manual) | SVN, archive, Insights |
| Notifications | Webhook only | Slack, email, PagerDuty, Teams… |
| Auth | OIDC, groups read from JWT claim → team/org mapping; local admin for bootstrap/break-glass | LDAP, SAML, social |

## Architecture

```
            ┌────────────┐   REST /api/v2 + WS
  UI ──────▶│  API (Go)  │◀────────── awx CLI / collection
            └─────┬──────┘
                  │ Postgres (state, LISTEN/NOTIFY)
            ┌─────▼──────┐
            │ Dispatcher │ schedules (rrule), workflow DAG, capacity-aware placement
            └─────┬──────┘
         local │          │ agent (mTLS stream)
       ┌───────▼──┐   ┌───▼──────────┐
       │ Executor │   │ Remote agent │  → ansible-runner in EE container
       └──────────┘   └──────────────┘
                event stream → batched COPY into partitioned job_events
```

Performance targets vs AWX: job event ingestion via batched `COPY` into a time-partitioned table, no Django ORM overhead, websocket fanout from a single in-process hub, list endpoints using keyset-friendly indexes.

## Phases

0. **Foundation** — repo layout, CI, dev compose, migrations, generic AWX-compatible resource engine (pagination/filters/serializers), local auth (session + tokens), OIDC + mapping dry-run, orgs/users/teams, RBAC core with folders and nested teams, version history, envelope encryption, `managed_by`, OpenAPI generation.
1. **Inventory & credentials** — credential types (managed + custom with injectors), credentials, Vault/Azure lookups, projects + native git sync with revision cache, inventories/hosts/groups/smart inventories, inventory sources, execution environments.
2. **Jobs** — job templates (prompt-on-launch, surveys, limits/tags/forks), launch, push-based executor shim, event streaming + archive, live job output UI, host summaries/facts. *First "runs playbooks" milestone.*
3. **Ops** — schedules (rrule), ad-hoc commands, webhook notifications, activity stream, system jobs (cleanup).
4. **Workflows** — workflow templates, visual editor, approval nodes, convergence, git webhooks (GitHub/GitLab), provisioning callbacks, job slicing.
5. **Enterprise** — instance groups / capacity, remote agent, metrics (`/metrics`, `/api/v2/metrics`), OpenTelemetry, importer from existing AWX, GitOps reconciler.
6. **Advanced UX** — workflow loops/outputs/conditions, dynamic surveys, operator self-service catalog.
7. **Hardening** — benchmarks vs AWX, Helm chart, docs, e2e tests with `awx.awx` collection.

## Repo layout

```
cmd/awxnext/          single binary entrypoint
internal/config       env/flag config
internal/db           pool + embedded SQL migrations
internal/api          router, middleware, generic resource engine, AWX-compat helpers
internal/auth         sessions, tokens, OIDC
internal/rbac         role definitions, assignments, permission checks
internal/...          domain packages (inventory, credentials, projects, jobs, …)
ui/                   React app
deploy/               compose + helm
```

## UI goals (pain points of the current AWX UI to fix)

- **Job output**: virtualized, instant on 100k+ lines, live tail without jank, collapse by play/task, per-host filtering, search, download.
- **Navigation**: command palette (⌘K) to jump to any template/inventory/host/job; deep links for everything; back button that works.
- **Lists**: fast server-side filtering with a real query bar (`status:failed org:Ops`), saved views, column chooser, bulk actions (delete, copy, assign roles, launch).
- **Launch flow**: one-screen launch with prompts + survey, remembers last values, "relaunch with changes", "relaunch on failed hosts".
- **Workflows**: proper graph editor (zoom, auto-layout, drag to connect, inline node config), live run view on the same graph.
- **Dashboards**: what is running right now across the cluster, failure hot-spots, capacity per instance group.
- **Access management**: "who can do what on this object" and "what can this user do" views; role assignment from any object.
- **Editing**: YAML/JSON editors with validation for variables, survey designer with preview.
- **History**: history tab with diffs and one-click revert on every object; "last edited by" and "used by" (workflows, schedules referencing it) shown before edit/delete.
- **Organize**: folders, favorites, saved searches stored per user (not only in the URL).
- **Operator self-service**: catalog of launchable tiles with a simplified form; advanced fields (credentials, EE, verbosity, forks) hidden for non-authors.
- **Job output**: host × task grid with ok/changed/failed counts, "jump to first failure", diff view for changed tasks.
- **Bulk actions**: multi-select relabel, move to folder, grant permissions, enable/disable schedules, delete; inline editing for simple fields.
- **Settings**: searchable sections showing non-default values instead of huge flat forms.
- Dark mode, keyboard shortcuts, responsive layout.

## Visual direction (approved mockups)

Reference: the "AWX Next UX mockups" canvas (templates browser, job output, operator launch, history/diff, ⌘K palette).

- Dense, utilitarian ops-console look: IBM Plex Sans for UI, IBM Plex Mono for identifiers, paths, versions, vars and logs.
- Neutral ground (`#F3F4F1` chrome, white content, `#DCDFDA` hairlines), near-black top bar (`#1B1D1C`), one accent (cobalt `#2346B8`). 3px radii, no gradients, no decorative shadows (only on overlays), no emoji, inline stroke icons.
- Status is encoded by shape and lightness as well as hue: successful = dark green circle/square, failed = orange diamond, running = blue dot with halo, canceled = short gray bar, skipped = light gray, not run = outline.
- Job output is a dark terminal pane with collapsible tasks, line numbers, failed-only filter, search, "first failure"; above it a host × task grid and a plain-language "why it failed" summary.
- Templates: folder tree + saved views + starred; token query bar (`folder:` `inventory:` `status:`); last-12-runs strip; bulk action bar; keyboard hints.
- Operator launch: catalog, dynamic dropdowns (e.g. git tags), conditional fields, inline validation, "what will happen" preview, resolved variables with their source.
- History: version list, field-level and YAML line diffs, revert, "runs around this change", "used by", source of truth.
- ⌘K palette: actions first, then templates, jobs, hosts, inventories, with matched text emphasized.
