# Operator guide

Installing, upgrading and running awxnext, and moving to it from AWX. Every variable and
setting named here is in the [configuration reference](configuration.md).

## Install

awxnext is one binary (`awxnext serve`) and a PostgreSQL database: 16 is the version CI tests, 13
or newer has what awxnext uses. Two
images are built from the [Dockerfile](../Dockerfile):

- **all-in-one** (`ghcr.io/damhau/awxnext-all-in-one`): the binary, git, ansible-core and
  ansible-runner, and PostgreSQL 16's client tools. Jobs can run inside the server's
  container (`AWXNEXT_CONTAINER_RUNTIME=none`); Compose and the Helm chart use it.
- **default** (`ghcr.io/damhau/awxnext`): the binary only, on distroless. Jobs run in
  execution environment containers or container groups. Project syncs need `git`, so control
  nodes run from the all-in-one image or an image of yours with git.

Whatever the install, choose the **master key** first (see [Master key](#master-key)): with
the default local provider it is `AWXNEXT_SECRET_KEY`, and losing it makes every stored
secret unreadable.

### Docker Compose

For one machine: PostgreSQL and awxnext ([deploy/compose](../deploy/compose/docker-compose.yml)).

```sh
cd deploy/compose
export AWXNEXT_SECRET_KEY=$(openssl rand -hex 32) AWXNEXT_DB_PASSWORD=$(openssl rand -hex 16)
echo "AWXNEXT_SECRET_KEY=$AWXNEXT_SECRET_KEY" >> .env    # keep both: docker compose reads .env
echo "AWXNEXT_DB_PASSWORD=$AWXNEXT_DB_PASSWORD" >> .env
AWXNEXT_ADMIN_PASSWORD='a strong password' docker compose up -d --build
```

Then open `http://localhost:8052` and sign in as `admin`. Set `AWXNEXT_BASE_URL` to the URL
users reach it at (behind a TLS proxy: `https://awxnext.example.com`). The database password
applies only when its volume is first created. Jobs run inside the awxnext container
(runtime `none`), each as a job user of its own; the log says "jobs run as job users" at
start. See "Job isolation" in the [README](../README.md#job-isolation-and-the-none-runtime).

### Kubernetes with Helm

Each release (a `v*` tag) publishes the images and the chart, as `oci://ghcr.io/damhau/charts/awxnext`:

```sh
helm install awxnext oci://ghcr.io/damhau/charts/awxnext --namespace awxnext --create-namespace \
  --set ingress.enabled=true --set ingress.host=awxnext.example.com
kubectl -n awxnext get secret awxnext-secrets -o jsonpath='{.data.admin-password}' | base64 -d
```

From a checkout instead (before the first release, or to try a change), build the all-in-one
image, push it where the cluster pulls from (or `kind load docker-image`), and install the chart
from `deploy/helm/awxnext`:

```sh
docker build --target all-in-one -t registry.example.com/awxnext-all-in-one:dev .
docker push registry.example.com/awxnext-all-in-one:dev
helm install awxnext deploy/helm/awxnext --namespace awxnext --create-namespace \
  --set image.repository=registry.example.com/awxnext-all-in-one --set image.tag=dev
```

The chart ([deploy/helm/awxnext](../deploy/helm/awxnext/values.yaml)) creates the master key,
the admin password and, with the bundled PostgreSQL, its password, in the secret
`awxnext-secrets`, kept across upgrades and when the release is uninstalled. For production:

- an external PostgreSQL: `--set postgresql.bundled=false --set postgresql.external.existingSecret=db`,
  the secret holding the URL under `url`;
- your own master key: `--set secretKey.existingSecret=awxnext-master-key` (key `secret-key`), or
  a KMS (Vault Transit, Azure Key Vault) through `env`;
- `replicaCount: 2` or more (see [High availability](#high-availability));
- jobs in container groups: create an instance group with **Container group** checked and no
  credential, and the chart's service account runs the job pods in the release's namespace.
  Jobs that run on the replicas themselves (runtime `none`) run as the server's user there:
  the chart's `emptyDir` is writable by others, so job users are refused (README, "Job
  isolation").

### From the binary

```sh
export AWXNEXT_DATABASE_URL=postgres://awxnext:...@db:5432/awxnext AWXNEXT_SECRET_KEY=...
awxnext createsuperuser --username admin --password '...'
awxnext serve
```

`serve` applies the database migrations at start. It needs `git` for project syncs, and for
`AWXNEXT_CONTAINER_RUNTIME` either `podman`, `docker`, or `none` with ansible-runner on the PATH.

## Upgrade

1. **Back up** (`awxnext backup`, see [below](#backup-and-restore)). Migrations only go forward:
   to go back to a previous version, restore the backup taken before the upgrade.
2. **Upgrade**:
   - Compose: `git pull` (or a new image tag), then `docker compose up -d --build`. The new
     container applies the migrations as it starts.
   - Helm: `helm upgrade awxnext oci://ghcr.io/damhau/charts/awxnext --version <new>
     --reuse-values` (or `deploy/helm/awxnext` with the new image tag). A Job applies the
     migrations, then the replicas roll.
   - Binary: replace it, then restart each node. The first to start migrates; the others wait
     on the migration lock.
3. Check `GET /api/v2/ping/` (the version) and `awxnext list-instances` (every node back).

Jobs running on a node that stops are failed when it starts again ("Task was marked as running
at system start up"), and jobs of a node that never comes back are reaped, as in AWX. Workflow
jobs keep running.

## Migrate from AWX

`awxnext import-awx` reads an AWX 24 database and copies its configuration:

- users (with their password hashes), organizations and teams;
- credential types and credentials, decrypted with AWX's `SECRET_KEY` and encrypted again
  with awxnext's keys;
- execution environments and instance groups;
- inventories and their sources;
- projects, job templates and workflows;
- system job templates and their schedules, and other schedules;
- notification templates (email and webhook);
- role grants;
- the login settings: LDAP, SAML, OIDC, GitHub, Google and Azure AD.

Job history and instances (nodes) are not imported. Import into a new awxnext: users that exist
already there are kept, and organizations of the same name merged.

1. **Find AWX's database URL and `SECRET_KEY`.** With awx-operator, they are in the secrets
   `<awx>-postgres-configuration` and `<awx>-secret-key` (key `secret_key`); the bundled database
   is reachable with `kubectl port-forward svc/<awx>-postgres-15 5432`. In AWX's containers, the
   key is `/etc/tower/SECRET_KEY`.
2. **Dry run.** Everything is imported in a transaction that is then rolled back, and a report
   is printed: counts and warnings, for what is skipped (Subversion projects, inventory sources
   and notification types awxnext does not have, RADIUS and TACACS+ settings).

   ```sh
   awxnext import-awx --source 'postgres://awx:...@awx-db:5432/awx' --secret-key "$AWX_SECRET_KEY" --dry-run
   ```

3. **Import.** Run it again without `--dry-run`, on an awxnext connected to its own database.
   AWX keeps running: the import only reads.
4. **After the import:**
   - sync the projects (Projects, **Sync**);
   - register execution nodes again (Instances, **Add node**, then the agent);
   - for container groups, check their credential or the service account;
   - point users, webhooks and callbacks at awxnext's URL.

The `awx` CLI, the `awx.awx` collection and the Terraform provider work against awxnext
unchanged ([parity](parity.md)).

## High availability

Run two or more control nodes on one database:

- each with a unique `AWXNEXT_NODE_NAME`, and an `AWXNEXT_INTERNAL_URL` the others reach (the
  Helm chart sets both from the pod);
- the same master key configuration, and the same `AWXNEXT_BASE_URL`;
- behind a load balancer: any node serves the API, the UI and websockets, with health checks
  on `/api/v2/ping/`. No session affinity is needed; live job events reach viewers on every
  node.

One node at a time dispatches jobs, by a database lock; when it stops, another takes over. Each
node runs jobs up to its capacity. Instance groups choose where jobs run: AWX's membership
policies, `max_concurrent_jobs` and `max_forks`. `awxnext list-instances` shows the nodes,
their groups and heartbeats.

Remote execution nodes run `awxnext agent`, which dials back to the control plane: no inbound
port, any node can take its connection. Container groups run each job in a pod.

The database is the one shared piece: use a PostgreSQL with failover, such as a managed
service, Patroni or CloudNativePG. Nodes reconnect when it comes back.

## Master key

Secrets (credential inputs, secret settings) are encrypted with data keys, which are wrapped by
the master key. The master key provider is `AWXNEXT_KMS`:

- `local` (default): derived from `AWXNEXT_SECRET_KEY`. Generate it with
  `openssl rand -hex 32` and store it like a password.
- `vault-transit`: a Vault Transit key (`AWXNEXT_KMS_VAULT_ADDR`, `_KEY`, and a token, AppRole or
  Kubernetes authentication). The key never leaves Vault.
- `azure-keyvault`: an Azure Key Vault key (`AWXNEXT_KMS_AZURE_VAULT_URL`, `_KEY`, and a service
  principal, workload identity or managed identity).

Keep an escrow of the data keys offline: it recovers the secrets if the master key is lost.

```sh
awxnext keys export > keys.json        # the data keys, in clear: store it offline
awxnext keys import keys.json          # wrap them with the master key configured now
```

**Rotation:**

- `awxnext keys rotate` makes a new data key for new secrets; existing secrets keep theirs.
- To move to a **new master key** (a new secret, another KMS), set the new one in the usual
  variables and the old one with the `AWXNEXT_OLD_` prefix (`AWXNEXT_OLD_SECRET_KEY`,
  `AWXNEXT_OLD_KMS`...), then run `awxnext keys rewrap`, then start the nodes with the new key
  only.

## Backup and restore

```sh
awxnext backup -o awxnext.tar.gz --passphrase-file pass.txt
awxnext restore --passphrase-file pass.txt awxnext.tar.gz       # into an empty database
```

A backup holds the database (`pg_dump`) and, with a passphrase, the escrow of the data keys,
encrypted with it, so it restores under another master key. `restore` loads it into an empty
database, wraps the data keys with the master key configured, applies newer migrations, marks
the backed up install's nodes unavailable, and queues an update of every SCM project to rebuild
the checkouts. Manual projects' files are not in the backup.

`restore` needs the master key the backup was made under, unless it has the escrow: then the
master key configured now. With Compose, back up from the running container, and restore with
only PostgreSQL started, on a new machine or new volumes:

```sh
docker compose exec -T -e AWXNEXT_BACKUP_PASSPHRASE='...' awxnext awxnext backup -o - > awxnext.tar.gz

docker compose up -d postgres         # a new database: no awxnext has started on it
docker compose run --rm -T -e AWXNEXT_BACKUP_PASSPHRASE='...' awxnext restore - < awxnext.tar.gz
docker compose up -d
```

On Kubernetes, back up from a replica. To restore, install a release with no replica, so its
database stays empty, and restore from a pod of the release's image and environment that runs
nothing; then give the release its replicas. A new release has a new master key: restore a
backup with the escrow (a passphrase), or install with `secretKey.existingSecret` holding the
backed up install's key. The CI `helm` job runs these commands.

```sh
kubectl -n awxnext exec deploy/awxnext -- env AWXNEXT_BACKUP_PASSPHRASE='...' awxnext backup -o - > awxnext.tar.gz

helm install awxnext oci://ghcr.io/damhau/charts/awxnext -n awxnext --create-namespace --set replicaCount=0
kubectl -n awxnext get deployment awxnext -o json | jq '{apiVersion: "v1", kind: "Pod",
    metadata: {name: "awxnext-restore"}, spec: (.spec.template.spec | .restartPolicy = "Never"
    | .containers[0] |= (.command = ["sleep", "infinity"] | del(.args, .readinessProbe, .livenessProbe)))}' |
  kubectl -n awxnext apply -f -
kubectl -n awxnext wait --for=condition=Ready pod/awxnext-restore --timeout 5m
kubectl -n awxnext exec -i awxnext-restore -- env AWXNEXT_BACKUP_PASSPHRASE='...' awxnext restore - < awxnext.tar.gz
kubectl -n awxnext delete pod awxnext-restore
helm upgrade awxnext oci://ghcr.io/damhau/charts/awxnext -n awxnext --reuse-values --set replicaCount=1
```

## Job event archives

A finished job's events stay in the database for `JOB_EVENTS_HOT_DAYS` (14), then become one
compressed file per job. Viewing the output of an archived job reads it back for a day.
Archives are kept in the database by default; to keep the database small, put them in object
storage (Settings, Jobs, or `/api/v2/settings/jobs/`):

| Setting | S3 or S3-compatible | Azure Blob Storage |
|---|---|---|
| `JOB_EVENTS_ARCHIVE_STORAGE` | `s3` | `azure_blob` |
| `JOB_EVENTS_ARCHIVE_BUCKET` | the bucket (it must exist) | the container (it must exist) |
| `JOB_EVENTS_ARCHIVE_ENDPOINT` | empty for AWS; else the service's URL, such as `https://minio.example.com:9000` | `https://<account>.blob.core.windows.net/` |
| `JOB_EVENTS_ARCHIVE_REGION` | the region (else `AWS_REGION`, else `us-east-1`) | |
| `JOB_EVENTS_ARCHIVE_CREDENTIAL` | an Amazon Web Services credential (access key), or empty: AWS's default chain (environment, shared files, EKS web identity, instance role) | a Microsoft Azure Resource Manager credential (service principal), or empty: `AZURE_STORAGE_CONNECTION_STRING`, else Azure's default chain (environment, workload identity, managed identity) |

Objects are named `<prefix><INSTALL_UUID>/<job id>.jsonl.gz` (`JOB_EVENTS_ARCHIVE_PREFIX`,
`job-events/` by default), so several installs can share a bucket. Archives made before a change
stay readable where they are, and deleting a job deletes its object at the next archive run
(every 10 minutes). A backup holds the database only: archives in object storage are not in it, so keep
the bucket's own backups or versioning.

## Observe

- **Metrics:** `/metrics` (Prometheus; `AWXNEXT_METRICS_TOKEN` to protect it) and
  `/api/v2/metrics/`, AWX's.
- **Traces:** OpenTelemetry, with `OTEL_EXPORTER_OTLP_ENDPOINT`.
- **Logs:** JSON on stderr (`AWXNEXT_LOG_FORMAT=json`), and AWX's external logging to
  logstash, Splunk, Loggly, Sumo Logic or any HTTPS, TCP or UDP receiver (Settings, Logging).
- **Instance health:** the Instances page, `awxnext list-instances`, and
  `/api/v2/instances/N/health_check/`.
