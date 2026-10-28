# AWX Next

A re-implementation of [AWX](https://github.com/ansible/awx) with a Go control plane,
an AWX-compatible `/api/v2`, and a new UI. See [docs/PLAN.md](docs/PLAN.md) for the
design, scope and roadmap.

**Status:** early development. The API, job execution (local, remote agents,
Kubernetes container groups), workflows, schedules, notifications, GitOps, the AWX
importer and the web UI are in place; see [docs/PLAN.md](docs/PLAN.md) for what's next and
[docs/parity.md](docs/parity.md) for a feature by feature comparison with AWX 24.6.

## Try it with Docker Compose

```sh
cd deploy/compose
export AWXNEXT_SECRET_KEY=$(openssl rand -hex 32) AWXNEXT_DB_PASSWORD=$(openssl rand -hex 16)
AWXNEXT_ADMIN_PASSWORD=admin docker compose up -d --build
docker compose exec awxnext seed-demo --jobs    # optional demo content
```

Keep the secret key: it decrypts the stored credentials. The database password only
applies when the database volume is first created.

Open http://localhost:8052 and sign in as `admin`. The Compose install uses the
`all-in-one` image, where jobs run inside the awxnext container with ansible-runner
(ansible-core only: install collections with a project `requirements.yml`).
`AWXNEXT_ADMIN_PASSWORD` creates the admin user, and resets its password on every start
while it is set.

## Run from source

Needs Go, Node 22 (or 20.19+), PostgreSQL and, to run jobs, ansible-runner on the PATH.

```sh
make dev-db                     # local PostgreSQL (or point AWXNEXT_DATABASE_URL at your own)
make ui build                   # web UI, then the binary that embeds it
AWXNEXT_DEV=1 ./bin/awxnext createsuperuser --username admin --password admin
export AWXNEXT_CONTAINER_RUNTIME=none AWXNEXT_PROJECTS_ROOT=~/.local/share/awxnext/projects AWXNEXT_JOBS_ROOT=~/.local/share/awxnext/jobs
make run                        # http://localhost:8052
```

`make ui-dev` serves the UI with hot reload on http://localhost:5173 against the server
on :8052. `uv run --no-project scripts/seed-demo.py --jobs` fills it with demo content.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `AWXNEXT_DATABASE_URL` | `postgres://postgres@localhost:5432/awxnext` | PostgreSQL connection |
| `AWXNEXT_SECRET_KEY` | — (required unless `AWXNEXT_DEV=1`) | Master key for the local key provider |
| `AWXNEXT_KMS` | `local` | `local`, `vault-transit` or `azure-keyvault` master key provider |
| `AWXNEXT_BASE_URL` | `http://localhost:8052` | External URL (OIDC redirects, notifications) |
| `AWXNEXT_LISTEN` | `:8052` | Listen address |
| `AWXNEXT_CONTAINER_RUNTIME` | `podman` | How local jobs run: `podman` or `docker` (execution environment image), or `none` (ansible-runner on the PATH) |
| `AWXNEXT_PROJECTS_ROOT` | `/var/lib/awxnext/projects` | Project checkouts |
| `AWXNEXT_JOBS_ROOT` | `/var/lib/awxnext/jobs` | Job working directories |
| `AWXNEXT_UI_DIR` | — | Serve the web UI from this directory instead of the embedded build |

Vault Transit: `AWXNEXT_KMS_VAULT_ADDR`, `_KEY`, `_MOUNT`, and `_TOKEN`, `_ROLE_ID`/`_SECRET_ID`
or `_K8S_ROLE`. Azure Key Vault: `AWXNEXT_KMS_AZURE_VAULT_URL`, `_KEY`, `_TENANT_ID`, `_CLIENT_ID`,
and `_CLIENT_SECRET`, workload identity, or `_MANAGED_IDENTITY=1`.

### Job isolation and the `none` runtime

With `AWXNEXT_CONTAINER_RUNTIME=none`, the default of the all-in-one image, jobs are not
containers but processes of the server's own user. The server keeps its environment
(secret key, database URL, key provider tokens) unreadable to them, but a playbook can
still read the files of other jobs running at the same time, change project checkouts,
and reach the database over the network. Use `none` for one trusted team, with your own
secret key and database password. Otherwise run jobs in containers (`podman`, `docker`),
on remote execution nodes or in container groups. The server logs a warning at start
when it uses `none` or the Compose example secrets.

### Encryption keys

Secrets are encrypted with data keys that are wrapped by the master key. Keep an escrow copy:

```sh
awxnext keys export > keys.json   # store offline; recovers secrets if the master key is lost
awxnext keys import keys.json     # restore with the currently configured master key
awxnext keys rotate               # new data key for new secrets
awxnext keys rewrap               # move to a new master key (old one in AWXNEXT_OLD_*)
```

## API

The API follows AWX `/api/v2` so the `awx` CLI, the `awx.awx` collection and the Terraform
provider work against it. Additions: `/api/v2/folders/`, `/<resource>/<id>/history/`
(diff and revert), `/api/v2/openapi.json`, `/api/v2/settings/oidc/test/` (mapping dry-run).

## Development

```sh
make test   # vet + unit and integration tests
cd ui && npx tsc --noEmit -p . && npx vitest run   # UI typecheck and unit tests
```

The integration tests use the PostgreSQL server in `AWXNEXT_TEST_DATABASE_URL`, or start a
throwaway PostgreSQL 16 when it isn't set (its binaries are downloaded once from Maven Central
into the user cache directory). Job tests run only when `ansible-runner` is on the PATH; they
are skipped otherwise.

Licensed under the Apache License 2.0.
