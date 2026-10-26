# AWX Next

A re-implementation of [AWX](https://github.com/ansible/awx) with a Go control plane,
an AWX-compatible `/api/v2`, and a new UI. See [docs/PLAN.md](docs/PLAN.md) for the
design, scope and roadmap.

**Status:** early development. Phase 0 (foundation) is in place: the AWX-compatible
resource engine, authentication (local, tokens, OIDC with group mapping), RBAC with
folders and nested teams, version history, envelope encryption and the settings API.
Inventories, credentials, projects and job execution are next.

## Quick start

```sh
make dev-db                     # local PostgreSQL (or point AWXNEXT_DATABASE_URL at your own)
AWXNEXT_DEV=1 ./bin/awxnext createsuperuser --username admin --password admin
make run                        # http://localhost:8052/api/v2/
```

Or with Docker: `docker compose -f deploy/compose/docker-compose.yml up -d`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `AWXNEXT_DATABASE_URL` | `postgres://postgres@localhost:5432/awxnext` | PostgreSQL connection |
| `AWXNEXT_SECRET_KEY` | — (required unless `AWXNEXT_DEV=1`) | Master key for the local key provider |
| `AWXNEXT_KMS` | `local` | `local`, `vault-transit` or `azure-keyvault` master key provider |
| `AWXNEXT_BASE_URL` | `http://localhost:8052` | External URL (OIDC redirects, notifications) |
| `AWXNEXT_LISTEN` | `:8052` | Listen address |

Vault Transit: `AWXNEXT_KMS_VAULT_ADDR`, `_KEY`, `_MOUNT`, and `_TOKEN`, `_ROLE_ID`/`_SECRET_ID`
or `_K8S_ROLE`. Azure Key Vault: `AWXNEXT_KMS_AZURE_VAULT_URL`, `_KEY`, `_TENANT_ID`, `_CLIENT_ID`,
and `_CLIENT_SECRET`, workload identity, or `_MANAGED_IDENTITY=1`.

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
make test   # vet + unit and integration tests (needs PostgreSQL, see make dev-db)
```

Licensed under the Apache License 2.0.
