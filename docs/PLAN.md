# AWX Next — Plan

A re-implementation of AWX with near feature parity, a faster control plane and a modern UI.

## Decisions

| Area | Decision |
|---|---|
| Control plane | Go (single binary, subcommands: `serve`, `migrate`, `agent`, `createsuperuser`) |
| Database | PostgreSQL only. `LISTEN/NOTIFY` for dispatch + websocket fanout (no Redis) |
| API | Drop-in compatible with AWX `/api/v2` (pagination, filtering, `related`/`summary_fields`, launch endpoints) so `awx` CLI, `awx.awx` collection and Terraform provider keep working |
| Execution | `ansible-runner` inside Execution Environment containers (pods on k8s, podman/docker on VMs) |
| Remote nodes | Own lightweight Go agent dialing back to control plane (mTLS, streaming) — not Receptor |
| Deployment | k8s-first (Helm), docker-compose for dev / small installs |
| UI | React + TypeScript + Vite, TanStack Query/Router, Tailwind, React Flow for workflows |

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

0. **Foundation** — repo layout, CI, dev compose, migrations, generic AWX-compatible resource engine (pagination/filters/serializers), local auth (session + tokens), orgs/users/teams, RBAC core.
1. **Inventory & credentials** — credential types (managed + custom with injectors), credentials (encrypted at rest), Vault/Azure lookups, projects + git sync, inventories/hosts/groups/smart inventories, inventory sources, execution environments.
2. **Jobs** — job templates (prompt-on-launch, surveys, limits/tags/forks), launch, executor via ansible-runner, event streaming, live job output UI, host summaries/facts. *First "runs playbooks" milestone.*
3. **Ops** — schedules (rrule), ad-hoc commands, webhook notifications, activity stream, system jobs (cleanup).
4. **Workflows** — workflow templates, visual editor, approval nodes, convergence, git webhooks (GitHub/GitLab), provisioning callbacks, job slicing.
5. **Enterprise** — OIDC with JWT group mapping, instance groups / capacity, remote agent, metrics (`/metrics`, `/api/v2/metrics`), settings API, importer from existing AWX.
6. **Hardening** — benchmarks vs AWX, Helm chart, docs, e2e tests with `awx.awx` collection.

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
