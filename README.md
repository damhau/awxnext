# AWX Next

A re-implementation of [AWX](https://github.com/ansible/awx) with a Go control plane,
an AWX-compatible `/api/v2`, and a new UI. See [docs/PLAN.md](docs/PLAN.md) for the
design, scope and roadmap.

**Status:** early development. The API, job execution (local, remote agents,
Kubernetes container groups), workflows, schedules, notifications, GitOps, the AWX
importer and the web UI are in place; see [docs/PLAN.md](docs/PLAN.md) for what's next and
[docs/parity.md](docs/parity.md) for a feature by feature comparison with AWX 24.6, and
[docs/benchmarks.md](docs/benchmarks.md) for measures against it.

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

## Install on Kubernetes with Helm

```bash
helm install awxnext oci://ghcr.io/damhau/charts/awxnext --namespace awxnext --create-namespace
kubectl -n awxnext get secret awxnext-secrets -o jsonpath='{.data.admin-password}' | base64 -d
```

The chart (`deploy/helm/awxnext`) runs the all-in-one image:

- `replicaCount` API and control plane replicas, each an instance named after its pod,
  reaching the others by its pod IP;
- a bundled PostgreSQL (`postgresql.bundled`, a single-replica StatefulSet) or an external one
  (`postgresql.external.existingSecret`, the URL under `url`);
- the master key generated into a kept secret, or `secretKey.existingSecret` (key `secret-key`);
- `ingress.enabled`, `ingress.host`, `ingress.tls`;
- a service account and Role letting container groups without a Kubernetes credential run their
  job pods in the release's namespace (`containerGroups.rbac`);
- the migrations as a Job before each upgrade (`migrations.enabled`);
- an optional remote execution node (`agent.enabled`, with its instance's token in
  `agent.tokenSecret`).

Keep a copy of the master key (or a key escrow, `awxnext keys export`): the secret is kept when
the release is uninstalled, and secrets cannot be read without it.

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

Every environment variable and setting is in the [configuration reference](docs/configuration.md),
generated from the code (`go run ./cmd/gendocs`). Installing, upgrading, high availability,
migrating from AWX and the master key: the [operator guide](docs/operations.md).

Capacity follows AWX's formula: 4 forks per CPU and one fork per 100 MB of memory after 2 GiB
(none set aside in Kubernetes). AWX's `SYSTEM_TASK_FORKS_CPU` and `SYSTEM_TASK_FORKS_MEM` change
these ratios, and `SYSTEM_TASK_ABS_CPU` (`2`, `1500m`) and `SYSTEM_TASK_ABS_MEM` (`8Gi`) replace a
control node's measured CPU and memory. An instance's `capacity_adjustment` goes from the smaller of
its CPU and memory capacities (0) to the larger (1).

External logging is set under Settings → Logging, as in AWX (`LOG_AGGREGATOR_*`): service logs,
activity stream entries, job events and gathered facts go to logstash, Splunk, Loggly, Sumo Logic
or any HTTPS/HTTP, TCP or UDP receiver, as JSON. Activity stream entries, job events and facts are
INFO messages, so they need `LOG_AGGREGATOR_LEVEL` at INFO.

### Job isolation and the `none` runtime

With `AWXNEXT_CONTAINER_RUNTIME=none`, the default of the all-in-one image, jobs are not
containers but processes. In the all-in-one image each job runs as a job user of its own
(`awxjob0` to `awxjob127`), through `awxnext-jobexec`, a helper that only the server's group
can run and that has the file capabilities setuid, setgid, chown and kill and no others
([internal/jobexec](internal/jobexec/jobexec.go)):

- the job sees its own directory (0700, its user's) and home, emptied before and after
  it, and its temporary directory (`TMPDIR`);
- it cannot read the server's files (project checkouts, other jobs' private data, the
  server's environment), write to them, or list the jobs and projects directories;
- it cannot reach another running job's files, nor its processes' environment, working
  directory or open files in `/proc` (their command lines stay visible, as on any Linux);
- it cannot gain privileges (no_new_privs: setuid binaries such as `su` do nothing);
- when it ends or is canceled, every process of its user is killed, including those it
  moved to another session, and its directory removed.

Playbooks that wrote to a shared place, such as one `/tmp` file used by several jobs, now
get "Operation not permitted" when the file was made by another job: use the job's own
`TMPDIR`. Jobs can still reach the database and the API over the network, as in AWX.

`AWXNEXT_JOB_ISOLATION` is `auto` (the default: job users when the helper works, else jobs
run as the server's user with a warning at start), `users` (the server does not start
without them) or `off`. The helper does not work where no_new_privs is set on the server
(`allowPrivilegeEscalation: false`, Docker's `no-new-privileges`), and job users are refused
where a directory above the jobs or projects directories is writable by others, such as the
Helm chart's `emptyDir` volume (0777): on Kubernetes, run jobs in container groups. Without
job users, use `none` for one trusted team only. Otherwise run jobs in containers
(`podman`, `docker`), on remote execution nodes or in container groups. The server logs a
warning at start when jobs run as its own user or with the Compose example secrets.

### Backup and restore

```sh
awxnext backup -o awxnext.tar.gz --passphrase-file pass.txt   # or AWXNEXT_BACKUP_PASSPHRASE
awxnext restore --passphrase-file pass.txt awxnext.tar.gz      # into an empty database
```

A backup holds the database (`pg_dump`, so PostgreSQL's client tools of the server's major
version are needed; the image has them, or set `AWXNEXT_PG_DUMP` and `AWXNEXT_PG_RESTORE`) and
a manifest of the install's settings that are not secrets. With a passphrase it also holds the
data keys' escrow, encrypted with it (scrypt and AES-256-GCM): the backup then restores under
another master key. Without one, restoring needs the backed up install's master key.

`restore` loads the backup into the empty database `AWXNEXT_DATABASE_URL` names, re-wraps the
data keys with this install's master key, applies this version's migrations, marks the backed
up install's nodes unavailable (they come back when they connect to this database), and queues
an update of every SCM project, run when the server starts, to rebuild the project checkouts.
The files of manual projects are not in the backup. With Compose:

```sh
docker compose exec -T awxnext awxnext backup -o - > awxnext.tar.gz
```

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

### Importing an inventory file

`awxnext inventory-import` is `awx-manage inventory_import`, with the same options. It runs
`ansible-inventory` on the machine running the command and records the import as an update
of a `file` source of the inventory:

```bash
awxnext inventory-import --inventory-name Production --source /etc/ansible/hosts --overwrite
```

### Instances and instance groups

awx-manage's instance commands, with their options and output, under their names with dashes or
underscores. Control nodes register themselves when they start; these are for other nodes and
for groups:

```bash
awxnext provision-instance --hostname exec-1.example.com --node_type execution   # or --uuid U
awxnext deprovision-instance --hostname exec-1.example.com
awxnext register-queue --queuename dmz --hostnames exec-1.example.com,exec-2.example.com \
  --instance_minimum 1 --instance_percent 0       # also --is_container_group, --pod_spec_override,
                                                  # --max_forks, --max_concurrent_jobs
awxnext remove-from-queue --queuename dmz --hostname exec-2.example.com
awxnext unregister-queue --queuename dmz
awxnext list-instances --no-color
```

As in AWX, `register-queue` only adds instances, and sets the percentage and minimum (0 when not
given); the group's policies then choose its members. `default` and `controlplane` cannot be
unregistered.

### Signed projects

A project with a signature validation credential (a *GPG Public Key*) checks every sync as
AWX does: the GPG signature of `.ansible-sign/sha256sum.txt`, then the checkout against it.
A changed, added or removed file, or a wrong signature, fails the sync, and the project keeps
the last revision that passed. Sign with [`ansible-sign`](https://github.com/ansible/ansible-sign):

```bash
ansible-sign project gpg-sign .   # reads MANIFEST.in, writes .ansible-sign/
```

Every file counts unless `MANIFEST.in` excludes it. `.git` never counts, so the
`prune .git` that AWX's clones need is not required. As in AWX, the checkouts of jobs, such
as a branch override, are not verified.

## Development

```sh
make test        # vet + the short suite (go test -short), about 30 s
make test-full   # vet + every test, with the race detector
cd ui && npx tsc --noEmit -p . && npx vitest run   # UI typecheck and unit tests
```

The integration tests use the PostgreSQL server in `AWXNEXT_TEST_DATABASE_URL`, or start a
throwaway PostgreSQL 16 when it isn't set (its binaries are downloaded once from Maven Central
into the user cache directory). The short suite skips the slow tests (`slow(t)` in `test/`):
those that run jobs with ansible-runner, and a few others. In the full suite, job tests run only
when `ansible-runner` is on the PATH, and the object storage tests when
`AWXNEXT_TEST_S3_ENDPOINT` (MinIO) and `AWXNEXT_TEST_AZURITE_BLOB_ENDPOINT` (Azurite) are set.

CI runs in tiers:

| Workflow | When | What |
|---|---|---|
| `ci.yml` | every push | gofmt, vet, the short suite, the UI's typecheck, unit tests and build |
| `deploy.yml` | pushes that change the Dockerfile, `deploy/`, the job helper or the agent | the Helm chart in kind, the all-in-one image with Compose |
| `full.yml` | nightly, and by hand before a milestone closes (`gh workflow run full.yml`) | every Go test with the race detector, MinIO and Azurite; `deploy.yml`; the compatibility suites (`compat.yml`), a job each |
| `bench.yml` | by hand | the [benchmarks against AWX](docs/benchmarks.md) |

Licensed under the Apache License 2.0.
