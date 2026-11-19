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

A new install starts with one organization, `Default`, whose Galaxy credential is the public
`Ansible Galaxy`, as in AWX. AWX's demo project, inventory, credential and job template aren't
created. Each start creates `Default` again when no organization is left. An
[import from AWX](#migrate-from-awx) merges AWX's own `Default` into it.

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

Each push to `main` that passes CI also publishes the chart, as a pre-release of the next
release: version `<next release>-main.<commit time>` (UTC, `YYYYMMDDHHMMSS`), such as
`0.1.0-main.20261119110604`, whose default image is that commit's
`awxnext-all-in-one:main-<commit time>-<commit>`. Helm skips pre-releases unless you pass
`--devel` (the newest) or `--version`, so before the first release, add one of them to the
command above.

From a checkout instead (to try a change), build the all-in-one
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
  the secret holding the URL under `url` (or `postgresql.external.key`: `uri` for the
  `<cluster>-app` secret of a CloudNativePG cluster);
- your own master key: `--set secretKey.existingSecret=awxnext-master-key` (key `secret-key`), or
  a KMS (Vault Transit, Azure Key Vault) through `env`;
- `replicaCount: 2` or more (see [High availability](#high-availability));
- jobs in container groups: create an instance group with **Container group** checked and no
  credential, and the chart's service account runs the job pods in the release's namespace.
  Jobs that run on the replicas themselves (runtime `none`) run as the server's user there:
  the chart's `emptyDir` is writable by others, so job users are refused (README, "Job
  isolation").

The pods run as their image's user, with no privileges, no capabilities, the `RuntimeDefault`
seccomp profile and a read-only root file system, with `emptyDir` volumes where they write
(`podSecurityContext` and `securityContext`, and the same under `postgresql` for the bundled
PostgreSQL, which runs as its image's `postgres` user, uid 999). With the `podman` or `docker`
runtime (`containerRuntime`, `agent.runtime`), give `securityContext` what the runtime needs.
The replicas' limits (`resources`: 2 CPUs and 2 GiB by default) set their capacity: the chart
passes them as `SYSTEM_TASK_ABS_CPU` and `SYSTEM_TASK_ABS_MEM`, so jobs that run on the
replicas don't take more forks than the limits hold. To run more jobs on each replica, raise
the limits.

#### With Argo CD or Flux

They render the chart with `helm template`, where the lookup that keeps the generated secrets
finds nothing, so each sync would generate a new master key. Give the chart its secrets
instead (from External Secrets, Sealed Secrets...), with an external PostgreSQL, and turn off
the migration Job: Argo CD runs Helm's pre-upgrade hook before every sync, the first one
included, when the database does not exist yet. The replicas apply the migrations as they
start.

```yaml
admin:
  existingSecret: awxnext-secrets     # key "password"
secretKey:
  existingSecret: awxnext-secrets     # key "secret-key"
postgresql:
  bundled: false
  external:
    existingSecret: awxnext-db-app    # a CloudNativePG cluster's secret
    key: uri
migrations:
  enabled: false
```

The images of `main` are published as `ghcr.io/damhau/awxnext-all-in-one:main-<commit time>-<commit>`
(UTC, `YYYYMMDDHHMMSS`), so they sort by date: Argo CD Image Updater can follow them with the
`alphabetical` strategy and `allowTags: regexp:^main-[0-9]{14}-[0-9a-f]{7}$`. When it, or
kustomize, changes the image, `AWXNEXT_SHIM_IMAGE` keeps the chart's: copy the image into it
with a kustomize replacement, so container group job pods get the server's own version.
Pin the chart itself at one of `main`'s versions, as kustomize's `helmCharts` take it
(`repo: oci://ghcr.io/damhau/charts`, `version: 0.1.0-main.<commit time>`), and move it
when the chart changes: the Image Updater moves only the image.

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
3. Check the version with `awxnext version` on each node, or in the UI's About dialog (`awxnext` in
   `GET /api/v2/config/`), and `awxnext list-instances` (every node back). `GET /api/v2/ping/`
   reports the AWX API version awxnext answers to, 24.6.1, whatever awxnext's own.

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

Job history and instances (nodes) are not imported. Import into a new awxnext: users and execution
environments that exist already there are kept, with a warning, and organizations of the same
name merged. AWX's managed credentials and execution environment map onto awxnext's own.

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
   AWX keeps running: the import only reads. A running awxnext sees the imported settings within
   5 seconds.
4. **After the import:**
   - sync the projects (Projects, **Sync**);
   - register execution nodes again (Instances, **Add node**, then the agent);
   - for container groups, check their credential or the service account;
   - for secret lookups that log in as the server itself (Vault's `kubernetes_role`, Azure Key
     Vault without a client secret), list their hosts in
     `CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS` ([Secret lookups](#secret-lookups));
   - point users, webhooks and callbacks at awxnext's URL.

The `awx` CLI, the `awx.awx` collection and the Terraform provider work against awxnext
unchanged ([parity](parity.md)).

### What differs after an import

Through `/api/v2`, the imported objects match AWX's, with these exceptions:

- **What awxnext's install created stays.** `admin` keeps its account. `AWX EE (latest)` keeps
  the image of `AWXNEXT_DEFAULT_EE_IMAGE`, and `default` stays awxnext's instance group, however
  it was set up in AWX.
- **Notification types.** Only email and webhook templates come along; the report lists the
  others, and their links are lost.
- **Computed values.** The callback and metadata URLs of the login settings come from awxnext's
  URL, and a project's `local_path` from its id, which changes when awxnext already uses it.
  awxnext counts the hosts and sources of an inventory, where AWX shows a counter it hasn't
  refreshed.
- **Empty values.** A workflow's unset `job_tags`, `skip_tags` and `scm_branch`, and an
  inventory's unset `host_filter`, read `""` where AWX has null.
- **Managed credential types.** AWS, Azure Resource Manager, Google Compute Engine, OpenStack,
  Satellite, Terraform and vCenter show their injectors, which AWX builds in code and shows as
  `{}`. An Azure Key Vault lookup needs no client, secret or tenant: without them, it uses
  awxnext's own Azure identity.

### How the importer is tested

`.github/workflows/import-awx.yml` runs by hand and every night from `full.yml`. It installs
AWX 24.6.1 and awxnext in kind, and `test/awximport/awximport.py` fills AWX through its API:
users, teams and role grants; credentials of several types, one a secret lookup; login settings;
regular, smart and constructed inventories; a project; job templates with a survey; a workflow
with an approval node; schedules and notification templates. It then checks that the dry run
changes nothing, imports, and compares the two servers through `/api/v2`: names, fields,
memberships, roles, surveys, workflow graphs, settings, and logins with the imported passwords.
Finally, an imported job template must succeed, with a playbook that checks the secrets it gets
from its imported credentials.

A difference or a warning fails the run unless `test/awximport/known-differences.yml` lists it
with its reason, and so does an entry that no longer matches anything. `test/importer_test.go`
is the fast test: the same importer on a copy of the AWX tables it reads.

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

A control node sends a heartbeat every 15 seconds. When one has been silent for 2 minutes, the
dispatching node fails the jobs it was running and marks it lost, as AWX does: **unavailable**,
with no capacity, so its groups' capacity drops. Its next heartbeat makes it ready again. With
`AWXNEXT_AUTO_DEPROVISION_INSTANCES=true` a lost node is deleted instead, once its jobs have
failed, and the group policies apply again. A node deleted that way registers again if it
comes back. The Helm chart turns this on (`autoDeprovisionInstances`), because each rollout's
pods have new names. A node provisioned with `awxnext provision-instance` that has not started
yet is never lost.

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

## Secret lookups

Credentials of an external type (HashiCorp Vault, Azure Key Vault, CyberArk, Conjur, Centrify,
Thycotic, AWS Secrets Manager) fetch secrets from the address the credential gives, when a job
starts and when someone tests them. Anyone who may create a credential chooses that address, so
two things are refused unless a superuser allows them, on the Jobs settings page under "Secret
lookups" (roadmap M9-06):

- **awxnext's own identity.** Vault's Kubernetes login sends the pod's service account token,
  and Azure Key Vault without a client secret uses the server's workload identity
  (`AZURE_FEDERATED_TOKEN_FILE`) or managed identity. AWX sends them to any URL; awxnext only to
  the hosts in `CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS`, one per entry: `vault.example.com`,
  `vault.example.com:8200`, `*.vault.azure.net`, or `http://vault.vault.svc:8200` for plain
  HTTP (an entry without a scheme matches https only, one without a port any port). The workload
  identity's token goes to the cloud's own authority, or to an `authority_host` that is listed.
  Once a host is listed, anyone who may create a credential can read there whatever that
  identity can read: list only the vaults meant for it, and give the identity only what lookups
  need.
- **The server's own network.** Lookups refuse loopback, link-local and unspecified addresses
  and cloud metadata services, checked on the address they connect to.
  `CREDENTIAL_LOOKUP_ALLOWED_LOCAL_ADDRESSES` allows an address (`127.0.0.1`), a network
  (`127.0.0.0/8`) or an address and port (`127.0.0.1:8200`, for example a Vault agent beside
  awxnext, or a proxy on loopback). The metadata endpoint Azure's managed identity uses needs no
  entry. Through a proxy (`HTTPS_PROXY`), awxnext connects to the proxy, so only addresses
  written in a URL are checked: the proxy should refuse the same ones.

A refused lookup sends nothing, and says which setting would allow it. Lookups with a secret of
the credential's own (a Vault token, username and password, AppRole or certificate, an Azure
client secret) need no listing. The master key's Vault or Key Vault (`AWXNEXT_KMS_*`), which the
operator configures, is not affected.

Testing a credential type with values not saved yet (`POST /api/v2/credential_types/N/test/`) is
for superusers, as in AWX; anyone who may use a credential tests it at `credentials/N/test/`.

## Secrets in job output

A task that prints a secret, such as a `debug`, `-vvv`, or a failing `uri` or `command` that
echoes its arguments, would show it to anyone who may read the job. awxnext replaces each secret
it hands a run with `$encrypted$` before the output is stored, published on websockets, sent to
the log aggregator or archived (roadmap M12-02). AWX hides only the SCM password in project
updates.

What awxnext masks:

- **Values:** the fields a credential type marks secret, in every credential the run uses,
  including values a secret lookup fetched; survey password answers, generated ones included
  ([computed questions](surveys.md#computed-questions-value_from)); the passwords prompted at
  launch (SSH, privilege escalation, vault); and a template's secret outputs, from the
  `set_stats` task that publishes them on ([secret outputs](workflows.md#secret-outputs)). The credentials a run uses on its own are
  masked too: an inventory source's credential, a project's SCM credential, the execution
  environment's registry credential and the organization's Galaxy credentials.
- **Runs:** jobs, ad hoc commands, project updates and inventory updates.
- **Fields:** each event's stdout, its task and play names, and its event data, keys and values.
- **Forms:** the value as given; JSON-escaped, as Go writes it and as Python's `json.dumps` does
  by default (`\u00fc` for `ü`); each of those escaped a second time, as a `to_json` inside a
  `debug` message is; and each line of a multi-line value, such as a private key.

What it doesn't mask:

- **Values under 4 characters,** such as a PIN, which would hide ordinary text.
- **Transformed values:** base64, a hash, part of a value, or a value written with other
  escaping, such as a YAML string or a Python `repr`. Set `no_log: true` on tasks that print
  them.
- **What the run keeps rather than prints:** the job's artifacts (`set_stats`), which a workflow
  passes to its next nodes, and the facts it caches keep their values, except secret outputs,
  which are kept encrypted. Host names aren't masked.
- **Values awxnext didn't hand the run:** fields that aren't secret, such as a username, and
  secrets that the playbook reads itself, from a file or a lookup plugin.

Masking costs one pass over each event, whatever the number of secrets; see
[Benchmarks](benchmarks.md#secrets-in-job-output).

## rvav sources

[rvav](https://github.com/damhau/rvav), a self-service VM portal, lists the VMs it manages for
AWX. On AWX, a source from a project runs rvav's inventory plugin (the `rvav.platform`
collection) in an execution environment. awxnext has a source type of its own, `rvav`, and
reads them itself (roadmap M4-11):

1. In the inventory, add a source of the type rvav. It takes no source variables or limit;
   overwrite, the host filter, the enabled variable, update on launch and schedules work as for
   other sources.
2. Give it a credential of the type rvav: rvav's address as awxnext reaches it
   (`https://rvav.example.com`, or `http://rvav-api.rvav.svc:8080` in the same cluster) and a
   token of inventory scope. Verify SSL is on by default, against the system's CAs and the
   credential's Certificate Authority data.

rvav can do the second step: put the source's ID in its Administration, AWX, Inventory source,
and click Inventory token. rvav makes the token, sets it in the source's credential (making one in
the inventory's organization when the source has none) and syncs the source. Its `awx-bootstrap`
command makes an rvav source when the AWX it talks to offers the type, and a project source
otherwise.

A sync is one `GET <url>/api/v1/ansible/inventory` with the token, from awxnext's own server
rather than an execution node, and imports the answer (`ansible-inventory --list`'s format, at
most 64 MiB). It follows no redirects. Like secret lookups, it refuses loopback, link-local and
unspecified addresses and cloud metadata services, unless listed in
`INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES` (Jobs settings, "rvav sources"): for example
`127.0.0.1:8080` for an rvav on the same host. A failed sync gives the HTTP status, not rvav's
answer, and says what to do when the source has no credential or rvav refuses the token.

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

## Event streams

An event stream takes events that outside systems post: Alertmanager or Grafana alerts, GitHub
or GitLab webhooks, ServiceNow records, CloudEvents, or any JSON. It stores each one as a
CloudEvent in the event log, where rules read them (roadmap M14). To create one, post to
`/api/v2/event_streams/` with these fields:

| Field | What it sets |
|---|---|
| `organization`, `name` | The stream's organization, and a name unique in it. Organization admins manage the organization's streams; the roles `EventStream Admin` and `EventStream Read` give one stream. |
| `stream_type` | What the sender posts: `cloudevents`, `generic` (any JSON), `alertmanager`, `grafana`, `github`, `gitlab` or `servicenow`. |
| `credential` | How the sender authenticates: a credential of one of the event stream types below, which you may use. |
| `retention_days` | How long the log keeps the stream's events, 7 by default. |
| `max_payload_bytes` | The largest request the stream takes, 1 MiB by default, at most 25 MiB. |
| `rate_limit` | Events per minute, on each node, 6000 by default; 0 for no limit. A minute's events may arrive at once. |
| `test_mode` | Keep and show the events, but hide them from rules. |
| `mapping` | For `generic` streams, JSONPaths to the event's `id`, `type`, `source`, `subject` and `time` in the body, such as `{"id": "$.alert.id"}`. |
| `additional_data_headers` | Request headers each event keeps, separated by commas, such as `X-Request-Id`. Each becomes an attribute of the event, named in lowercase letters and digits (`event.xrequestid` in a rule). `Authorization`, `Proxy-Authorization` and `Cookie` are never kept. In the UI, it's the form's **Request headers** section. |

The answer's `receiver_url` is where the sender posts: `<TOWER_URL_BASE>/api/v2/event_streams/<uuid>/post/`.
The UUID is not a secret, the credential is. The stream's events are at
`/api/v2/event_streams/N/events/`, and every stream you can see at `/api/v2/stream_events/`.

### Senders' credentials

The receiver never signs in a user: it checks the stream's credential only. Its secrets are
encrypted as every credential's and can come from a secret lookup. Neither they nor the headers
that carry them are stored with the events.

| Credential type | The sender sends |
|---|---|
| HMAC Event Stream | A signature of the body with the secret, in `http_header_key` (`X-Hub-Signature-256` by default). `hash_algorithm` is `sha256` (default), `sha512` or `sha1`; `signature_encoding` `hex` (default) or `base64`; `signature_prefix` is what comes before it (`sha256=` by default). |
| GitHub Event Stream | GitHub's `X-Hub-Signature-256` with the webhook's secret. |
| GitLab Event Stream | GitLab's `X-Gitlab-Token`. |
| Token Event Stream | `Authorization: Bearer <token>`, or the token alone in `http_header_key`. |
| Basic Event Stream | `Authorization: Basic` with the username and password. |
| mTLS Event Stream | A client certificate the credential's CA signs, with `subject` (optional) as its common name, a SAN or its whole DN. |
| OIDC Event Stream | `Authorization: Bearer <token>`: a JWT the `issuer` signs, for the `audience`, whose claims meet `claims` (optional). |

awxnext does not end TLS itself, so a proxy in front of it checks the client certificate and
passes it on in a header. awxnext reads that header only from the addresses in
`EVENT_STREAM_TRUSTED_PROXIES` (Settings, System, "Event streams"): without one, mTLS streams
refuse every request. `EVENT_STREAM_CLIENT_CERT_HEADER` names the header,
`X-Forwarded-Client-Cert` by default. It takes Envoy's and Istio's `X-Forwarded-Client-Cert`
(the last element, the one the closest proxy added), URL-encoded PEM (ingress-nginx's
`$ssl_client_escaped_cert`, as `Ssl-Client-Cert`), or base64 DER (Traefik). Make the proxy
replace the header a client sends, not add to it: a client at a listed address can pass any
certificate it likes.

`claims` is a [CEL](https://cel.dev) condition on the token's claims, such as
`claims.repository == "org/app" && claims.ref == "refs/heads/main"` for GitHub Actions' tokens
(`issuer` `https://token.actions.githubusercontent.com`). A claim it names that the token lacks
makes it false. awxnext fetches the issuer's discovery document and keys through the same
checks as secret lookups: an issuer on loopback, a link-local address or a cloud metadata
service needs an entry in `EVENT_STREAM_ALLOWED_LOCAL_ADDRESSES`.

### What a stream answers

| Status | When |
|---|---|
| `202` | The events are stored. The body lists each one's ID, source and `sequence` in the stream, and whether it was a `duplicate`. |
| `400` | The body is not what the stream's type takes, or an event has no ID, source or type. |
| `401`, `403` | The credentials are wrong or missing (`401`), or an OIDC token's claims don't meet the condition (`403`). |
| `404` | No stream has this UUID. |
| `413` | The request is larger than `max_payload_bytes`, or has more events than `rate_limit`. |
| `415` | The content type is not one the stream's type takes. |
| `429` | The stream's rate limit is reached; `Retry-After` says when to try again. |
| `503` | The database or an OIDC issuer cannot be reached. Nothing is stored: send again. |

A sender that gets anything but `202` can send the same request again. An event whose ID the
stream already has, from the same source, is dropped and answered as a duplicate, for as long as
the stream keeps its events. Where each event's ID comes from:

- **CloudEvents**: its `id`, in binary mode (`Ce-*` headers), structured mode
  (`application/cloudevents+json`) or batch mode (`application/cloudevents-batch+json`).
  `traceparent` and `tracestate` are kept as extensions.
- **Alertmanager and Grafana**: one event per alert of the group, with the ID
  `<fingerprint>/<startsAt>/<status>` and the type `alert.firing` or `alert.resolved`. A group
  sent again is dropped; an alert that resolves is a new event.
- **GitHub**: `X-GitHub-Delivery`, with the type `github.<X-GitHub-Event>`.
- **GitLab**: `Idempotency-Key`, else `X-Gitlab-Event-UUID`, with the type
  `gitlab.<event>`, such as `gitlab.merge_request`.
- **ServiceNow**: `<sys_id>/<sys_mod_count>`, else `<sys_id>/<sys_updated_on>`, one event per
  record of an object, an array or a `result`.
- **Generic**: `Idempotency-Key`, `X-Request-Id` or `X-Delivery-Id`, else the mapping's `id`,
  else a new ID: without one, every request is a new event. The type is `X-Event-Type`, else the
  mapping's, else `generic`.

### Alertmanager

Create a stream of the type `alertmanager` with a Token Event Stream credential, and point a
receiver at its `receiver_url`:

```yaml
receivers:
  - name: awxnext
    webhook_configs:
      - url: https://awxnext.example.com/api/v2/event_streams/<uuid>/post/
        send_resolved: true
        max_alerts: 0
        http_config:
          authorization:
            type: Bearer
            credentials_file: /etc/alertmanager/awxnext-token
```

Alertmanager sends the group again at each `repeat_interval`; awxnext keeps each alert once per
start and status.

### GitHub

Create a stream of the type `github` with a GitHub Event Stream credential whose secret you
choose. In the repository's or organization's Settings, Webhooks, add a webhook:

1. **Payload URL**: the stream's `receiver_url`.
2. **Content type**: `application/json` (`application/x-www-form-urlencoded` works too).
3. **Secret**: the credential's secret.
4. Choose the events to send.

GitHub's redelivery of a delivery has the same `X-GitHub-Delivery`, so it is a duplicate.

### Retention and test mode

The event log is partitioned by day, like job events. The management job
**Cleanup Event Streams** (`cleanup_event_streams`, daily by default) drops the days older than
every stream's retention, then deletes each stream's events older than its own, and the events
of deleted streams. It cleans up the rules' [decisions](#decisions) too. With `dry_run` it says
what it would delete.

In test mode a stream's events are logged and shown with `test_mode: true`, but rules don't see
them, then or after the stream leaves test mode. The rules read each stream's log from a
position of their own.

### Streams in the UI

**Events, Event streams** in the navigation lists the streams you can see. A stream's page has
these tabs:

- **Events**: the stream's latest events, newest first, refreshed every 3 seconds while **Live**
  is on. Open an event to see its data. **Open the event and its decisions** shows the event's
  page, with what each rule decided on it.
- **Setup**: the receiver URL, the credential and the stream's limits, and how to point a sender
  at it: an Alertmanager receiver, a GitHub or GitLab webhook, or `curl` for a token or basic
  credential. **Turn on test mode** and **Leave test mode** switch test mode.
- **Access**: the stream's roles.

A stream created on the page starts in test mode, so that you can set up its sender and
[write rules against its events](#writing-a-rule-against-real-events) before any rule acts on
them.

## Event connectors

An event connector reads events from a system that doesn't post them, such as a Kafka topic, and
posts them to an event stream (roadmap M14-06). It runs an
[`ansible.eda`](https://github.com/ansible/event-driven-ansible) source plugin, or any plugin
with the same interface (`async def main(queue, args)`), without `ansible-rulebook` or Java. A
connector runs where jobs run, in an image that has the plugin's collection and the Python
packages it imports, such as a decision environment.

To create a connector, post to `/api/v2/event_connectors/` with these fields:

| Field | What it sets |
|---|---|
| `organization`, `name` | The connector's organization, and a name unique in it. Organization admins manage the organization's connectors; the roles `EventConnector Admin` and `EventConnector Read` give one connector. |
| `event_stream` | The stream of the organization that the connector posts to. Setting it takes the right to change the stream (`EventStream Admin`): the connector posts with a token of its own, not the stream's credential. |
| `source` | The plugin, by its full name, such as `ansible.eda.kafka`. `eda.builtin.` names are looked up in `ansible.eda`. |
| `args` | The plugin's arguments, as a rulebook's source gives them. |
| `filters` | Event filters applied in order, as a rulebook's: `[{"ansible.eda.json_filter": {"exclude_keys": ["password"]}}]`. |
| `credentials` | The IDs of credentials whose injectors configure the plugin ([Credentials](#a-connectors-credentials)). Adding one takes the right to use it. |
| `execution_environment` | The image the connector runs in, such as a decision environment. Empty: the default execution environment. |
| `instance_group` | Where the connector runs: an instance group of execution or hybrid nodes, or a container group. Empty: the `default` group. Setting it takes the right to use the group. |
| `python` | The Python interpreter. Empty: the interpreter of `ansible-rulebook` if the image has it, else `python3`. |
| `enabled` | Whether the connector runs. `false` stops it. |
| `restart_policy`, `restart_delay`, `max_restarts` | When it starts again after it stops ([Restarts](#restarts)). |
| `buffer_size` | The events it holds while awxnext is unreachable, 1000 by default. |

A connector's events have its URL as their `source` (`/api/v2/event_connectors/N/`), the
plugin's name as their `type`, and the plugin's event, with its `meta`, as their `data`. A rule
selects them with `event.type == "ansible.eda.kafka"`, for example.

### Where a connector runs

The leader places each enabled connector on one node at a time, the node of its group that runs
the fewest connectors:

- **An execution node:** a control node starts it through the node's agent, in the agent's
  runtime: podman or docker in the connector's image, or `none`, the node's own Python.
- **A hybrid node:** the node runs it itself, in `AWXNEXT_CONTAINER_RUNTIME`, as its jobs. With
  `none`, the Helm chart's setting, the node's Python must find the collection
  (`ANSIBLE_COLLECTIONS_PATH`): on Kubernetes, put connectors in a container group instead.
- **A container group:** a control node starts a pod, `event-connector-<id>-...`, labelled
  `awxnext-event-connector-id`, from the group's pod spec with the connector's image.

awxnext sends the runner, a Python script that uses only the standard library, each time the
connector starts, as it sends its job helper to pods: you install nothing but the collection.
The runner needs Python 3.9 or later. It posts to awxnext at:

- On an execution node, the URL its agent connects to.
- On a hybrid node with `none`, `AWXNEXT_INTERNAL_URL`, else the node's own `AWXNEXT_LISTEN`
  address.
- In a container on a hybrid node, or in a pod, `AWXNEXT_SERVICE_URL`, else `AWXNEXT_BASE_URL`.

Each placement has its own token. When the connector moves, stops, is turned off or changed,
awxnext revokes the token: a runner left on a node that awxnext lost gets `401` from the stream
and exits, so that two copies never post at once. When the execution node of a connector stops
being ready, or its control node sends no heartbeat for 2 minutes, the leader moves the
connector to another node, whatever its restart policy. A control node that stops hands its
connectors back as it shuts down: the leader places them on another node at once, and passes
over the stopped node until it sends a heartbeat again.

### Restarts

| `restart_policy` | The connector starts again when |
|---|---|
| `on-failure` (default) | Its plugin fails, or its runner exits with an error. |
| `always` | It stops for any reason, the plugin's end included. |
| `never` | Never: it stays `completed` or `failed`. |

The first restart waits `restart_delay` seconds, 5 by default; each restart in a row doubles the
wait, up to 64 times the delay. After `max_restarts` restarts in a row (0 for no limit), the
connector stays `failed`. A run of 10 minutes resets the count. To start a stopped connector
again, post to `/api/v2/event_connectors/N/restart/`. A change to a running connector's
configuration restarts it with the new configuration.

### Buffering through an outage

The plugin's events go to a queue of `buffer_size` events. The runner posts them to the stream
in batches of up to 100, as a CloudEvents batch, and takes them off the queue once awxnext has
answered `202`. While awxnext is unreachable or answers `429` or `5xx`, the runner keeps the
events and posts them again, after `Retry-After` or a backoff from 1 to 30 seconds. When the
queue is full, the plugin waits: the Kafka source stops reading, and its consumer group's
offset keeps the rest of the topic. Each event's ID is its `meta.uuid`, which the Kafka source
derives from the message's topic, partition and offset, so an event posted twice is stored once.
The stream drops events it refuses (`400`, `413`) one at a time; `stats.rejected` counts them.

### A connector's credentials

A connector takes credentials of any type with injectors, built-in or custom:

- `env` injectors set the plugin's environment, inside the runner: never on a command line.
- `extra_vars` injectors become the plugin's arguments, unless `args` sets them.
- `file` injectors write files that only the runner's user can read. Name them with
  `{{ eda.filename }}` (or `{{ tower.filename }}`), as EDA's controller does.

The runner never logs its arguments, its environment or its token.

### Kafka example

To read a Kafka topic over SASL and TLS into a stream:

1. Create a credential type for the broker's login:

   ```sh
   awx credential_types create --name "Kafka SASL" --kind cloud \
     --inputs '{"fields": [{"id": "username", "label": "Username"},
       {"id": "password", "label": "Password", "secret": true},
       {"id": "ca", "label": "CA certificate", "multiline": true}]}' \
     --injectors '{"extra_vars": {"sasl_plain_username": "{{ username }}",
       "sasl_plain_password": "{{ password }}", "cafile": "{{ eda.filename }}"},
       "file": {"template": "{{ ca }}"}}'
   ```

2. Create a credential of that type with the login and the CA certificate.
3. Create an execution environment for an image with `ansible.eda` and the packages its Kafka
   source imports: `aiokafka`, `aiohttp`, `fastavro` and `PyYAML`. The decision environment
   `quay.io/ansible/ansible-rulebook:v1.3.2` lacks `fastavro`; to add it, build on it:

   ```dockerfile
   FROM quay.io/ansible/ansible-rulebook:v1.3.2
   RUN python3.12 -m pip install --user fastavro==1.13.1
   ```

4. Create the connector:

   ```sh
   curl -u admin -H 'Content-Type: application/json' https://awxnext.example.com/api/v2/event_connectors/ -d '{
     "name": "orders", "organization": 1, "event_stream": 4,
     "source": "ansible.eda.kafka",
     "args": {"host": "kafka.example.com", "port": 9093, "topic": "orders", "group_id": "awxnext",
              "offset": "earliest", "security_protocol": "SASL_SSL", "sasl_mechanism": "SCRAM-SHA-512"},
     "filters": [{"ansible.eda.json_filter": {"exclude_keys": ["card_number"]}}],
     "credentials": [12], "execution_environment": 3, "instance_group": 2}'
   ```

5. Check `status` on `/api/v2/event_connectors/N/`: `starting`, then `running`. Its events are at
   `/api/v2/event_streams/4/events/`.

### Status and log

| `status` | Meaning |
|---|---|
| `pending` | Waiting for a node; `status_explanation` says why, such as no node of its group being up. |
| `starting`, `running` | Placed on `execution_node`, watched by `controller_node`. |
| `restarting` | Stopped; starts again at `next_start`. |
| `stopped` | Turned off. |
| `completed`, `failed` | Ended and not restarted; `status_explanation` says why. |

`stats` is what the runner last reported, every 5 seconds: events `sent`, `buffered`,
`dropped` and `rejected`, `retries`, and whether awxnext is `unreachable`. Each run, the last 20
of each connector, is at `/api/v2/event_connectors/N/runs/`, with its node, exit code (`1`: the
plugin failed; `2`: the runner couldn't start it, such as a plugin not found; `3`: awxnext
refused its token), its explanation and the last 64 KiB of its log. A connector has version
history, `used_by`, and GitOps documents of kind `EventConnector` ([GitOps](gitops.md)).

## Event rules

An event rule reads the events of one or more streams, and acts on the events its filter holds
for: it launches a job or workflow template, sends a notification, posts an event to another
stream, or records a message in its decision log. To create one, post to
`/api/v2/event_rules/` with these fields, or import a rulebook of ansible-rulebook (see
[Importing rulebooks](#importing-rulebooks)):

| Field | What it sets |
|---|---|
| `organization`, `name` | The rule's organization, and a name unique in it. Organization admins manage the organization's rules; the roles `EventRule Admin`, `EventRule Automate`, `EventRule Approve` and `EventRule Read` give one rule (see [Modes](#modes)). |
| `streams` | The IDs of the streams the rule reads, at most 20. |
| `filter` | A [CEL](https://cel.dev) condition on `event`, such as `event.type == "alert.firing"`. |
| `key` | A CEL expression that gives a string, such as `event.data.labels.instance`. The throttle, the pattern and the circuit breaker count events by key. Empty: one key for all events. |
| `throttle`, `throttle_window` | `once_within` or `once_after`, and the window in seconds (at most 7 days). Empty: the rule acts on every event. A rule with a pattern takes no throttle. |
| `pattern`, `pattern_window` | `count`, `all`, `sequence` or `absence`, and its window in seconds (at most 7 days): see [Patterns](#patterns). Empty: no pattern. |
| `conditions` | For `all`, `sequence` and `absence`: CEL conditions on `event`, 2 to 10, exactly 2 for `absence`. |
| `pattern_count` | For `count`: how many events, 2 to 1,000. |
| `action` | `launch` (the default), `notify`, `post_event` or `log`. |
| `unified_job_template`, `extra_vars`, `limit` | For `launch`: the job or workflow template; each variable's name and the CEL expression of its value; a CEL expression that gives the limit. |
| `retries`, `retry_delay` | For `launch`: how many times the rule launches a failed job again, at most 10, and how many seconds after the job ended, at most 3,600. |
| `post_artifacts`, `artifacts_stream` | For `launch`: `event` or `fact` posts the artifacts of each job (its `set_stats`) once the job ends, to `artifacts_stream` (the rule's first stream by default). Empty: the rule posts nothing. |
| `notification_template`, `message` | For `notify`: the notification template, and a CEL expression that gives the message. For `log`: a CEL expression that gives the message the decision records. |
| `target_stream`, `event_type`, `event_data` | For `post_event`: the stream to post to, which the rule must not read, and CEL expressions that give the event's type and its data (the event's own data by default). |
| `mode` | `shadow` (the default), `approval` or `auto`: see [Modes](#modes). |
| `max_concurrent_runs`, `host_lock`, `max_group_share` | For `launch`: the [blast radius](#blast-radius). |
| `breaker_firings`, `breaker_window`, `breaker_notification_template` | The [circuit breaker](#circuit-breaker). |
| `window_action`, `approval_timeout` | What a [maintenance window](#maintenance-windows-and-change-freezes) does to an action: `hold` (the default) or `approval`; and how long an approval waits, in seconds (one hour by default, `0` for ever). |
| `enabled` | `false` stops the rule. |

The answer's `owner`, `stopped_reason` and `last_fired` are read-only, and so is `rulebook`: the
rulebook whose import made the rule, if one did.

### Expressions

Every expression sees `event`, the event as a CloudEvent's JSON format has it: `id`, `source`,
`type`, `subject`, `time`, `data` and the extensions, such as `traceparent`. An action that
`once_after` or a [pattern](#patterns) fires for several events also sees `events`, all of them
in the order they were received; `event` is the first.

When you save a rule, awxnext compiles each expression and checks its types against the schema
of each stream's 100 latest events, test mode included. A field that the events don't have is
dynamic and passes the check. A whole number is an `int`, a number with a fraction a `double`,
and Alertmanager and Grafana streams know their alerts' fields (`labels` and `annotations` are
maps of strings). A rule with an error is refused with `400`, the error under its field. For
example, `event.data.labels.severity + 1 > 2` is refused, because `severity` is a string.
`GET /api/v2/event_streams/N/schema/` shows the schema that a stream's rules are checked
against.

An expression that fails on an event, such as one that reads a field the event doesn't have,
doesn't match it. Each evaluation is limited in cost and to one second.

### Writing a rule against real events

**Events, Event rules** in the navigation lists the rules you can see. **New rule** opens the
editor; on a stream's page or an event's, **New rule** and **Write a rule** open it with the
stream chosen. To write a rule against a stream's events:

1. Send the stream some events, in test mode if no rule must act on them yet.
1. In the editor, choose the streams. Completion and type checks come from their latest events,
   test mode's included: type a dot after `event` or one of its fields for the fields the events
   have, with their types, or press **Ctrl+Space**.
1. Write the filter. Each type error shows under its expression as you type, with its line and
   column; select the position to move the cursor there.
1. Read the pane **On the latest events**: it runs the draft on the streams' 20 latest events
   and shows which match, with their key and what the action would get. For an event that
   doesn't match, it marks the part of the filter that failed and shows its values.
1. Set the key, the throttle or the [pattern](#patterns) and its conditions, the action and its
   variables, the mode and the guardrails. Each event in the pane shows the conditions it
   matches. Under a launch's variables, the preview shows what the template would get from the
   latest matching event.
1. Optional: to see what the draft would have done over a longer period, select **Backtest the
   draft…** (see [Backtests](#backtests)).
1. Save. A new rule starts in `shadow` mode: see
   [Moving a rule from shadow to auto](#moving-a-rule-from-shadow-to-auto).

A rule's page shows its settings, its patterns' open windows, its circuit breakers, which you can
reset there, and its recent decisions. Its **Decisions** tab lists every [decision](#decisions), and its
**Backtest** tab runs a [backtest](#backtests) of the saved rule.

The editor checks a draft with `POST /api/v2/event_rules/check/`. It takes the rule's
`streams`, `action`, `filter`, `key`, `pattern`, `conditions`, `extra_vars`, `limit`, `message`,
`event_type` and `event_data`, and `events`, the number of latest events to run the draft on: 20
by default, at most 100. It saves nothing and acts on nothing; throttles, patterns' windows and
guardrails are a backtest's. The answer has:

| Field | What it holds |
|---|---|
| `errors` | Each expression's errors, by field: `filter`, `key`, `limit`, `message`, `event_type`, `event_data`, `extra_vars.<name>` for a variable, and `conditions.<i>` for a condition, from 0. |
| `schema_events` | How many events the schema was built from. |
| `checked`, `matched` | How many events the draft ran on, and how many it matched. |
| `truncated` | `true` when the check stopped at its 10-second limit. |
| `events` | Each event checked, newest first: its `stream`, `sequence`, `event_id`, `event_type`, `received` and `test_mode`; whether it `matched`; for a miss, the `reason` and the failed part in `filter`, as a [decision](#decisions) has them; with conditions, the `conditions` it matches, from 0, and an event that matches none doesn't match; for a match, the `key` and the `action`'s values, or `key_error` and `action_errors`. |

With a filter that doesn't compile, the check runs nothing and answers the errors alone. To
check a draft, you need to see its streams; otherwise awxnext refuses with `403`.

### Throttles

- **`once_within`** acts on a key's first event, then holds back the key's events until
  `throttle_window` seconds after that one.
- **`once_after`** opens a window on the first event. When the window ends, the rule acts
  once, for the first event of each key that the window collected.

Both follow ansible-rulebook's `throttle`. The window counts from the time each event was
received.

### Patterns

A pattern makes a rule wait, for each key, for several events within `pattern_window` seconds,
and then act once for all of them: the action sees the first as `event`, and all of them, in the
order they were received, as `events`. An event must pass the filter first; `conditions` then
say which part of the pattern it is. In these examples, the events carry their host in
`data.host`, or are Alertmanager alerts:

- **`count`** fires when a key has `pattern_count` events within the window. The window
  slides: an event leaves it `pattern_window` seconds after it was received, and its decision
  becomes `expired`. After firing, the key counts again from zero. Restart a host after 3 OOM
  kills within 10 minutes:

  ```json
  {"filter": "event.type == \"oom.kill\"", "key": "event.data.host",
   "pattern": "count", "pattern_count": 3, "pattern_window": 600}
  ```

- **`all`** fires when the key's window holds an event of each condition, in any order. An
  event can match several conditions. Act when both the disk and the CPU of a host alert within
  5 minutes:

  ```json
  {"filter": "event.type.startsWith(\"alert.\")", "key": "event.data.host", "pattern": "all",
   "conditions": ["event.type == \"alert.disk\"", "event.type == \"alert.cpu\""], "pattern_window": 300}
  ```

- **`sequence`** fires when events of each condition come in order, the last within the window
  of the first. An event that matches a step whose step before it hasn't come is `ignored`. Act
  when a deploy is followed by an error spike and then by a health check failure, within 15
  minutes:

  ```json
  {"filter": "true", "key": "event.data.host", "pattern": "sequence", "pattern_window": 900,
   "conditions": ["event.type == \"deploy.done\"", "event.type == \"errors.spike\"", "event.type == \"health.failed\""]}
  ```

- **`absence`** waits after an event of the first condition for an event of the second. If none
  comes within the window, the rule fires when the window ends, from a timer, with the events
  of the first condition that came in the meantime. An event of the second condition clears
  the window: its decision and the waiting ones are `cleared`, and the rule doesn't fire. An
  event of the second condition with nothing waiting is `ignored`. Escalate an alert that isn't
  resolved within 15 minutes:

  ```json
  {"filter": "event.data.labels.alertname == \"ServiceDown\"", "key": "event.data.labels.instance",
   "pattern": "absence", "pattern_window": 900,
   "conditions": ["event.type == \"alert.firing\"", "event.type == \"alert.resolved\""]}
  ```

An event that passes the filter and matches none of the conditions is `not_matched`, with each
condition's failed part in `details.conditions`. An event that joins a window is `waiting`, and
its reason says how far the window got, such as `2 of 3 events within the window: waiting for 1
more before 2026-11-16T08:10:00Z.` When the window fires, its events' decisions get the
action's outcome. Only one of them counts toward the [circuit breaker](#circuit-breaker): a
window that fires is one firing. A key's window holds 1,000 events at most; past that, an event
is `deduplicated`.

Windows count by the time awxnext received each event, not by the event's `time`, which a
sender sets and can get wrong; the filter and the conditions can still read `event.time`. An
event is late when it was received more than `pattern_window` seconds before the newest event of
its key that the rule decided: this happens when the rule reads several streams and one of them
is decided behind the others. A late event is `expired` at once, and counts for nothing.

The windows are rows in PostgreSQL, locked for each event and committed with the decisions, so
a restart or a failover keeps them, and two streams that send the same key to a rule share its
window. Each node's engine checks the windows that end every second, and fires or expires them,
but only once the rules have decided every event of the rule's streams received before the end,
so a backlog decides before the timer. In `shadow` mode, the rule keeps its windows apart from
the ones of the other modes, and its decisions are `shadow`; the guardrails, the mode and
`window_action` apply when a window fires, as for any action. A change of mode, of the pattern,
its conditions or its window, or of the key, drops the open windows, as does disabling the rule:
their decisions are `dropped`.

`GET /api/v2/event_rules/N/patterns/` lists the rule's open windows, the next to end first, and
`/api/v2/event_rule_patterns/` those of every rule you can see. Each has its `key`, whether it
is the `shadow` mode's, when it `opened`, its `clock` (the latest event it saw), its `deadline`
(when it next changes with time alone), its `events` (each one's `decision`, `stream`,
`sequence` and `received`) and its `progress` (`kind`, `events`, `seen` of `needed`, the
`conditions` an `all` window holds, and `until`). The management job **Cleanup Event Streams**
removes a window still there a day after its deadline, such as one of a stopped rule, and drops
its decisions.

### Actions

- **`launch`** launches the template as the rule's owner, with `launch_type` `manual`, as a
  user does. The template must prompt for what the rule sets: variables by
  `ask_variables_on_launch` or its survey, the limit by `ask_limit_on_launch`. Variables
  named `ansible_*` are refused unless `ALLOW_ANSIBLE_VARS_AT_LAUNCH` is on. The job's
  `summary_fields.event_rule` names the rule, and `summary_fields.stream_event` the event: its
  `stream`, `sequence`, `id`, `source`, `type` and `subject`, and how many `events` the action
  fired for. `GET /api/v2/jobs/?event_rule=N` lists the jobs of rule N.
- **`notify`** sends a notification with the template. A `webhook` posts the message when it is
  an object, and otherwise the rule, the event and the message (`message`). An `email` has the
  message as its subject and body.
- **`post_event`** posts an event to the target stream, with the ID
  `rule-<rule>-<stream>-<sequence>` and the source `/api/v2/event_rules/<rule>/`. Other rules
  can read it. The extension `awxnextdepth` counts the rules an event went through: past 8,
  the rule refuses to post, so rules that post to each other don't loop.
- **`log`** records the message in the decision, with the outcome `logged`: its `reason` holds
  the first 1,000 characters, and `details.message` the whole message.

A rule that launches can follow its jobs, as ansible-rulebook's `run_job_template` does. With
`retries`, when a job fails, the rule waits `retry_delay` seconds and launches the template
again with the same prompts, as its owner, up to `retries` times; a job that ends in `error` or
`canceled` isn't launched again. With `post_artifacts`, when a job ends, the rule posts the
job's artifacts as an event of type `post_event` (`event`) or `set_fact` (`fact`), with the ID
`rule-<rule>-job-<job>`, to `artifacts_stream`. Posting takes the right to change that stream.

### Owner and rights

A rule runs as its owner: the user who saved it last, or, for a GitOps source, the user who
last saved the source. To save an enabled rule, the owner must be able to view its streams and
to perform its action: launch the template, change the notification template, or change the
target stream. Otherwise the save is refused with `403` and the reason.

awxnext checks the same rights again before each action. When the owner lost one, the rule
stops: it is disabled, and its `stopped_reason` and the event's decision say why. To restart
the rule, enable it again: you become its owner.

A rule in `auto` mode also takes the `automate` permission on the rule: the `EventRule Automate`
role, or `Organization Admin`. `EventRule Admin` doesn't include it. Because whoever saves an
enabled rule becomes its owner, saving a rule in `auto` mode takes that role too.

### Modes

A rule's `mode` says what it does with an action that its guardrails let through:

| Mode | What the rule does |
|---|---|
| `shadow` | Records what it would have done, and does nothing. New rules start in this mode. |
| `approval` | Asks for a workflow approval of each action, and acts once it is approved. |
| `auto` | Acts. |

To turn a rule off, set `enabled` to `false`. Rules created before awxnext had modes are in
`auto` mode.

In `shadow` mode, the rule applies its filter, throttle and guardrails as in the other modes. Its
decisions have the outcome `shadow`, with the reason ("Shadow mode: the rule would have launched
job template Restart service on web-01.") and, in `details.action`, what it would have done: the
template, the `extra_vars` and the `limit`, the notification's message, or the event it would
have posted. The throttle keys and circuit breakers of `shadow` mode are its own: when you move
the rule to another mode, its shadow decisions don't throttle it or count toward its breaker.

### Moving a rule from shadow to auto

1. Create the rule. It starts in `shadow` mode.
1. Let it see real events, then read its decisions: `GET /api/v2/event_rules/N/decisions/`.
   Check that each `shadow` decision is an action you want, and that `throttled` and `held`
   decisions hold back what you expect. To see what the rule would have done with the events
   the log already holds, run a [backtest](#backtests).
1. Optional: to have a person check each action first, move the rule to `approval` mode:
   `PATCH /api/v2/event_rules/N/` with `{"mode": "approval"}`.
1. When the decisions are right, move the rule to `auto` mode with `{"mode": "auto"}`. You
   need the `EventRule Automate` role on the rule, or `Organization Admin`; otherwise awxnext
   refuses with `403`.

Changing the mode of a rule that holds `once_after` windows drops their events: the rule starts
again from the next event. Moving a rule to `shadow` mode clears its shadow throttle keys and
breakers, so each try starts afresh.

### Approval mode

In `approval` mode, each action creates a workflow approval, as a workflow's approval node does,
and the decision waits with the outcome `awaiting_approval` and the approval's ID in `approval`.
The approval is named after the rule and the action ("Restart on ServiceDown: launch job template
Restart service on web-01"), its description names the event, and its
`summary_fields.event_rule` names the rule. It appears on the approvals page and in the bell, and
the organization's approval notifications tell of it as of a workflow's, with a link to the
approvals page.

People who can approve or deny a rule's approvals:

- Users with the `EventRule Approve` or `EventRule Admin` role on the rule.
- Users with the `Organization Approval` or `Organization Admin` role on its organization.

Those who can view the rule see its approvals. `GET /api/v2/workflow_approvals/?event_rule=N`
lists them. On the approvals page, a rule's approval links to its rule.

Once the approval is decided, the decision takes its outcome:

| Approval | Decision |
|---|---|
| Approved | The rule acts, as its owner, and the decision is `launched`, `notified` or `posted`. awxnext checks the owner's rights and the blast radius again first; it doesn't check the maintenance windows and the breaker again. |
| Denied | `denied`, with who denied it. |
| Timed out after `approval_timeout` seconds | `timed_out`. |
| Canceled, or the rule disabled or moved to `shadow` mode | `canceled`. awxnext cancels the pending approvals of a rule disabled, moved to `shadow` mode or deleted. |

The rule acts once for each approval, even when several nodes run. An action that waits for
approval holds its `once_within` key, and counts toward the circuit breaker, as an action does.

### Guardrails

Before each action, in every mode, awxnext checks the rule's guardrails. A guardrail that holds
an action back gives the decision the outcome `held`, with the guardrail in `guardrail`, the
reason in `reason`, and what it found in `details`. An action held back is dropped: the rule
doesn't try it again later, and it doesn't hold its `once_within` key, so the key's next event
can act.

The guardrails are checked in this order: the circuit breaker, the maintenance windows and
change freezes, then the blast radius.

#### Blast radius

These guardrails apply to rules that launch. Each compares hosts by name within an organization:
a host named `web-01` in two inventories of the organization is the same host.

| Field | What it holds back | `guardrail` |
|---|---|---|
| `max_concurrent_runs` | A launch while the rule has this many jobs or workflow jobs that haven't finished. `0`: no limit. | `max_concurrent_runs` |
| `host_lock` | A launch on a host that a job or ad hoc command that hasn't finished runs on, the rule's own included. `details.hosts` lists the hosts, and `details.jobs` the jobs. | `host_lock` |
| `max_group_share` | A launch that would put more than this percentage of the hosts of an inventory group (`all` included) under remediation at once: the launch's hosts and those of the unfinished jobs that rules launched, workflows' jobs included. A group may always have one host under remediation. `0`: no limit. | `group_share` |

The hosts a launch runs on are those of its inventory that its limit selects, as Ansible
resolves a pattern: groups and their children, host names, wildcards, `~` regular expressions,
`&` and `!`. For a workflow, awxnext resolves the inventory and the limit of each job template
node, prompts included. A pattern with a subscript (`web[0:2]`) or a file (`@hosts.txt`) selects
more hosts than Ansible would, never fewer, and `details.hosts_approximate` says so. Disabled
hosts are left out.

#### Circuit breaker

When a key fires `breaker_firings` times within `breaker_window` seconds, its circuit breaker
trips: the rule holds back the key's events, with the guardrail `circuit_breaker`, until a person
resets it. A firing is an action that ran, failed, waited for approval, or would have run in
`shadow` mode. A [check](#checking-fixes) that finds the key not fixed, or the action failed,
counts as one more firing: a failed action counts twice. The other keys are not affected. `0`
turns the breaker off.

When the breaker trips, awxnext sends a notification with `breaker_notification_template`. A
`webhook` posts the rule, the `key`, the `firings`, the `window` and the `reset` URL; an `email`
has them in its body. Trips in `shadow` mode don't notify.

To list a rule's breakers, `GET /api/v2/event_rules/N/breakers/`, or
`/api/v2/event_rule_breakers/` for every rule you can see. A tripped breaker has `tripped_at`
and the `firings` that tripped it; a reset one has `reset_at` and `reset_by`. To reset a
breaker, post its key; post no key to reset every breaker of the rule:

```sh
curl -u admin -H 'Content-Type: application/json' https://awxnext.example.com/api/v2/event_rules/7/reset_breaker/ -d '{"key": "web-01:9100"}'
```

The answer gives how many breakers were reset. Resetting takes the right to change the rule.
After a reset, the breaker counts the firings from then on.

On the rule's page, **Circuit breakers** lists its tripped breakers, with **Reset** for each and
**Reset all**.

#### Maintenance windows and change freezes

A maintenance window or a change freeze is a calendar of periods when rules must not act. Inside
one, a rule's action is held back, with the guardrail `maintenance_window` or `change_freeze`, or,
when the rule's `window_action` is `approval`, it waits for a [workflow
approval](#approval-mode), in any mode but `shadow`. The window time is when awxnext received the
event.

To create one, post to `/api/v2/maintenance_windows/`:

| Field | What it sets |
|---|---|
| `organization`, `name` | The window's organization, and a name unique in it. Organization admins manage the organization's windows; the roles `MaintenanceWindow Admin` and `MaintenanceWindow Read` give one window. |
| `kind` | `maintenance` (the default) or `freeze`. |
| `rrule` | When each period starts: `DTSTART`, `RRULE`, `EXRULE`, `RDATE` and `EXDATE`, as a schedule's `rrule`. Use `EXDATE` to skip a date. |
| `duration` | How long each period lasts, in seconds, from one minute to 90 days. |
| `event_rules` | The IDs of the rules the window covers. |
| `groups` | The IDs of the inventory groups the window covers: a launch on one of their hosts, or of their children's. |
| `catalog_items` | The IDs of the catalog items the window covers: their requests are refused, or wait for an exception ([Freezes](#freezes)). |
| `enabled` | `false` turns the window off. |

A window covers the rules that it lists, the launches on the hosts of the groups that it lists,
the requests of the catalog items that it lists, or, when it lists none of them, every rule of
its organization. It covers only the rules and items of its own organization. Listing a rule
takes the right to change it, listing a group the right to change its inventory, and listing an
item the right to change the item. Deleting a rule, a group or an item takes it out of the
windows that list it.

The answer's `active` says whether a period is going on, `active_until` when it ends, and
`next_start` when the next one starts. Windows have a version history and `used_by`, and a
GitOps source can declare them (`MaintenanceWindow`, see [GitOps](gitops.md)).

**Events, Maintenance windows** in the navigation lists the windows, whether each is on and when
it starts next. **New window** creates one, with the same calendar editor as schedules.

This change freeze holds back every rule of organization 1 on Saturdays from 22:00 to midnight,
Zurich time, but on 26 December:

```sh
curl -u admin -H 'Content-Type: application/json' https://awxnext.example.com/api/v2/maintenance_windows/ -d '{
  "name": "Saturday freeze",
  "organization": 1,
  "kind": "freeze",
  "rrule": "DTSTART;TZID=Europe/Zurich:20261107T220000 RRULE:FREQ=WEEKLY;BYDAY=SA EXDATE;TZID=Europe/Zurich:20261226T220000",
  "duration": 7200
}'
```

### Checking fixes

A rule can check that its action fixed the problem that fired it: it waits for the event that
shows the key resolved, such as the alert resolved, or it runs a check template. Each check
gets an outcome, and the rule gets a success rate.

| Field | What it sets |
|---|---|
| `check` | `event`: wait for a resolving event. `template`: launch `check_template`. Empty, the default: no check. |
| `check_condition` | For `event`: a CEL condition on `event` that a resolving event matches, such as `event.type == "alert.resolved"`. The rule's `key`, evaluated on that event, names the key it resolves. The filter doesn't apply to it. |
| `check_window` | For `event`: the seconds the rule waits for the resolving event once the action finished, 1 at least. For `template`: the seconds it waits before it launches the check template; `0` launches it at once. |
| `check_template` | For `template`: the job or workflow template that checks the fix. |
| `check_notification_template` | Optional: the notification sent when a check finds a key not fixed, or the action failed. |
| `check_approval` | In `auto` mode: once a check finds a key not fixed, the rule's next action on the key waits for an approval, until a check finds the key fixed. |

A notification or a posted event finishes at once; a launch finishes when its job does. Each
action opens one check for each key it ran for: a pattern's action, one for its key.

With `check: event`, the check is `fixed` when an event of the rule's streams that matches
`check_condition` resolves the key after the event that fired the action, while the job runs or
within `check_window` seconds after it finished. Otherwise, the check is `not_fixed`. The rule
looks at each event for the conditions of its open checks before it applies its filter, so a
resolving event that the filter doesn't match still resolves the key. The window ends once the
rule has read the events received before its end: a backlog doesn't make a check miss a
resolving event received in time.

With `check: template`, awxnext launches `check_template` as the rule's owner, `check_window`
seconds after the action finished. For a rule that launches, the check template gets the
action's `extra_vars` and `limit`, so it must prompt for them, as the action's template does.
The job decides: `successful` is `fixed`, `failed` is `not_fixed`, and `error` or `canceled`
gives `error`. A playbook that sets the artifact `fixed` decides by it instead, whatever the
job's status:

```yaml
- name: Ask the service for its health
  ansible.builtin.uri:
    url: "http://{{ inventory_hostname }}/healthz"
  register: health
  failed_when: false
- name: Report the service as not fixed
  ansible.builtin.set_stats:
    data: {fixed: false}
    per_host: false
  when: health.status != 200
```

| Outcome | Meaning |
|---|---|
| `pending` | The check is under way: the action runs, the rule waits for the resolving event, or the check job runs. |
| `fixed` | The resolving event came in time, or the check job succeeded or set `fixed` to `true`. |
| `not_fixed` | No resolving event came in time, or the check job failed or set `fixed` to `false`. |
| `action_failed` | The action failed: the template refused the launch, or the action's job failed or ended in error. |
| `error` | The check couldn't decide: the check job ended in error or was canceled, the check template couldn't launch, or a job was deleted. |
| `canceled` | The action's job was canceled, or the rule was disabled, stopped, or got another `check` before the check ended. |
| `resolved`, `unresolved` | In `shadow` mode: whether the key resolved by itself, with no action. |

In `shadow` mode, the rule only watches: an event check waits `check_window` seconds from the
decision, and a template check doesn't run. Its outcomes, `resolved` and `unresolved`, say how
many keys would have looked fixed with no action. Compare them with the success rate in
`auto` mode before you trust the rule's fixes. Shadow checks don't count toward the success
rate and don't escalate. In `approval` mode, the check starts when the approved action runs.

A check is a row in PostgreSQL: it survives a restart, and the node that holds a stream, or any
node once the check is due, settles it.

#### Escalation

When a check finds a key `not_fixed`, or the action failed, outside `shadow` mode:

- awxnext sends `check_notification_template`. A `webhook` posts the `rule`, the `key`, the
  firing `event`, the action's `job`, and the `check` with its `id`, `outcome`, `reason` and
  `url`; an `email` has them in its body.
- With `check_approval`, in `auto` mode, the key's next action waits for a workflow approval,
  as in [approval mode](#approval-mode). Its decision has the guardrail `check_escalation`.
  Once a check finds the key fixed, the rule acts by itself again.
- The check counts as one more firing for the [circuit breaker](#circuit-breaker), and can trip
  it at once.

`escalated` marks the checks that notified or asked for approval.

#### Success rate

A rule's `success_rate` is the share of its checks settled in the last
**Days of an event rule's success rate** (`EVENT_RULE_SUCCESS_RATE_DAYS`, 30 by default) that
found the key fixed: `fixed` out of `fixed`, `not_fixed` and `action_failed`. It is `null` when
no check settled in that time. `summary_fields.checks` holds the counts: `checked`, `fixed`,
`not_fixed`, `action_failed`, `errors` (`error` and `canceled`, which the rate leaves out),
`pending`, and the `shadow` checks' `checked`, `resolved` and `resolved_rate`.

`GET /api/v2/event_rules/N/checks/` lists a rule's checks, and `/api/v2/event_rule_checks/` the
checks of every rule you can see. Each check has its `key`, `kind`, `mode`, the firing `event`
and the `decisions` it checks, the `action`'s outcome and `job`, the `check_job`, its `state`
(`acting`, `watching`, `checking` or `settled`), its `outcome` and `reason`, the `deadline`, and
the event that resolved the key in `resolved_by`. A decision names its check in `check` and the
check's outcome in `check_outcome`. A job's `summary_fields.event_rule_checks` lists the checks
it is the action of (`role: action`) or the check job of (`role: check`).

The metric `awxnext_event_rule_checks_total{rule, outcome}` counts the checks by outcome on each
node. To graph a rule's success rate across nodes:

```promql
sum by (rule) (increase(awxnext_event_rule_checks_total{outcome="fixed"}[7d]))
  / sum by (rule) (increase(awxnext_event_rule_checks_total{outcome=~"fixed|not_fixed|action_failed"}[7d]))
```

In the UI, the rule's page shows its success rate and its latest checks, and the list of rules
a column of success rates. The editor's **Check the fix** section sets the check, and its
pane of latest events tags the ones that resolve a key. A decision shows its check's outcome,
and a job page the fix it made or checked.

### Decisions

`GET /api/v2/event_rules/N/decisions/` lists what the rule decided for each event it read, newest
first, and `/api/v2/event_rule_decisions/` the decisions of every rule you can see.
`GET /api/v2/stream_events/N/decisions/` lists the decisions of every rule on one event. Each
decision has the event (`stream`, `sequence`, `event_id`, `event_type`, `received`), the node
that decided, the rule's `mode` then, the `key`, the `outcome` and its `reason`, the `guardrail`
that held the action back or sent it for approval, the `approval` it waits for, the `job`
launched, and the `trace_id` of the event's [trace](#metrics-and-traces). `fired` marks the
decisions that count toward the circuit breaker. For a rule that [checks its fixes](#checking-fixes),
`check` names the check of the action and `check_outcome` gives its outcome. For a rule with a [pattern](#patterns),
`details.pattern` holds the key's window when the rule decided: its `kind`, the `events` it
holds, `seen` of `needed`, and `until` when it ends; a `cleared` decision names the event that
cleared it in `details.cleared_by`.

| Outcome | Meaning |
|---|---|
| `not_matched` | The filter didn't hold for the event, or failed on it, or the event matches none of the pattern's conditions. |
| `launched`, `notified`, `posted` | The rule acted. |
| `throttled` | `once_within` held the event back. |
| `waiting`, `deduplicated` | `once_after` or a pattern keeps the event for its window, or already has one with its key (for a pattern, 1,000 events). |
| `expired` | The event left its pattern's window before the pattern was complete, or was received after the window moved past it. |
| `cleared` | An event of an absence's second condition came within the window: the rule doesn't fire. |
| `ignored` | The event matches a sequence's step whose step before it hasn't come, or an absence's second condition with nothing waiting. |
| `refused` | The owner may no longer run the rule, which stopped. |
| `failed` | The action failed, such as a launch the template refused. |
| `error` | An expression failed on the event, such as a key that is not a string. |
| `dropped` | The rule was disabled, or changed mode, pattern or key, before its window ended. |
| `held` | A [guardrail](#guardrails) held the action back: `guardrail` names it. |
| `shadow` | In `shadow` mode, what the rule would have done. |
| `awaiting_approval` | The action waits for the workflow approval in `approval`. |
| `denied`, `timed_out`, `canceled` | The approval was denied, timed out, or canceled. |

A `not_matched` decision names the part of the filter that failed. With the filter
`event.type == "alert.firing" && event.data.labels.alertname == "DiskFull"` and a `HighCPU`
alert, the reason is `The filter's condition event.data.labels.alertname == "DiskFull" is
false.`, and `details.filter` holds:

```json
{
  "expression": "event.data.labels.alertname == \"DiskFull\"",
  "start": 32,
  "end": 73,
  "values": [{"expression": "event.data.labels.alertname", "value": "HighCPU"}]
}
```

`start` and `end` are the part's offsets in the filter, and `values` what its operands were,
at most five. A long value is summarized: a string by its length, an object by its keys, a list
by its size. When the filter failed on the event, such as on a key the event doesn't have,
`error` says why. The decision holds no copy of the event: the event is in the stream's log.

To keep the log small, a `not_matched` decision is kept as long as its event, for its stream's
`retention_days`. The other decisions are kept for **Days event rule decisions are kept**
(`EVENT_RULE_DECISION_RETENTION_DAYS`, 90 by default), or longer when their stream keeps its
events longer; `0` keeps them as long as their events. The management job
**Cleanup Event Streams** removes them, the throttle keys that expired more than a day ago, and
the pattern windows a day past their deadline, and the checks of fixes settled before the oldest
decisions it keeps.
The decision log is partitioned by day, like the event log.

### Backtests

A backtest runs a rule over a period of the event log, as `shadow` mode would have, and says
how many times the rule would have fired, on which keys and hosts, and what its guardrails would
have held back. It records no decision and takes no throttle key, so you can run it on a rule in
any mode.

To backtest a saved rule, post to `/api/v2/event_rules/N/backtest/`. To try a change first, add
the rule's fields that you'd change: the backtest runs that draft and leaves the rule as it is.
To backtest a rule you haven't saved, post all its fields to `/api/v2/event_rules/backtest/`:

```sh
curl -u admin -H 'Content-Type: application/json' https://awxnext.example.com/api/v2/event_rules/7/backtest/ -d '{
  "since": "2026-11-14T00:00:00Z",
  "until": "2026-11-15T00:00:00Z",
  "host_lock": true
}'
```

| Field | What it sets |
|---|---|
| `since`, `until` | The period, in RFC 3339. By default, the last 24 hours. |
| `max_events` | The most events to read, at most 10,000 (the default). |

The answer has the counts (`events`, `matched`, `fired`, and `firings`, the actions, which a
pattern's window counts once for all its events), the `outcomes` and the `held` actions
by guardrail, the `keys` and `hosts` that fired most, at most 100 of each, the circuit
`breakers` that tripped, the filter's `misses` grouped by the part that failed, and the first
200 `decisions` that matched. `truncated` is `true` when the period held more events than
`max_events`, or the backtest stopped at its 30-second limit: `until` is then the last event read.

For a rule with an event [check](#checking-fixes), `checks` counts the firings whose key a
resolving event of the log resolved in time (`resolved`), those it didn't (`unresolved`), the
`action_failed`, and the `pending` ones whose window was still open at `until`; `resolved_rate`
is `resolved` out of the three. Those events resolved the keys without the rule's actions, or
by the actions taken then: in `shadow` mode, the rate is how many fixes would have looked good
anyway. Each decision that fired has its `check_outcome`, and the event that resolved it in
`resolved_by`. Check templates don't run in a backtest.

A backtest approximates what happened. Its `approximations` list says how:

- Throttles, `once_after` windows, patterns' windows and circuit breakers start empty at
  `since`. A pattern's window still open at `until` stays `waiting`, and an absence fires at
  its deadline only if that is before `until`.
- Maintenance windows, change freezes, inventories and templates are today's.
- The blast radius counts the jobs that the job history shows running at each event's time,
  but the rule's own, and the launches that the backtest simulates. A simulated launch runs as
  long as the template's last finished jobs took on average, or five minutes.
- An action that would ask for approval counts as `awaiting_approval`, and as a firing.
- An action's check starts when the action fires, and lasts the template's average run time
  and `check_window`. A key not fixed counts toward the circuit breaker, and doesn't ask for
  approval.

To run a backtest, you need to see the rule, its streams, and the template, notification
template or stream it acts on. Otherwise awxnext refuses with `403`.

### Testing rules offline

`awxnext rules test DIR` runs sample events through the event rules of a
[GitOps](gitops.md) directory, without a server, and compares the decisions with the ones you
expect. Run it in the CI of the repository that declares your rules. Tests are YAML documents
of kind `EventRuleTest`, in any file of the directory; a sync skips them. This test checks the
rule in [Alertmanager remediation](#alertmanager-remediation):

```yaml
kind: EventRuleTest
name: Restart once per instance an hour
spec:
  rule: Restart on ServiceDown        # the EventRule, by name
  start: "2026-11-02T08:00:00Z"       # when the first event is received
  events:
    - type: alert.firing
      data: {labels: {alertname: ServiceDown, instance: web1, job: nginx}}
      expect:
        outcome: fired
        key: web1
        extra_vars: {service: nginx}
    - at: 10m                         # received 10 minutes after start
      type: alert.firing
      data: {labels: {alertname: ServiceDown, instance: web1, job: nginx}}
      expect: throttled
    - at: 75m
      type: alert.firing
      data: {labels: {alertname: DiskFull, instance: web1}}
      expect:
        outcome: not_matched
        reason: event.data.labels.alertname == "ServiceDown"
```

An event is a CloudEvent's `type`, `data`, and optionally `id`, `source`, `subject`, `time` and
`extensions`. An event without `expect` is only read, such as an alert resolved that settles a
check. Or it is a `payload` as a stream of `format` receives it, such as `alertmanager`,
with optional `headers`: a payload can make several events, and `expect` then takes one
expectation, or a list of one for each event. `at` is a duration after `start`, or a date and
time; without `at`, an event is received a second after the one before. `rule` can be left out
when the directory has one rule. `until`, a duration after `start` or a date and time, ends the
test: a window still open then keeps its events `waiting`. Without `until`, the test ends once
every window has ended, so an absence fires and an incomplete count, `all` or sequence expires.

`expect` is the outcome alone, or an object with the `outcome` and any of these:

| Field | What it checks |
|---|---|
| `key` | The event's key. |
| `guardrail` | The guardrail that held the action back or sent it for approval. |
| `reason` | Text that the reason holds; for `not_matched`, the part of the filter that failed and its values. |
| `extra_vars`, `limit` | For a launch: the variables listed, and the limit. |
| `message` | For a notification: the message. |
| `event_type`, `event_data` | For a posted event: its type and data. |
| `check` | For a rule with an event [check](#checking-fixes): the check's outcome, `fixed`, `not_fixed`, `action_failed` or `pending`, or in `shadow` mode `resolved` or `unresolved`. The test's later events resolve the key. Without `until`, a check that no event resolved is `not_fixed`; with `until`, one whose window is still open then is `pending`. |

The outcomes are `fired` (the rule acted, or would have in `shadow` mode), `awaiting_approval`,
`held`, `throttled`, `deduplicated`, `waiting`, `expired`, `cleared`, `ignored`, `not_matched`
and `error`. The test checks the filter, the key, both throttles, the pattern and its conditions,
the circuit breaker, the rule's mode and `window_action`, the
maintenance windows and change freezes the directory declares for the rule or for every rule,
and the action's expressions, which it type-checks against the test's events. The blast radius
(`max_concurrent_runs`, `host_lock`, `max_group_share`) needs the jobs running on a server, and
windows that list only inventory groups need the inventory: the test doesn't check them, and an
expectation of a hold by one of them fails. A check template runs on the server too: an
expectation of its `check` fails. A check that finds a key not fixed counts toward the circuit
breaker, as on the server. A `once_after` window still open at the end of a test
fires. This test checks the absence rule in [Patterns](#patterns), declared in git:

```yaml
kind: EventRuleTest
name: Escalate when ServiceDown is not resolved within 15 minutes
spec:
  rule: Escalate a ServiceDown that stays down
  events:
    - type: alert.firing
      data: {labels: {alertname: ServiceDown, instance: web1}}
      expect: {outcome: fired, key: web1}  # at the end of the window
    - at: 6m
      type: alert.firing
      data: {labels: {alertname: ServiceDown, instance: web2}}
      expect: cleared
    - at: 10m
      type: alert.resolved
      data: {labels: {alertname: ServiceDown, instance: web2}}
      expect: cleared
```

```console
$ awxnext rules test -v rules/
ok    rules_test.yml#1  Restart once per instance an hour (3 events)
1 test, 1 passed, 0 failed
```

`awxnext rules test` exits with `0` when every test passes, `1` when a test fails, and `2` when
a document doesn't load or a rule's expression doesn't compile. `-v` lists the tests that pass
too. The repository's [`docs/examples/rules`](examples/rules) directory holds a rule, a change
freeze and their tests.

### Metrics and traces

`/metrics` publishes these metrics for each node. Streams and rules are labelled by their ID,
never by key or event value:

| Metric | What it counts |
|---|---|
| `awxnext_event_stream_received_total{stream}` | Events a stream stored. |
| `awxnext_event_stream_duplicates_total{stream}` | Events a stream dropped as already received. |
| `awxnext_event_stream_refused_total{stream, reason}` | Requests a stream refused: `invalid`, `unauthorized`, `forbidden`, `not_found`, `too_large`, `rate_limited`, `unavailable` or `error`. `stream` is empty when the URL names no stream. |
| `awxnext_event_stream_lag_seconds{stream}` | A histogram of the seconds from an event's receipt to the rules' decisions on it. |
| `awxnext_event_stream_backlog_events{stream}` | The events the rules still have to decide, as of the node's last batch. |
| `awxnext_event_rule_matched_total{rule}` | Events the rule's filter held for. |
| `awxnext_event_rule_decisions_total{rule, outcome}` | Decisions by outcome. A decision that waits for a window or an approval counts again when it gets its final outcome, such as `launched` or `failed`. |
| `awxnext_event_rule_held_total{rule, guardrail}` | Actions a guardrail held back. |
| `awxnext_event_rule_checks_total{rule, outcome}` | [Checks of fixes](#checking-fixes) settled, by outcome. |

With tracing on (`OTEL_EXPORTER_OTLP_ENDPOINT`), each event the rules decide has a trace: a span
`event_stream event`, with a span `event_rule decide` for each rule that matched it, and the
spans of the job it launched under that. The trace continues the event's own: the CloudEvents
`traceparent` extension, or the `traceparent` header of the request that carried the event.
An event without one starts a new trace. A decision's `trace_id` names its trace, and an event
the rule posts carries the trace on.

### Exactly once

Each stream's events are decided in order, by one node at a time: a node holds a stream while it
holds the stream's lock in PostgreSQL. A node decides a batch of events in one transaction:
the decisions, the actions (the job, the notification, the posted event) and the stream's new
position commit together, or not at all. If a node stops during a batch, the node that takes the
stream over starts where the last commit stopped, so no event is lost or decided twice. A
decision is unique for each rule and event, so an event read again, or sent again with the same
ID, never acts twice.

A rule acts on the events it reads while it is enabled: one created or enabled now starts with
the next event.

### Alertmanager remediation

This rule restarts the service that an Alertmanager alert names, at most once an hour for each
instance. The stream is the one in [Alertmanager](#alertmanager); the job template
`Restart service` prompts for variables and the limit:

```sh
curl -u admin -H 'Content-Type: application/json' https://awxnext.example.com/api/v2/event_rules/ -d '{
  "name": "Restart on ServiceDown",
  "organization": 1,
  "streams": [3],
  "filter": "event.type == \"alert.firing\" && event.data.labels.alertname == \"ServiceDown\"",
  "key": "event.data.labels.instance",
  "throttle": "once_within",
  "throttle_window": 3600,
  "unified_job_template": 12,
  "extra_vars": {"service": "event.data.labels.job", "summary": "event.data.annotations.summary"},
  "limit": "event.data.labels.instance.split(\":\")[0]"
}'
```

Each alert launches `Restart service` on its instance's host, with the variables `service` and
`summary`. The same alert sent again at Alertmanager's `repeat_interval` is a duplicate, and a new
one for the same instance within the hour is `throttled`. To declare the rule in git instead, see
[GitOps](gitops.md).

## Importing rulebooks

awxnext imports the rulebooks of [ansible-rulebook](https://ansible.readthedocs.io/projects/rulebook/)
1.3: it translates each ruleset into event streams, event connectors and event rules, and
reports, for each source, rule and action, whether it translated it, and why not. Every rulebook
belongs to a project:

- **A project's files.** Each sync of a git project imports the `.yml` and `.yaml` files at the
  top of `extensions/eda/rulebooks/` and `rulebooks/` that hold a list of rulesets, at most 200
  files of 1 MiB each. Files in subdirectories, and other YAML files, are left out. To import
  the project's last checkout again without a sync, post to
  `/api/v2/projects/N/import_rulebooks/`; a manual project, which has no sync, imports its
  directory's rulebooks that way. A rulebook that can't be imported doesn't fail the sync: its
  log says why.
- **An uploaded file.** To upload one, post to `/api/v2/rulebooks/` with `name`, `project` and
  `rulesets`, the rulebook's YAML. Each change to `rulesets` or `variables` imports it again.

`GET /api/v2/projects/N/rulebooks/` lists a project's rulebooks. A rulebook has these fields:

| Field | What it holds |
|---|---|
| `name`, `project` | The file's name, or its path when two files have the same name; the project. |
| `path` | The file in the project. Empty: an uploaded rulebook. |
| `rulesets` | The rulebook's YAML. |
| `variables` | The rulebook's variables, as ansible-rulebook's `--vars`: `vars.name` in conditions, `{{ name }}` in the arguments of sources and actions. You can set them on a file's rulebook too. |
| `source_streams` | The sources that an existing event stream feeds instead of their own, as EDA's source mappings: `{"<source name>": <stream ID>}`. A source without a name is `__SOURCE_n`, the rulebook's nth source. A mapped source gets no stream or connector of its own: its ruleset's rules read the stream. You can set them on a file's rulebook too, with a stream you can see. |
| `revision`, `imported` | The project's revision the file came from; when the rulebook was last imported. |
| `import_error` | Why the last import couldn't read the rulebook. The objects of the import before stay as they were. |
| `report` | What became of each ruleset, source, rule and action: `translated`, `partial` or `not_translated`, the `reasons` for what isn't, the `notes` where awxnext behaves otherwise than ansible-rulebook, and the `object` each piece became. `summary` counts the rules by status. |

`/api/v2/rulebooks/N/event_streams/`, `event_connectors/` and `event_rules/` list the objects a
rulebook's imports made, and `POST /api/v2/rulebooks/N/import/` imports it again. Whoever can
view the project can view its rulebooks and their reports; adding, changing, deleting and
importing a rulebook takes the right to change the project. Rulebooks aren't a GitOps kind:
their project's git repository already holds them.

In the UI, a project's **Rulebooks** tab lists them, imports them again and uploads one; a
rulebook's page shows its report and source mappings, with a link to each object. Its **Edit**
page sets the variables and maps each source to an event stream. The editor of an imported rule
leaves its streams, conditions and action alone, and changes its description, mode, fix check
and guardrails.

### What an import makes

The objects are named after the project, the rulebook, the ruleset and the source or rule, such
as `ops/rulebooks/web.yml: Web / Deploy`. They belong to the project's organization.

- **Sources.** `ansible.eda.webhook` becomes a `generic` event stream, and
  `ansible.eda.alertmanager` an `alertmanager` stream: senders post to the stream's receiver URL
  rather than to the plugin's host and port, and authenticate with the stream's credential.
  Any other source becomes an event connector that runs the plugin, and posts to a
  CloudEvents stream of the ruleset. The `eda.builtin` sources of ansible-rulebook run as their
  twins in `ansible.eda`. A source from a directory (`ansible-rulebook -S`) isn't translated.
- **Rules.** Each action of a rule becomes an event rule; `debug` and `print_event` alongside
  another action are recorded by that action's decisions.
- **Templates.** `run_job_template` and `run_workflow_template` name a template by `name` and
  `organization`. The import looks it up among the templates the importing user can view: the
  person who uploaded or imported the rulebook, or else the user who last changed the project.
  A template that doesn't exist is reported; create it, and import the rulebook again. The
  names may come from the rulebook's variables, not from the event.

An import makes nothing that acts. Its rules start disabled, in `shadow` mode, with no owner;
its streams start without a credential, so they refuse events; its connectors start disabled.
To turn a ruleset on:

1. Give each stream a credential, and each connector its execution environment and
   credentials, then enable the connectors.
2. Enable each rule in `shadow` mode, and read its decisions against real events.
3. Move the rules to `auto` or `approval` (see
   [Moving a rule from shadow to auto](#moving-a-rule-from-shadow-to-auto)).

### Importing again

Each import updates the objects of the import before: a rule, a source or an action that
stays in the rulebook keeps its object, with its ID, its history and its decisions. The
translated fields follow the rulebook: the filter, the key, the pattern, the throttle, the
action and its template, variables and limit. What an operator decides stays: `enabled`,
`mode`, the description, the guardrails, the checks, the stream's credential, retention and
test mode, and the connector's execution environment and credentials. A rule that is new in
the rulebook starts disabled; an object whose piece left the rulebook is deleted; a file that
left the project takes its rulebook and objects with it.

An enabled rule whose translation changed is checked again, as its owner. When the owner can't
use the new template or stream, the import disables the rule, and its `stopped_reason` says
why.

Changing a translated field of an imported object by hand answers `409` and names the fields
to change in the rulebook; deleting an imported object answers `409` too. Deleting an uploaded
rulebook deletes its objects. To delete a file's rulebook, remove the file and sync the
project.

### How rulebooks translate

**Conditions** become CEL filters. awxnext translates `==`, `!=`, `<`, `<=`, `>`, `>=`, `+`
and `-` on numbers, `and`, `or`, `not`, `is defined`, `is not defined`, `in`, `not in`,
`contains`, `not contains`, `is search`, `is match` and `is regex` with `ignorecase` and
`multiline`, `is select` and `is selectattr` with their operators, `vars.` from the
rulebook's variables, and `{{ }}` with `default(value)` in the arguments of sources and actions.
As in ansible-rulebook, a condition on an attribute the event lacks doesn't hold, and a bare
attribute holds only when it is `true`.

**Patterns.** `all` becomes an `all` pattern, with its `timeout`, or else the ruleset's
`default_events_ttl`, as the window. `any` becomes one filter that holds for any of the
conditions. `not_all` with 2 conditions and a `timeout` becomes an `absence` pattern. The
actions of a rule with several conditions read its events as `events.m_0`, `events.m_1` and so
on. A throttle's `once_within` and `once_after` become the rule's throttle, keyed by its
`group_by_attributes`, and `accumulate_within` a `count` pattern of `threshold` events.

**Ruleset options.**

- `match_multiple_rules` false (the default): an event goes to the first rule it matches, so
  each rule's filter leaves out the events of the rules before it. True: each rule acts on
  every event it matches.
- `default_events_ttl` is the window of the ruleset's patterns, 2 hours by default.
- `execution_strategy`: see the differences below.
- `gather_facts` isn't translated: awxnext's rules don't see the hosts' facts.
- `hosts` is the limit of a launch that sets none, when its event names no hosts in
  `event.meta.hosts`, and of every such launch of a rule with several conditions.

**Actions.**

| ansible-rulebook | awxnext |
|---|---|
| `run_job_template`, `run_workflow_template` | `launch`, with `job_args.extra_vars` and `job_args.limit`, the variable `ansible_eda` (the ruleset, the rule and the event or events, as ansible-rulebook sets it), `retries` and `delay` as `retries` and `retry_delay`, and `post_events` and `set_facts` as `post_artifacts`. The template must prompt for variables and the limit: otherwise AWX leaves them out, and so does the rule, with a note. |
| `post_event`, `set_fact` | `post_event` to the ruleset's stream, or the stream of the ruleset it names. |
| `debug`, `print_event` | `log`: the message, or the event, in the decision log. |
| `run_playbook`, `run_module`, `shutdown`, `retract_fact` and the others | Not translated: the report says why. |

### Differences from ansible-rulebook

Where awxnext can't do what ansible-rulebook does, the import reports the piece as not
translated rather than approximating it. Where it does it otherwise, the report notes it:

- **Facts.** awxnext keeps no facts. `set_fact` and `set_facts` post an event that the rules
  match once, as it comes, and conditions on `fact.` read that event. `retract_fact` isn't
  translated.
- **Joins.** A condition reads the event it tests: a condition on `events.` or `facts.`, such
  as `events.m_0.host == events.m_1.host`, isn't translated.
- **Sequential execution.** ansible-rulebook runs a ruleset's actions one at a time and waits
  for each job. awxnext's rules act as their events come; to keep jobs from overlapping, set
  `max_concurrent_runs` or `host_lock`.
- **Actions of a rule.** Each action is a rule of its own: they act on the same events without
  waiting for each other.
- **Alertmanager.** The stream gives the rules each alert, as ansible-rulebook with
  `skip_original_data: true`, and takes an alert again only when its status or start changes.
  ansible-rulebook acts again on each notification that repeats an alert; awxnext acts once,
  unless the throttle would let it act again. `event.payload`, `event.meta.endpoint` and
  `event.meta.headers` aren't translated.
- **Webhooks.** `event.meta.endpoint` and `event.meta.headers` aren't translated, and the
  plugin's `token` and `hmac_secret` are the stream credential's.
- **Expressions.** `*` and `/`, which ansible-rulebook 1.3 parses but doesn't evaluate, and
  regular expressions that Go's RE2 doesn't take, such as lookaheads, aren't translated.
- **Patterns.** An `absence` window opens on an event of the first condition only, where
  ansible-rulebook's `not_all` opens it on either; an event can count for several conditions of
  an `all` pattern, where ansible-rulebook matches it with one. A throttle on a rule with
  several conditions isn't translated: such a rule acts once per window.

awxnext's tests import every example rulebook of ansible-rulebook 1.3.2 and of the ansible.eda
2.13.0 collection. They also give the same Alertmanager notifications to an Alertmanager
rulebook run by ansible-rulebook itself and imported into awxnext, and check that each launches
the jobs and logs the messages that `test/testdata/rulebooks/alertmanager.expected.json` lists,
written from ansible-rulebook's semantics. awxnext launches one job fewer, for the alert that
Alertmanager sends again.

## EDA's API

awxnext answers the API of Event-Driven Ansible's controller, `/api/eda/v1/`, so the
`ansible.eda` collection's modules and the Terraform provider's EDA data source work against
it. `/api/` lists it as AAP does (`"apis": {"eda": "/api/eda/"}`). It is a view of awxnext's
own objects, with EDA's field names, filters, pagination (`page`, `page_size` up to 200,
`count`, `next`, `previous`), errors (`{"field": ["message"]}`, 404 for an unknown page) and
authentication (basic or a token, as `/api/v2/`). RBAC is awxnext's: a user sees and changes
what awxnext's roles let them, and an object they can't see answers 403.

| EDA | awxnext |
|---|---|
| `organizations/`, `teams/`, `users/`, `users/me/` | organizations, teams, users (EDA's user list shows fewer fields than a user's page, as EDA's does) |
| `role_definitions/`, `role_user_assignments/`, `role_team_assignments/` | awxnext's roles; EDA's `Organization Auditor` is `Organization Audit` |
| `credential-types/`, `eda-credentials/`, `credential-input-sources/` | credential types (a new one is `cloud` unless it says), credentials (one of a name per organization, as EDA has it), input sources; `test/` and `copy/` as awxnext's |
| `decision-environments/` | execution environments: `image_url` is the image, `pull_policy` the pull option |
| `projects/`, `projects/N/sync/` | git projects; `import_state` from the last sync, `import_error` its explanation, `proxy` and `verify_ssl` as below |
| `rulebooks/`, `rulebooks/N/sources/` | the rulebooks a project's syncs import ([Importing rulebooks](#importing-rulebooks)), and their sources with EDA's names (`__SOURCE_n` for one without a name) and the rulebook's hash |
| `event-streams/` | event streams, with EDA's `uuid`, `event_stream_type` (the credential's) and `additional_data_headers` |
| `activations/` | below |
| `users/me/awx-tokens/` | controller tokens, kept for each user, never shown |

A project's `proxy` (`scm_proxy` in `/api/v2/`, encrypted) is the HTTP proxy git syncs and
checks out through, its password shown as `$encrypted$`; `verify_ssl: false` turns off git's
certificate check. In the UI, a git project's form sets them as **Proxy** and **Check the TLS
certificate**. A stream's `additional_data_headers` names request headers its events
keep: each becomes an attribute of the event, named in lowercase letters and digits
(`X-Request-Id` is `event.xrequestid` in a rule). `Authorization`, `Proxy-Authorization` and
`Cookie` are never kept. EDA's senders post to `/api/eda/v1/external_event_stream/<uuid>/post/`,
which takes the same events as the stream's own URL.

### Activations

An activation runs a copy of one of a project's rulebooks: creating one uploads the rulebook to
the same project under the activation's name (the activation's ID is that rulebook's), with
`extra_var` as its variables, and imports it into connectors and rules of its own. The
project's rulebook, its objects disabled and in shadow mode, is left as its import made it, so
two activations of one rulebook are independent, and `rulebooks/` doesn't list activations'
copies. EDA-only settings (`restart_policy`, `log_level`, `restart_on_project_update`,
`enable_persistence`, the rule engine credential and the controller token) are kept in
`eda_activations`.

- **Enabled.** Enabling an activation, at its creation or later, enables its connectors with the
  activation's credentials, decision environment and restart policy, and its rules in `auto`
  mode, owned by the user who enabled it: the rules then act with that user's rights, as any
  rule ([Event rules](#event-rules)). Disabling turns both off. Since creating or enabling an
  activation changes its connectors and rules as its user, it takes the `EventConnector Admin`
  role on each connector and `EventRule Automate` on each rule (`EventRule Admin` to disable),
  or `Organization Admin`. The right to change the project, which an `Organization Project
  Admin` has, isn't enough: the `403` names the connector or rule and the role.
- **Status.** From its connectors: `running` when they run, `starting` while one starts or
  restarts, `failed` when one failed, `stopped` when disabled. An activation whose sources are
  all streams has no connector and is `running` while enabled.
- **Restart.** `restart/` restarts the connectors and counts in `restart_count`, as does a
  project sync that changes the rulebook of an activation with `restart_on_project_update`:
  the activation takes the new rulebook, the others keep theirs.
- **Copy.** `copy/` makes a disabled activation with the same settings and rulebook.
- **Source mappings.** `source_mappings` (YAML, kept as given) maps a source of the rulebook, by
  name and with the hash `rulebooks/N/sources/` gave, to an existing event stream: the rules
  read that stream instead of the source. A hash that no longer matches is refused.
- **Delete.** Deleting an activation deletes its rulebook and the connectors, rules and streams
  made from it; deleting a project deletes its rulebooks and activations.

What EDA has that awxnext doesn't: activations run no `ansible-rulebook`, so there is no
activation instance, log or `current_job_id` (each connector has its own runs and log), and
the rule engine credential, `enable_persistence` and `log_level` are kept but change nothing,
as awxnext keeps rules' state in PostgreSQL. `rules_fired_count` isn't given: a rule's
decision log has what it did. Rules that don't translate ([Differences from
ansible-rulebook](#differences-from-ansible-rulebook)) don't run in an activation either:
`status_message` counts them, and the activation's rulebook shows its report. The controller
token isn't used: rules launch jobs as their owner.

The `ansible.eda` 2.13.0 collection's integration targets run against awxnext nightly
(`compat/`, suite `eda`).

## Collection hub

awxnext serves Ansible collections on Galaxy's v3 API, in the URLs of galaxy_ng (Automation
Hub), so `ansible-galaxy` and `ansible-builder` install from it unchanged. It keeps collections
only: build execution environments with `ansible-builder` and push them to your own registry.

Collections belong to namespaces (`/api/v2/collection_namespaces/`), each owned by an
organization. A namespace's name is unique in awxnext and is the first part of its collections'
names (`acme` in `acme.tools`): lowercase letters, digits and single underscores, starting with
a letter, up to 64 characters. A namespace that has collections can't be renamed or deleted.
An organization's administrators create its namespaces; their collections are
[published](#publishing-a-collection) with `ansible-galaxy collection publish`, and wait in
`staging` until an approver of the namespace publishes them.

### Who sees a collection

You see and install a namespace's collections when you can see the namespace: as a member,
admin or auditor of its organization, a system administrator or auditor, or with the
`CollectionNamespace Read` or `CollectionNamespace Admin` role on it. Anyone else gets `404`, on
the collection, its versions and its download alike, and lists leave it out: a client then goes
on to its next server, and the namespaces of other organizations stay unknown. Every call but
the API's root needs a user: a personal token (`/api/v2/tokens/`, `read` scope is enough), sent
as `Authorization: Token <token>`, which is what `ansible-galaxy` sends, or `Bearer <token>`;
basic authentication while `AUTH_BASIC_ENABLED` is on; or a session.

That is the `published` repository. The `staging` repository (uploads waiting for approval) and
the `rejected` one are read only by the namespace's curators: whoever may upload, approve or
change there (the `CollectionNamespace Upload`, `Approve` and `Admin` roles, the organization's
administrators), the organization's auditors, and system administrators and auditors. A
namespace's readers and its organization's members get `404` there, and jobs' read tokens read
neither. The curators install from `/api/galaxy/content/staging/` to try a version before
approving it.

A [remote](#remotes)'s repository is read by the members and auditors of the remote's
organization, its jobs, and system administrators and auditors, whatever namespaces its
collections are in.

### URLs

| What | Path |
|---|---|
| Server for `ansible-galaxy` (the published repository) | `/api/galaxy/content/published/`, or `/api/galaxy/` |
| Server for a remote's repository | `/api/galaxy/content/<repository>/` |
| Collections | `/api/galaxy/v3/plugin/ansible/content/published/collections/index/` |
| A collection, its versions | `.../index/<namespace>/<name>/`, `.../index/<namespace>/<name>/versions/` |
| A version | `.../index/<namespace>/<name>/versions/<version>/` |
| Its artifact | `/api/galaxy/v3/plugin/ansible/content/published/collections/artifacts/<namespace>-<name>-<version>.tar.gz` |
| Upload (`POST`) | `/api/galaxy/v3/artifacts/collections/`, `/api/galaxy/content/<repository>/v3/artifacts/collections/` or `/api/galaxy/v3/plugin/ansible/content/<repository>/collections/artifacts/` |
| An upload's import | `/api/galaxy/v3/imports/collections/<id>/` |
| Approve or reject (`POST`) | `/api/galaxy/v3/collections/<namespace>/<name>/versions/<version>/move/<from>/<to>/` |
| Deprecate (`PATCH`), delete (`DELETE`) | a collection's or a version's path above |

Older clients' paths, `/api/galaxy/content/published/v3/collections/...` and
`/api/galaxy/v3/collections/...`, answer the same. Lists are paginated as Pulp's: `limit` (10 by
default, at most 100) and `offset`, with `meta.count`, `links` and `data`; versions come highest
first. A version's `download_url` starts with the address the client reached awxnext at, which
`ansible-galaxy` also sends its token to.

### ansible-galaxy

To install from awxnext before Galaxy, list it in `ansible.cfg`:

```ini
[galaxy]
server_list = awxnext, galaxy

[galaxy_server.awxnext]
url = https://awxnext.example.com/api/galaxy/content/published/
token = <a personal token>

[galaxy_server.galaxy]
url = https://galaxy.ansible.com/
```

Or set the same in the environment: `ANSIBLE_GALAXY_SERVER_LIST=awxnext,galaxy`,
`ANSIBLE_GALAXY_SERVER_AWXNEXT_URL` and `ANSIBLE_GALAXY_SERVER_AWXNEXT_TOKEN`. `ansible-galaxy`
asks every server for a collection's versions and takes the highest that matches, whichever
server has it: to make sure a private collection comes from awxnext, give its server in
`requirements.yml`:

```yaml
collections:
  - name: acme.tools
    version: ">=1.0.0,<2.0.0"
    source: https://awxnext.example.com/api/galaxy/content/published/
```

### Publishing a collection

To publish, you need the `CollectionNamespace Upload` or `Admin` role on the namespace, or to
administer its organization, and a personal token with the `write` scope. Build the collection
and publish it to awxnext's server:

```sh
ansible-galaxy collection build acme/tools/
ansible-galaxy collection publish acme-tools-1.0.0.tar.gz --server awxnext
```

`ansible-galaxy` waits for the import and prints its warnings and errors; `--no-wait` returns at
once. Whatever the upload's path names, the version lands in `staging`, or in `published` when
its namespace's `auto_approve` is on, and then [signed](#signing-collections). The namespace
must exist: an upload doesn't create it.

The import runs while the upload is answered, so the first poll finds it finished. Its log
(`messages`, each with its `level` and `time`) gives each check's result, and `error` why it
failed, with a code:

| Check | On failure |
|---|---|
| The artifact's SHA-256 is the one `ansible-galaxy` sent (a warning when none came) | `checksum_mismatch` |
| A gzip'd tarball, at most `HUB_MAX_UPLOAD_SIZE` (100 MiB; `413` above it), 1 GiB once decompressed, 50,000 members | `invalid_artifact` |
| No member outside the collection: no absolute path, no `..`, no link pointing out, no device or FIFO, no member twice | `invalid_artifact` |
| `MANIFEST.json` and `FILES.json` at the root; `MANIFEST.json`'s SHA-256 of `FILES.json`, and `FILES.json`'s of each file, match; every file is listed | `checksum_mismatch`, `invalid_artifact` |
| The metadata: namespace, name, a semantic version, authors, the readme in the artifact, tags, licenses, dependencies as `namespace.name: <range>` (warnings when `requires_ansible` or a license is missing) | `invalid_metadata` |
| The namespace exists and you can see it | `namespace_not_found` |
| You may upload to it | `permission_denied` |
| The version isn't there yet, in any repository: a version is uploaded once | `duplicate_version` |

The tarball is read in memory; nothing of it is written to disk. Imports are also on
`/api/v2/collection_imports/`, for the uploader and the namespace's curators. Failed imports,
and those whose version was deleted, are deleted after 30 days.

### Approving a collection

A namespace's approvers (the `CollectionNamespace Approve` or `Admin` role, the organization's
administrators and its Approval role) approve what waits in `staging`, or reject it, on
`/api/v2/`:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" https://awxnext.example.com/api/v2/collection_versions/42/approve/
curl -X POST -H "Authorization: Bearer $TOKEN" https://awxnext.example.com/api/v2/collection_versions/42/reject/
```

Or with galaxy_ng's move, `POST .../move/staging/published/` (or `.../staging/rejected/`): it
answers `202` with galaxy_ng's task ids, `null`, as the move is done. A version moves between
`staging`, `published` and `rejected`, in any direction: rejecting a published version
withdraws it. Approval [signs](#signing-collections) the version.
`/api/v2/collection_versions/?repository=staging&namespace=<id>` is the queue; each version's
`summary_fields` give its repositories, its import and its signatures, and `reviewed_by` who
last moved it. The next requirements install of a job sees the change: the cache is keyed by the
published versions of its organization's namespaces.

The roles on a namespace, assignable to users and teams:

| Role | Gives |
|---|---|
| `CollectionNamespace Admin` | The owner: change the namespace (`auto_approve` too), upload, approve, deprecate, delete |
| `CollectionNamespace Upload` | Upload, and read `staging` and `rejected` |
| `CollectionNamespace Approve` | Approve and reject, and read `staging` and `rejected` |
| `CollectionNamespace Read` | Read and install what is published |

Upload and Approve are apart so that whoever publishes a collection needn't be who approves it.
An organization's administrators hold all of them on its namespaces, its members only Read.

### Deprecating and deleting

A namespace's owners deprecate a collection, as galaxy_ng does: `PATCH` its path with
`{"deprecated": true}` (`false` to undo), or `/api/v2/collections/<id>/`. A deprecated
collection still installs, without a warning from `ansible-galaxy`: the flag is in its entry for
whoever lists the hub's collections. awxnext answers with the collection, at once, where
galaxy_ng answers with a task.

They also delete a version (`DELETE` its path, or `/api/v2/collection_versions/<id>/`) or a
whole collection, answered `204`. A published version can't be deleted while it is the only one
that meets what needs it: a requirement of a project of the namespace's organization, read from
its requirement files at each sync, or a dependency of another published collection version.
`used_by` (`/api/v2/collection_versions/<id>/used_by/`, `/api/v2/collections/<id>/used_by/`)
lists them, each with the range it asks for, and `effect`: `blocks` when the deletion would
leave it unmet, `none` when another version meets it. Versions in `staging` and `rejected` are
deleted freely.

A collection's `used_by` also lists the execution environments whose image holds it, as their
last contents read reports, with the version they hold and `effect` `none`: deleting the
collection doesn't change an image.

### Module and plugin docs

After each upload's import, and after a sync mirrors a version, awxnext reads the version's
README (the file `galaxy.yml` names, up to 512 KiB) and the docs of its modules, plugins and
roles with `ansible-doc --metadata-dump`, as galaxy-importer does. A background worker on each
node reads one version at a time, the newest first, in a private temporary directory removed
afterwards, and gives up after 10 minutes. Versions uploaded before awxnext read docs are read too.

`ansible-doc` imports some of the collection's Python code, so it runs where jobs run. The
`HUB_COLLECTION_DOCS` setting (Settings, System) chooses where:

| Value | Where `ansible-doc` runs |
|---|---|
| `isolated` (default) | In the organization's default execution environment (`AWXNEXT_DEFAULT_EE_IMAGE` without one), with podman or docker and no network; or, with runtime `none`, as a job user of its own. A node with neither that runs in a Kubernetes cluster, such as the Helm chart's replicas, reads them in a pod of that image. Any other node reads no docs. |
| `server_user` | Also as the server's own user, on a node with none of these. Set it only when you trust uploaders as you trust the server. |
| `off` | Nowhere: no docs are read. |

A docs pod runs as a container group's job pod does: in the node's namespace, created with
the node's service account (the chart's Role allows it), with the shim streaming the artifact in
and the docs out. It's named `collection-docs-<version id>-…`, labelled
`awxnext-collection-docs: "<version id>"`, and deleted once the docs are read or the 10 minutes
are up, `AWXNEXT_KEEP_JOB_PODS` aside. The registry credential of the organization's execution
environment becomes the pod's image pull secret.

The chart's NetworkPolicy `<release>-collection-docs` selects these pods by that label. It lets
nothing connect to them, and lets them connect only to the release's control pods on port 8052,
which their shim streams to, and to the cluster's DNS (`collectionDocs.networkPolicy.dns`,
CoreDNS's `k8s-app: kube-dns` by default). It holds only where the cluster's network plugin
enforces NetworkPolicy, as Calico and Cilium do; on a cluster whose plugin doesn't, the pods
have the network of any pod. NetworkPolicies add up: a policy of your own that selects every pod
of the namespace, as a default-deny policy with egress rules often does, gives the docs pods its
egress too. Leave them out of it with a `matchExpressions` entry
`{key: awxnext-collection-docs, operator: DoesNotExist}` in its `podSelector`. To ship your own
policy for them instead, set `collectionDocs.networkPolicy.enabled: false`.

A version whose docs can't be read still installs; its docs say why. They are on
`/api/v2/collection_versions/<id>/docs/`: `state` (`pending`, `reading`, `read`, `none` when
they weren't read, `failed`), the last read's `message`, the README, and `contents`, the type,
name and short description of each module and plugin. `?type=module&name=greet` gives one's
full docs, as `ansible-doc --metadata-dump` reports them. Approvers read a version's docs again
with `POST /api/v2/collection_versions/<id>/read_docs/`. galaxy_ng's `docs-blob/`
(`.../index/<namespace>/<name>/versions/<version>/docs-blob/`) answers with the same docs, and
the README as escaped text in a `<pre>` rather than rendered HTML.

### The hub in the UI

**Collection hub** in the navigation has these pages:

- **Collections**: the collections you can see, each with its latest version, where it is, and
  its tags. Search by words, or with `namespace:`, `tag:`, `repository:`, `signed:` and
  `deprecated:`; the chips under the search filter by repository and signature, and a tag
  filters by it. A collection's page has these tabs:
  - **Overview**: the README; the `ansible-galaxy` command and the `requirements.yml` entry that
    install the chosen version, by default the newest published one; its details and
    dependencies; and what uses it: the projects whose requirements name it, the collection
    versions that depend on it, and the execution environments whose image holds it.
  - **Docs**: its modules, plugins and roles, and each one's parameters, notes, examples and
    return values.
  - **Versions**: every version you can see; approvers review, sign or publish them from here, and
    owners delete them.
- **Namespaces**: each namespace's collections, its owners, uploaders, approvers and readers,
  its uploads with their import logs, and its roles.
- **Collection approvals**: what waits in `staging`, and what was rejected. A version's review
  page shows its import log and its files compared with the previous version's, or another
  version's, and publishes or rejects it.
- **Remotes**: the remotes and their last sync. A remote's page shows its requirements, the URL
  to install from its repository, and its syncs. System administrators create, change, delete
  and sync remotes.
- **Signing keys**: the keys, the active one first, and the keyring's URLs. System
  administrators rotate the key here.

The comparison is `/api/v2/collection_versions/<id>/diff/`, against the highest version below it
that you can see, or `?against=<id>`: each file `added`, `removed` or `changed`, with a unified
diff of text files. A binary file, a file over 256 KiB, and what comes after 2 MiB of diffs in
all are listed without their diff (`omitted`).

### Signing collections

Approval signs the version's `MANIFEST.json` with awxnext's active signing key: a detached,
armored GPG signature, as galaxy_ng's signing service makes. `ansible-galaxy` checks that
signature, then `MANIFEST.json`'s checksum of `FILES.json`, then `FILES.json`'s checksum of each
file, so one signature covers the whole collection. A version published by an upload to a
namespace with `auto_approve` is signed the same way.

awxnext makes its first key (RSA 3072, user ID `awxnext collection hub`) when it first signs.
The private key is wrapped by the [master key](#master-key), as credentials' secrets are, and
never leaves awxnext: `/api/v2/collection_signing_keys/` shows each key's fingerprint, public key
and state, not its private key.

A version's signatures are in its v3 detail, in galaxy_ng's shape: `signatures`, each with
`signature`, `pubkey_fingerprint`, `signing_service` (`ansible-default` for awxnext's own,
`null` for an uploaded one) and `pulp_created`. They are also on
`/api/v2/collection_versions/<id>/signatures/`, with who signed (`signer`, the key's user ID),
with which key (`pubkey_fingerprint`, and `signing_key` for awxnext's), and who had it signed
(`created_by`: the approver, or the uploader).

#### The public key

Anyone can download the public keys of awxnext's keys, the active one first:

| File | Format | For |
|---|---|---|
| `/api/galaxy/keyring.asc` | Armored | `gpg --import`, a GPG Public Key credential |
| `/api/galaxy/keyring.gpg` | gpg's keyring | `ansible-galaxy --keyring` |

To install and verify with the signature checked:

```sh
curl -fsSO https://awxnext.example.com/api/galaxy/keyring.gpg
ansible-galaxy collection install acme.tools --keyring keyring.gpg --required-valid-signature-count +1
ansible-galaxy collection verify acme.tools --keyring keyring.gpg
```

Or set `gpg_keyring` and `required_valid_signature_count` in the `[galaxy]` section of
`ansible.cfg`. Keep the `+`: without it, a collection that has no signature at all passes.
`install` refuses an artifact whose `MANIFEST.json` the signatures don't match ("Not installing
acme.tools because GnuPG signature verification failed."); `verify` reports a changed
`MANIFEST.json` as a failed signature and a changed file as modified content. `ansible-galaxy`
runs `gpg`, which must be installed; give it a `GNUPGHOME` of its own if `~/.gnupg` isn't
writable. `ansible-galaxy` keeps a version's details, signatures included, a day in its response
cache: after a signature is added or deleted, pass `--clear-response-cache` once.

#### Signing a version by hand

`POST /api/v2/collection_versions/<id>/sign/` signs a published version with the active key, if
that key hasn't: for versions published before awxnext signed, or after a rotation that didn't
sign them. It takes the Approve role, as approval does.

#### Uploading signatures

A namespace's approvers add signatures made elsewhere, by a release team's key for example. Sign
the `MANIFEST.json` of the artifact, as published, and post the signature with the signer's
public key:

```sh
tar -xzf acme-tools-1.0.0.tar.gz MANIFEST.json
gpg --armor --detach-sign --local-user releases@example.com MANIFEST.json
gpg --armor --export releases@example.com > releases.asc
jq -n --rawfile signature MANIFEST.json.asc --rawfile public_key releases.asc '{$signature, $public_key}' |
  curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" --data @-     https://awxnext.example.com/api/v2/collection_versions/42/signatures/
```

awxnext checks the signature against the version's `MANIFEST.json` with that public key, and
records the key's fingerprint and user ID, and who uploaded it. A version takes one signature
per key. A signature with a binary notation is refused: OpenPGP.js and go-crypto add one by
default (a salt), and `ansible-galaxy` can't read gpg's report of it; sign with `gpg`. Approvers
also delete signatures (`DELETE /api/v2/collection_version_signatures/<id>/`), uploaded or not.

The hub's keyrings hold awxnext's keys only: a client counts an uploaded signature when its own
keyring has the signer's key, which you hand out as you choose (each signature's `public_key`
holds it). galaxy_ng's own signature upload, through Pulp, isn't served.

#### Rotating the key

A system administrator makes a new key:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json"   -d '{"name": "awxnext collection hub 2027", "email": "hub@example.com"}'   https://awxnext.example.com/api/v2/collection_signing_keys/
```

The new key becomes the active one and signs approvals from then on. With `sign_published`
(`true` by default) it also signs every published version at once, beside their older
signatures. The old key is retired (`retired` gives when): its signatures are still served, and
its public key stays in the keyrings, so clients that know only the old key go on installing.
Once they have the new one, delete the old key (`DELETE /api/v2/collection_signing_keys/<id>/`):
its signatures go with it. The active key can't be deleted. A key's name and email are set when
it is made; its description can change.

#### Requiring signatures in projects

A project installs its collections only with valid signatures when it has a
`collection_signature_validation_credential`: a GPG Public Key credential, which you need the
Use role on. Paste `keyring.asc` in it, with other signers' keys if you want them to count.
`collection_signature_count` (1 to 10, 1 by default) is how many valid signatures each
collection needs. AWX has no such setting: the two fields are awxnext's, beside AWX's
`signature_validation_credential`, which checks the project's own files (ansible-sign).

Each job's requirements install then runs `ansible-galaxy collection install` with the
credential's keys as its keyring, `--required-valid-signature-count +<count>` and `--no-cache`,
whatever `GALAXY_TASK_ENV` or the project's `ansible.cfg` say, and with a `GNUPGHOME` in the
install's private directory. A collection with fewer valid signatures fails the install, and the
job with it (`Project requirements: installing requirements failed`); the output says which
collection and why. The requirement covers every collection the project installs, from any
Galaxy server: a collection its server serves no signature for fails it. Roles aren't checked. The requirements cache is keyed by the requirement and by the hub's signatures, so an
install cached without the requirement, or before a signature was deleted, doesn't count.

`gpg` must be in the job's execution environment. The all-in-one image and `awx-ee` have it;
add the `gpg` (Debian, Ubuntu) or `gnupg2` (Fedora, RHEL) package to others. An execution node's jobs run its own `gpg` unless they run in a
container.

### ansible-builder

`ansible-builder` installs an execution environment's collections with `ansible-galaxy`, so it
takes the same configuration. To keep the token out of the image, pass it as a build argument of
the stage that installs collections:

```yaml
# execution-environment.yml
version: 3
dependencies:
  galaxy: requirements.yml
additional_build_files:
  - src: ansible.cfg   # server_list and url as above, without the token
    dest: configs
additional_build_steps:
  prepend_galaxy:
    - COPY _build/configs/ansible.cfg /etc/ansible/ansible.cfg
    - ARG ANSIBLE_GALAXY_SERVER_AWXNEXT_TOKEN
```

```sh
ansible-builder build --tag registry.example.com/acme-ee:1.0 \
  --build-arg ANSIBLE_GALAXY_SERVER_AWXNEXT_TOKEN="$TOKEN"
```

To build only with signed collections, give `ansible-builder` the hub's keyring and the count, as
for `ansible-galaxy`; the base image needs `gpg`:

```sh
curl -fsSO https://awxnext.example.com/api/galaxy/keyring.gpg
ansible-builder build --tag registry.example.com/acme-ee:1.0 \
  --galaxy-keyring keyring.gpg --galaxy-required-valid-signature-count +1 \
  --build-arg ANSIBLE_GALAXY_SERVER_AWXNEXT_TOKEN="$TOKEN"
```

awxnext builds no images: run `ansible-builder` where you build the others.

### Jobs

A job installs its project's `collections/requirements.yml` from its organization's Galaxy
credentials, in order. To install from the hub, add the managed **Collection Hub** credential to
the organization's Galaxy credentials (`POST /api/v2/organizations/<id>/galaxy_credentials/`).
It holds no token: each install gets a read token of its own, for the namespaces of the
project's organization, deleted when the install ends. Should that fail, it expires 5 minutes
after the install's timeout (`DEFAULT_PROJECT_UPDATE_TIMEOUT`, an hour without one). An install
the requirements cache answers gets no token. With the Collection Hub credential, the cache's
key includes the published versions of the organization's namespaces, the repositories of its
[remotes](#remotes), and their signatures: an approval, a rejection, a deletion, a sync or a
signature makes the next job install again. A project can
[require signatures](#requiring-signatures-in-projects).

The install reaches awxnext where it runs:

| Where the job runs | The hub's address |
|---|---|
| A process on awxnext's node | `AWXNEXT_INTERNAL_URL`, else the listen address on loopback |
| A container on awxnext's node, or a container group's pod | `AWXNEXT_SERVICE_URL`, else `AWXNEXT_BASE_URL` |
| An execution node | `AWXNEXT_BASE_URL`, which its agent connects to |

### Remotes

A remote mirrors collections of another Galaxy server into a repository of its own, so that
jobs and `ansible-builder` install them from awxnext, without reaching the internet, and the
same versions each time, as Private Automation Hub's remotes do. The server is
galaxy.ansible.com, Red Hat's Automation Hub, or another galaxy_ng. Each sync is a job, with
its output and its status, that you start, schedule and notify on as a project update.

System administrators create remotes, at `/api/v2/collection_remotes/`:

| Field | What it holds |
|---|---|
| `organization` | The organization whose members and jobs read the remote's repository, and that owns the namespaces its syncs create. |
| `credential` | An **Ansible Galaxy/Automation Hub API Token** credential of the server: see below. |
| `requirements_file` | The collections to mirror, as a `requirements.yml` lists them. |
| `sync_dependencies` | Mirror their dependencies too: `true` by default. |
| `signature_validation_credential` | A **GPG Public Key** credential: then syncs keep only the signatures its keys verify, and only the versions that have one. See [Signatures](#signatures). |
| `proxy_url` | The proxy syncs go through. Empty: the server's `HTTPS_PROXY`, if any. |
| `tls_validation` | Check the server's TLS certificate: `true` by default. |
| `repository` | The repository the remote syncs into, set when you create it: lowercase letters, digits, `-` and `_`. Empty: one from the remote's name. |

`galaxy_credential` is the remote's managed credential, `Collection Hub: <repository>`, which
awxnext creates with the remote, in its organization, and deletes with it.

#### The server

The remote's credential gives the server's URL, and its token when the server needs one:

| Server | Galaxy Server URL | Auth Server URL | API Token |
|---|---|---|---|
| galaxy.ansible.com | `https://galaxy.ansible.com/` | | |
| Red Hat's Automation Hub, certified content | `https://console.redhat.com/api/automation-hub/content/published/` | `https://sso.redhat.com/auth/realms/redhat-external/protocol/openid-connect/token` | Your offline token |
| Red Hat's Automation Hub, validated content | `https://console.redhat.com/api/automation-hub/content/validated/` | as above | as above |
| Another galaxy_ng, or Private Automation Hub | Its repository's URL, such as `https://hub.example.com/api/galaxy/content/published/` | | Its API token |

To get Red Hat's offline token, sign in to console.redhat.com and load the token on the
Automation Hub's **Connect to Hub** page (`https://console.redhat.com/ansible/automation-hub/token`).
Each sync exchanges it at the SSO server for access tokens, as `ansible-galaxy` does, and gets
another when one expires. An offline token that isn't used for 30 days expires: a scheduled
sync keeps it in use.

awxnext finds the server's API as `ansible-galaxy` does: the URL, else the URL with `api/`
appended. The token goes to the server's own host only: a download that redirects elsewhere,
to object storage for example, goes there without it.

#### The requirements file

The remote's `requirements_file` lists collections as a `requirements.yml` does, each a name, or
a name and a version range:

```yaml
collections:
  - name: community.docker
    version: ">=4.8.0,<5.0.0"
  - name: ansible.posix
    version: "==2.1.0"
  - kubernetes.core
```

A sync mirrors every version in each collection's range: a name alone mirrors all of them.
Pre-releases come only when the range names them (`==2.1.0-beta.1`). With `sync_dependencies`,
each mirrored version's dependencies come too, at the highest version in the range it asks for
that passes the [signature check](#signatures), and theirs. Collections only: the file refuses
`roles:`, a `source:` (make a remote for that server), and a `type:` other than `galaxy`. Quote
a version (`"1.0"`): YAML reads `1.0` as a number.

#### Syncing

To sync, `POST /api/v2/collection_remotes/<id>/sync/`: awxnext answers `202` with the new
`collection_sync` job, at `/api/v2/collection_syncs/<id>/`, with its output at `stdout/` and its
events at `events/`. Creating a remote doesn't sync it. Schedules (`schedules/`) and
notification templates (`notification_templates_started/`, `_success/`, `_error/`) attach to
the remote as to a project.

A sync adds what is new, and a version the server no longer has stays: only the
[signature check](#signatures) takes a version out of the repository. A version is mirrored once: when the hub has it already, in any repository, with the
same SHA-256, the sync adds it to the repository; with another SHA-256, the hub keeps its own,
and the sync fails and says so. A version waiting in `staging`, or rejected, makes the sync fail
until an approver decides. A collection the server doesn't have, or no version of it in the
range, fails the sync too, after the rest is mirrored, and so does a range in which the
[signature check](#signatures) skips every version.

Each artifact is checked as an upload is (its checksums, its size up to `HUB_MAX_UPLOAD_SIZE`,
its members). Namespaces the hub doesn't have are created in the remote's organization; versions
of a namespace the hub has go into that one.

#### Signatures

A sync mirrors each version's signatures, as the server gives them in its v3 detail, with the
origin `remote`. Red Hat signs its certified content. Without `signature_validation_credential`
they are kept unchecked, each with the fingerprint of the key it names, for clients to check
with their own keyring. With it, only the signatures its keys verify are kept, and a version
with none is skipped: the sync's output says so on the version's line, and lists every skipped
version at the end. A skipped version doesn't fail the sync while its range has another one:

- For a requirement, another version in its range that the sync mirrors, or that the
  repository already holds, meets it.
- For a dependency, the sync tries the next lower version in its range, down to the lowest.

A requirement or a dependency left with no version in its range that passed the check fails the
sync, after the rest is mirrored: the job explanation names it as unmet.

A sync with `signature_validation_credential` also checks the versions already in the
repository, as it checks new ones, before it judges any requirement. Setting the credential, or
changing its keys, is enough to hold what earlier syncs mirrored to them:

- A version loses the signatures the keys don't verify.
- A version left with none leaves the repository. The output's first lines say so, and it is
  listed with the skipped versions. When no other repository holds it, the hub deletes the
  version and its artifact.

A version that another repository holds too keeps all its signatures, even when it leaves the
remote's repository. Signatures belong to the version, so one these keys don't verify can be
the one that the other repository's clients check, such as awxnext's own in `published`.

Versions in a remote's repository aren't signed by awxnext's own key: to install them with
`--keyring`, give the keyring of their signers.

#### Installing from a remote's repository

For an organization's jobs to install from the remote, add its managed credential, the
remote's `galaxy_credential`, to the organization's Galaxy credentials
(`POST /api/v2/organizations/<id>/galaxy_credentials/` with `{"id": <credential>}`), before
Galaxy's or in its place. As with the **Collection Hub** credential, each install gets a read token of its own. Members of
the organization install with `ansible-galaxy` from `/api/galaxy/content/<repository>/` with
their personal token, as from `published`.

#### Proxies and local addresses

A sync reaches the server from awxnext's node, through the remote's `proxy_url`, else the
node's `HTTPS_PROXY`. It refuses loopback, link-local and unspecified addresses and cloud
metadata services, the server's and the proxy's: a server or a proxy on awxnext's own host
needs an entry in `HUB_REMOTE_ALLOWED_LOCAL_ADDRESSES` (Settings, System): an address
(`127.0.0.1`), a network (`127.0.0.0/8`) or an address and port (`127.0.0.1:8080`).

#### Without internet access

To bring collections to an awxnext that can't reach the server, download them on a machine that
can, with their dependencies, and import the directory:

```sh
ansible-galaxy collection download -r requirements.yml -p collections/
# copy collections/ to the awxnext node, then:
awxnext hub import --organization Default collections/
```

`awxnext hub import` reads the server's configuration as `awxnext serve` does
(`AWXNEXT_DATABASE_URL`, `AWXNEXT_SECRET_KEY`, the storage settings). It imports the artifacts
the directory's `requirements.yml` lists, as `ansible-galaxy collection download` writes it, or
each `*.tar.gz` without one, and prints a line for each:

| Option | What it does |
|---|---|
| `--repository` | The repository to import into: `published` (the default), `staging`, or a remote's. Into `published`, awxnext signs each version as an approval does. |
| `--organization` | The organization that owns the namespaces the import creates. Without it, an artifact of a namespace the hub doesn't have fails. |
| `--keyring` | A file of public keys, armored or binary: then only the signatures they verify are kept, and an artifact without one is skipped. |

`ansible-galaxy collection download` writes no signatures. To import some, put each artifact's
signatures, armored, in a file beside it named after it with `.asc`
(`community-docker-4.8.1.tar.gz.asc`), or list them in the artifact's `signatures:` in
`requirements.yml`, as files of the directory (`file://<name>`). A version the hub has already,
with the same SHA-256, gets the signatures it lacks; with another, it fails. The command exits
with `1` when an artifact fails, after importing the others.

### Storage

Collection artifacts are kept in the database by default. To keep them in object storage, set
the `HUB_STORAGE_*` settings (Settings, System, or `/api/v2/settings/system/`), as the
[job event archives'](#job-event-archives): `HUB_STORAGE` (`database`, `s3`, `azure_blob`), the
bucket or container, the endpoint, the region and the credential. Objects are named
`<prefix><INSTALL_UUID>/<sha256>/<namespace>-<name>-<version>.tar.gz` (`HUB_STORAGE_PREFIX`,
`collections/` by default). Artifacts stored before a change stay where they are; a deleted
version's object is deleted at the next maintenance (every 10 minutes). A backup holds the
database only: keep the bucket's own backups or versioning.

## Self-service portal

The self-service portal is for people who request automation rather than run it (roadmap M15). A
catalog item publishes a job or workflow template to people with no role on the template: they
fill in the item's form, and awxnext runs the template for them.

### In the admin UI

**Portal** in the navigation has these pages:

- **Catalog items:** the items you can see, filtered by kind, category, template or organization.
  An item's page has its details, its requests, its notifications and its access. **Open in the
  portal** shows the item as its requesters see it.
- **Requests:** the requests for the items you administer, and your own, filtered by item, status,
  kind, team or requester. Each links to its page in the portal and to its job.
- **Deployments:** the deployments of the items you administer, and your own, filtered by item,
  status, owner or team.
- **Catalog quotas:** what each team uses of the items' [quotas](#quotas).
- **Open the portal:** the portal itself ([The portal](#the-portal)).

To publish a template, select **Publish to the portal** on the template's page, or **Publish a
template** in **Catalog items**. Both show only to those with Admin on a template. The form has a
section for each part of the item:

- **The item:** its name, template, description, kind, category, icon and owner team.
- **Questions:** for each question of the template's survey, whether requesters answer it, don't
  see it, or get a fixed answer, which you can show them; its wording, default and choices, and a
  dynamic list's filter ([The item's form](#the-items-form)).
- **Approvals:** the [rules](#approvals), the freeze exception team and the email notification.
- **The deployment,** for an item that creates one: the deployment's name, its teardown, its
  lifetime, [how to connect](#how-to-connect) and its [actions](#day-2-actions). **Kept until
  deleted** keeps each deployment until its owners delete it. **Expires** takes a default or a
  longest lifetime, and the extensions owners can ask for ([Lifetime](#lifetime)).
- **Quotas** and **Step labels.**

When the API refuses the item, each error shows on its field or section, and the form scrolls to
the first one. On the item's **Notifications** tab, **Request changes** attaches the
[request webhooks](#requests-from-another-catalog) and **Expiry reminders** the
[reminders](#lifetime).

### Catalog items

To publish a template, post to `/api/v2/catalog_items/` with these fields:

| Field | What it sets |
|---|---|
| `unified_job_template` | The job or workflow template the item publishes. It must belong to an organization, which becomes the item's. Publishing takes the right to change the template: `JobTemplate Admin`, `WorkflowJobTemplate Admin` or `Organization Admin`; `Execute` isn't enough. A template can have several items. |
| `name`, `description` | The item's name, unique in its organization, and its description. |
| `kind` | `run_once` for a request that runs once, such as a firewall rule; `deployment` for one that creates a deployment that its owners keep. |
| `icon`, `category` | The icon the portal shows, by name (`server`), and the category it lists the item under. |
| `owner_team` | The team of the organization that owns the item. |
| `questions` | What the item makes of the template's survey ([The item's form](#the-items-form)). |
| `max_lifetime_days`, `default_lifetime_days` | For a `deployment` item, the longest a requester can keep the deployment and the lifetime the form proposes, in days. With neither, a deployment is kept until its owners delete it ([Lifetime](#lifetime)). |
| `deployment_name`, `connection`, `actions`, `teardown_template` | For a `deployment` item, what its deployments are named, how to connect to them, their day-2 actions and what deleting one runs ([Deployments](#deployments)). |
| `max_deployments`, `max_requests_per_day`, `over_quota` | The deployments each team can keep at once, the requests it can make in 24 hours, and whether a request over quota is refused or waits for approval ([Quotas](#quotas)). |
| `extension_days`, `max_extensions`, `expiry_waits_for_freeze` | For a `deployment` item, the extensions its deployments' owners can ask for, how many, and whether an expiry waits for a freeze's end ([Lifetime](#lifetime)). |
| `step_labels` | The portal's names for the template's plays, or a workflow's nodes, by their name: `{"Create the VM": "Creating your VM"}` ([The portal](#the-portal)). |

The item's creator gets `CatalogItem Admin` on it. These roles give access to items:

| Role | What it gives |
|---|---|
| `CatalogItem Admin` | Changes and deletes the item, and sees its requests. Changing what runs, the template or the questions, also takes the right to change the template. |
| `CatalogItem Requester` | Requests the item. It gives no role on the template, its inventory, its project or its credentials. |
| `CatalogItem Read` | Sees the item. |
| `Organization CatalogItem Requester` | Requests every item of the organization. |

An item has a version history (`/history/`), readable by those who can change the item, since it
holds the item's fixed answers. The template's `used_by/` lists its items. When the template moves
to another organization, its items move with it; deleting the template deletes them. GitOps declares items as `CatalogItem` ([GitOps](gitops.md#declarations)).

### The item's form

An item's form is a view over the template's survey. It adds no variable, so a request sets only
what the survey asks. `questions` maps survey variables to what the item changes about each:

| Key | What it does |
|---|---|
| `question_name`, `question_description` | Rewords the question. |
| `choices` | Narrows a `multiplechoice` or `multiselect` question to some of its choices. |
| `filter` | Narrows a dynamic list (`choices_from`) with an expression that keeps a choice when true. It reads `value` (the choice), `answers` (the answers so far, by variable) and `user` (the requester's `id`, `username` and `teams`, by name): `value startsWith "web"`. |
| `default` | Sets the question's default. |
| `value` | Fixes the answer: the form doesn't ask the question, and a request that answers it is refused. A password answer is stored encrypted and read back as `$encrypted$`; send `$encrypted$` to keep it. |
| `visible` | With `value`, shows the fixed answer to requesters. A password answer is never shown. |
| `hidden` | Leaves the question to its default. A required question needs a default or a `value`. |

```json
{
  "env": {"question_name": "Stage", "choices": ["dev", "staging"]},
  "token": {"value": "s3cret"},
  "owner": {"value": "platform", "visible": true},
  "target": {"filter": "value != \"db1\""}
}
```

Only those who can change the item see its `questions`. When the template's survey changes so that
the item no longer matches it, a request answers `400` until the item's admins update it.

### Requests

`GET /api/v2/catalog_items/N/request/` returns the form as the requester sees it: the questions
it asks, with the item's changes; the fixed answers it shows; the requester's teams in the item's
organization (`teams`, and `team_required` when they're in several); for a `deployment` item, its
lifetime; and, when the item has [quotas](#quotas), what each of those teams uses of them. `catalog_items/N/survey_choices/?variable=x` lists the choices of a dynamic
question, narrowed by the item's filter, as the item's publisher set them: the requester needs no
role on the inventory or project they come from. `answers` (a JSON object) gives the filter the
answers so far.

To request the item, post to the same URL:

| Field | What it sets |
|---|---|
| `answers` | The answers to the questions the form asks, by variable. An answer to a fixed or hidden question, or to a variable the form doesn't ask, is refused. |
| `team` | The team the request is for: one of the requester's teams in the item's organization. Required when they're in several; with one, that team; with none, no team. |
| `reason` | Why the requester needs it. |
| `lifetime_days` | For a `deployment` item, how long to keep the deployment, up to `max_lifetime_days`. Empty: the item's `default_lifetime_days`. An item that runs once refuses it. |

awxnext then runs the template with the item's answers, the requester as the launcher, and
answers `201` with the request (`/api/v2/catalog_requests/N/`). The request records the item, the
team, the reason, the lifetime, the answers the requester gave (passwords masked), the label
each answer was shown with when it differs from the answer (`answer_labels`, for lists from a
[choice source](surveys.md#choice-sources)), the fixed answers they were shown, its job, and a `status` that follows the job's: `pending`, `running`,
`successful`, `failed` or `canceled`. Its `kind` is `new`; requests on a
[deployment](#deployments) have the deployment's id in `deployment`, and the kind of an action,
`extend` or `delete`.

The requester sees their requests, not the jobs that run them: a request's jobs, the workflow's
nodes included, carry the item's hidden answers, and stay visible to those with a role on the
template. In the requester's activity stream, the job's entry shows only what they answered or
were shown. An item's admins and the organization's auditors see its requests in
`/api/v2/catalog_items/N/requests/` and `/api/v2/catalog_requests/`.

### Approvals

Some requests should wait for someone: a large VM, anything in production. An item's
`approval_rules` hold a request until its approvers approve it. Each rule is an object:

| Key | What it sets |
|---|---|
| `name` | The rule's name, unique in the item. Approvers see it. |
| `condition` | An expression that holds the request when true ([Conditions](#conditions)). Empty: every request. |
| `approvers` | Who approves: `team_admins`, the Team Admins of the team the request is for; `team`, the members of `team`; or `users`, the users that `users_from` names. |
| `team` | With `approvers: team`, a team of the item's organization. |
| `users_from` | With `approvers: users`, an expression that gives usernames or email addresses, matched to awxnext's active users regardless of case: `item("group").owners` for the owners of the group that the requester picked. |
| `required` | How many approvals the rule needs, from 1 (the default) to 20. When the group is smaller, every member must approve. |
| `timeout` | How long the rule waits, in seconds: 7 days by default, at most 90 days, `0` for no limit. A timeout denies the request. |

```json
"approval_rules": [
  {"name": "Large VMs", "condition": "size == \"large\"", "approvers": "team", "team": 12, "required": 2},
  {"name": "Production", "condition": "env == \"prod\"", "approvers": "team_admins"}
]
```

A request that no rule holds runs at once. Otherwise its `status` is `awaiting_approval`, and
each rule whose condition holds gets its own workflow approval, as an event rule's
[approval mode](#approval-mode) does, so the approvals page, its count in the navigation, `/api/v2/workflow_approvals/` and the organization's
approval notifications serve it. The rules wait in parallel: when every one approves, awxnext
runs the request as the requester, who must still be able to request the item (for a request on
a deployment, still own it), and the status follows the job's. The first deny or timeout ends the request: its `status` is `denied`, its
`explanation` says who denied it and why, and its other approvals are canceled. A request that
is approved but can't run, because its item was deleted or its requester lost access, is `failed`
with the reason.

The request's `approvals` lists each approval with its rule, its approvers, the approvals it
needs, its answers and when it expires. An approval's `summary_fields` has the request
(`catalog_request`) and the same details (`request_approval`).

#### Conditions

A condition is an [expr](https://expr-lang.org/docs/language-definition) expression. It reads:

- each answer by its variable, as the job gets it: `size > 4`, `env == "prod"`. An unanswered
  question reads as its type's zero value: `0`, `""` or an empty list. Password answers aren't
  readable. The variables are those of the template's survey and of the item's actions'
  surveys: a request for an action answers its own survey.
- `answers`, the same answers by variable, with `nil` for unanswered questions.
- `item("x")`, the item picked in list `x`, as [computed questions](surveys.md#computed-questions-value_from)
  read it.
- `team`, the team the request is for (`id`, `name`), or `nil`.
- `kind`, the kind of request: `new`, the id of a deployment's [action](#day-2-actions),
  `extend` or `delete`. A rule without a condition holds every kind: to hold only new requests,
  use `kind == "new"`.
- `user`, the requester (`id`, `username`, `email`, `teams`), and `request` (`id`,
  `lifetime_days`, `expires_at`). For an [extension](#lifetime), `request.lifetime_days` is the
  days it adds and `request.expires_at` the expiry it gives.

awxnext compiles the conditions when the item is saved. When the template's survey changes so that
a condition no longer compiles or fails at run time, requests answer `400` until the item's
admins update the rule.

#### Who approves

Nobody approves their own request, Team Admins and superusers included: awxnext takes the requester
out of each rule's group. When nobody is left, the organization's admins approve instead (the
superusers, if the organization has none), and the approval's `fallback` is `true`.

Each approver answers once, at `/api/v2/workflow_approvals/N/approve/` or `deny/`, with an
optional `comment`. A deny needs a comment. An approval is decided by its first deny, or once it
has the approvals it needs. The organization's admins can answer any waiting request of their
organization, even when they aren't among its approvers: their answer decides the approval at
once. Each answer is recorded in the request's activity stream, with `as_admin` for an admin's.
`can_approve_or_deny` is `false` for the requester and for those who have answered.

#### Freezes

When a [maintenance window or change freeze](#maintenance-windows-and-change-freezes) that lists
the item is going on, awxnext refuses its requests with `400`, the window's name and its end
(`window.until`). If the item's `freeze_exception_team` is set, a request waits for an exception
instead: one approval by a member of that team, with the organization's admins as the fallback,
which times out when the window ends. Freezes don't apply to a deployment's
[extensions](#lifetime), which change nothing that they protect.

#### Emails

To mail the people concerned, set the item's `notification_template` to an email notification
template of its organization. awxnext uses its server and sender, but not its recipients: it mails
each rule's approvers when a request waits for them, and the requester when the request is
approved, denied, or approved but unable to run. Approvers without an email address get no
mail. To notify a fixed list as well, attach notification templates to the organization's
`notification_templates_approvals`: they are sent as for any workflow approval.

The mails link to the [portal](#the-portal): an approver's to the approval
(`/portal/approvals/N`), the requester's to the request (`/portal/requests/N`). So do the
approval notifications of a catalog request. A link only opens the page, and answering takes a
click there: mail scanners that open links decide nothing.

### Quotas

An item's quotas limit what each team uses of it: `max_deployments`, how many of its deployments
a team keeps at once, and `max_requests_per_day`, how many requests it makes in 24 hours. A
request for no team counts for its requester alone. Empty: no limit.

- **Deployments:** a deployment counts until it's deleted, including when its teardown failed
  (`delete_failed`). A new request counts as well while it waits or runs. A failed provisioning
  creates no deployment, so its place is free again.
- **Requests:** the item's new requests of the last 24 hours count, except the denied ones. Day-2
  actions, extensions and deletions don't count.

Over quota, awxnext refuses the request with `400`, the reason and the usage (`quota`). A request
over its day's quota also says when the next one fits. With the item's `over_quota` set to
`approve`, the request waits instead for one approval by a member of the item's `owner_team`, or
the organization's admins without one, under the rule `Over quota`, with `kind` `quota`. An
approved request goes past the quota, and its deployment counts until it's deleted.

The request form (`GET catalog_items/N/request/`) gives the usage in `quota`: the item's
`max_deployments`, `max_requests_per_day` and `over_quota`, and in `usage`, the `deployments` and
`requests` of each of the requester's teams in the item's organization, or of the requester when
they have none. The item's admins see every team's and user's usage at
`GET /api/v2/catalog_items/N/quota_usage/`, and in the UI under **Portal > Catalog quotas**.

### Deployments

When a request for a `deployment` item succeeds, awxnext records what it provisioned, such as a
VM or a database, as a deployment (`/api/v2/deployments/N/`). Its owners are the requester
(`owner`) and the team the request was for (`team`): each of them sees it, runs its actions,
shows its secrets, hands it over and deletes it. The item's admins and the organization's
auditors see it too; the item's admins also hand it over and delete it, but don't see its
secrets.

| Field | What it holds |
|---|---|
| `name`, `description` | Its name, from the item's `deployment_name`, and a description. Its owners can change both. |
| `catalog_item`, `item_name`, `request` | The item, its name when it was requested, and the request that created it. |
| `owner`, `team` | Its owners. |
| `status` | `active`, `deleting`, `delete_failed` or `deleted`. |
| `outputs` | The outputs of its runs, by name. Secret outputs show as `$encrypted$`. |
| `connection` | How to connect to it ([How to connect](#how-to-connect)). |
| `expires_at`, `extensions` | When it expires, counted from the request by its `lifetime_days`, and how many times it was extended ([Lifetime](#lifetime)). Empty `expires_at`: it is kept for good. |
| `explanation`, `left_behind`, `deleted_at` | Why its teardown failed, what the teardown left, and when it was deleted. |

The outputs are those that the template [declares](workflows.md#typed-outputs) and its playbook
publishes with `set_stats`; for a workflow template, those of its jobs that succeeded, the later
ones winning. Artifacts that no template declares aren't kept, and
[secret outputs](workflows.md#secret-outputs) stay encrypted. `deployments/N/requests/` lists
the requests on the deployment, with their `kind`: the one that created it, its actions and its
deletions. Deployments record no cost.

An item's `deployment_name` is an expression over `answers` (as the request records them),
`outputs`, `request` (`id`) and `item` (the item's name): `answers.name + "-db"`. Empty, or when
it gives nothing, the name is the item's name and the request's id.

#### How to connect

An item's `connection` lays out how to connect to its deployments, in order. Each entry is an
object:

| Key | What it sets |
|---|---|
| `label` | What the entry shows, unique in the item. |
| `value` | An [expr](https://expr-lang.org/docs/language-definition) expression that reads each output by name (`host`), all of them as `outputs` (`outputs.host`), and the deployment's `id` and `name` as `deployment`. |
| `copy` | `false` to show the value without a copy button. The default is `true`. |

```json
"connection": [
  {"label": "Host", "value": "host"},
  {"label": "Port", "value": "port", "copy": false},
  {"label": "Password", "value": "db_password"},
  {"label": "Connection string", "value": "\"postgresql://\" + db_user + \":\" + db_password + \"@\" + host + \":\" + string(port) + \"/app\""}
]
```

Without a layout, every output shows under its name, in alphabetical order. An entry that reads a
secret output is secret: the deployment's `connection` shows it as `$encrypted$`, with `secret:
true`. To show it, an owner posts its label to `deployments/N/reveal/` (`{"label": "Password"}`),
which answers with its `value`. Each showing is recorded in the deployment's activity stream, as
`revealed` with the label, and in the server log, never with the value. An entry whose expression
fails shows the `error` instead of a value.

#### Day-2 actions

An item's `actions` are what its deployments' owners can run on them after they're made, such as
a restart, a snapshot or a resize. Each action is an object:

| Key | What it sets |
|---|---|
| `id` | Lowercase letters, digits and `_`, starting with a letter, unique in the item: the `kind` of the action's requests. `new`, `extend` and `delete` are taken. |
| `name`, `description` | What the portal shows. |
| `template` | The job or workflow template the action runs, of the item's organization. As for the item's template, publishing it takes the right to change it. |

`GET deployments/N/actions/` lists the actions, with the questions of their templates' surveys. To
run one, post `action` (its id), `answers` and `reason` to the same URL: awxnext answers `201`
with the request. The template runs as the owner who asked, with the deployment's outputs as
variables, secret ones passed as survey passwords are, and the answers over them. Ansible's own
variables (`ansible_*`) among the outputs aren't passed. The request goes through the item's
[rules](#approvals) and freezes as any request does; its `kind` is the action's id, so a rule can
hold some actions only: `kind == "resize" && size == "large"`. When the action succeeds, the
outputs its template declares and its playbook publishes replace the deployment's, such as a
rotated password.

A deployment takes one request at a time: while a request on it waits or runs, another one is
refused with `400`.

#### Handing over

To give a deployment to someone else, post `user`, `team` or both to `deployments/N/hand_over/`.
`user` alone changes the owner and keeps the team; `team` alone gives it to the team, with no
owning user. Whoever gets it must be able to request the item: a requester role on the item or
its organization, held by the team or by a team it is in. With the item's `notification_template`
set, the new owner, or the team's members, are mailed a link to it. The activity stream records
the hand-over. When the item has a deployment [quota](#quotas), whoever gets the deployment needs
room in it: the item's admins can hand over past it, its owners can't.

`GET deployments/N/hand_over/` lists who can get it: `users`, the people who hold a requester role
on the item or its organization, apart from its owner (`id`, `username`, `name`), and `teams`, the
organization's teams that can request it (`id`, `name`). Its owners and the item's admins can read
it.

#### Deleting

`DELETE /api/v2/deployments/N/` asks for the deployment's deletion and answers `202`. The
deletion is a request of kind `delete`, which goes through the item's rules and freezes. It runs
the item's `teardown_template` with the deployment's outputs, and the deployment's `status` is
`deleting`; without a teardown template, the deployment is deleted at once.

When the teardown succeeds, the status is `deleted`. When it fails or is canceled, the status is
`delete_failed`, with the job's explanation and what the teardown published as `left_behind` (with
`set_stats`, such as a list of what's left), and the owners are mailed. To try again, delete it
again. A deleted deployment stays listed, with `deleted_at`. When the item was deleted, there's no
teardown to run: deleting the deployment marks it deleted at once.

#### Lifetime

A requester chooses how long to keep a deployment, up to the item's `max_lifetime_days`; its
`expires_at` counts from the request, as the request's `expires_at` does. A request that gives no
`lifetime_days` gets the item's `default_lifetime_days`. When the item has neither lifetime, such
a request's deployment is kept until its owners delete it, with no `expires_at`; when the item has
only `max_lifetime_days`, the request must give one. An item's
`extension_days` lists the extensions its owners can ask for, in days, such as `[1, 3, 7]`. Its
caps are `max_extensions`, how many extensions a deployment gets (empty: no limit), and
`max_lifetime_days`, which holds for the whole lifetime, extensions included.

`GET deployments/N/extend/` lists each extension with the expiry it gives, why it's refused
(`refused`), or the approvals it would wait for (`approvals`: the rule, its approvers and the
approvals it needs), with the deployment's `extensions` and the latest it can be kept (`until`).
While a request on the deployment waits or runs, every extension is refused until it finishes.
To extend, an owner posts `days` and an optional `reason` to the same URL; awxnext answers
`201` with the request, of kind `extend`. An extension goes through the item's
[rules](#approvals): one that no rule holds applies at once, and one that a rule holds waits for
its approvals. To have extensions longer than 3 days approved, add a rule such as:

```json
{"name": "Long extensions", "condition": "kind == \"extend\" && request.lifetime_days > 3", "approvers": "team_admins"}
```

An extension adds its days to the expiry. An extension past a cap is refused with `400`, and one
that was approved meanwhile fails with the reason. Past the caps, the item's admins set
`expires_at` on the deployment themselves; its owners can't.

Reminders go out 24 hours and 2 hours before expiry, once per expiry: an extension or a new
`expires_at` sends them again. The owners are mailed at their own addresses through the item's
`notification_template`, with a link to the deployment. Each notification template in the item's
`notification_templates_reminders` gets the reminders too: an email template at its own
recipients, a webhook template as JSON, with `subject`, `body`, `hours_left` and the
`deployment` (`id`, `name`, `url`, `expires_at`, `catalog_item`, `item_name`, `owner`, `team`).
The item's [request webhooks](#requests-from-another-catalog) get them as `expiring` events.
Slack notifications come with roadmap M11-02.

At expiry, awxnext requests the deployment's deletion itself, with the reason `It expired.` and no
requester. The item's rules don't apply, so the teardown runs at once, inside a maintenance window
or change freeze too. To have it wait for the window's end, set the item's
`expiry_waits_for_freeze`. An extension that still waits for approval is canceled; a request on
the deployment that runs is waited for. When the item was deleted, no teardown can run: the
expired deployment is `delete_failed`, for its admins to delete once what it provisioned is gone.

### The portal

The portal, at `/portal` in the UI, is where requesters find items, request them and follow
their requests. It has its own top bar, without the admin navigation, and fits a phone. A user
whose roles all come with requesting, such as `CatalogItem Requester`, `Organization CatalogItem
Requester`, `Organization Member` or `Team Member`, lands there after signing in, and the admin
pages send them back to it. The others open it from the account menu, and go back through
**Admin view**. `GET /api/v2/portal/` tells the UI which case applies (`request_only`).

The portal's home lists the catalog by category, with a search, and what needs the user
(`needs_you` in `GET /api/v2/portal/`):

- `requests`: their requests that wait or run;
- `deployments`: the deployments they own, or their teams own, that expire within 3 days;
- `approvals`: how many requests wait for their approval.

An item in a maintenance window or change freeze shows it, and when it ends, from its
`summary_fields.freeze` (`id`, `name`, `kind`, `until`).

To brand the portal, set the organization's **Portal title** and **Portal logo** in its form. In
the API, they're `portal_title`, at most 128 characters, and `portal_logo`, an image as a `data:`
URL (`data:image/png;base64,...`) in PNG, JPEG, WebP or SVG of at most 64 KB. The portal takes them from the first organization, by id, that the user sees
and that sets one; without one, it shows awxnext's.

The request form shows, as the requester answers, the approvals the request will need and why,
the quota left and when the deployment expires. It gets them from
`POST catalog_items/N/preview/`, which takes the body of a request and runs the same checks
without creating it. It answers `approvals` (each rule that holds, with its condition, its
approvers by name, how many approvals it needs and its timeout), `refused` (why the request would
be refused, from a freeze or a quota) and `quota` (the usage). Invalid answers get `400`, as for a
request.

**My requests** shows each request's steps, sent, approval, running and done, and what happens
next: who it waits for and until when, who denied it and their comment, or that it failed, with a
link to request it again with the same answers. `GET catalog_requests/N/progress/` gives the run's
progress:

| Field | What it holds |
|---|---|
| `steps` | The plays of the job, as its events start them, then the plays of the last successful run of the template that haven't started yet. For a workflow, its nodes. Each has its `name`, its `label` from the item's `step_labels` (the name without one), and its `state`: `done`, `running`, `pending`, `failed` or `canceled`. |
| `percent` | `100` once the request succeeded. While it runs, the tasks started so far against the task count of the template's last successful run, at most `99`. Empty without such a run. |
| `job` | The job's `id`, `type` and `status`, and `can_see`: whether the user can see the job. The portal links to the job only then. |

Steps follow plays because Ansible tags don't reach job events. To show a step per stage, give
each stage its own play, and reword the plays in the item's `step_labels`.

**My deployments** lists the [deployments](#deployments) that the user or their teams own, apart
from deleted ones, with when each expires, or that it's being deleted or couldn't be. A
deployment's page shows:

- its lifetime, a cell per day: gone, today, left, and the days that extensions added, at most
  60, the oldest going first. Below it, the item's extensions as buttons, from
  `GET deployments/N/extend/`; one that a rule holds names who approves it;
- how to connect, as the item's `connection` lays it out, with a copy button where the entry
  allows one. A secret shows on demand, through `deployments/N/reveal/`, which records each
  showing;
- its actions, each with its template's survey and an optional reason, and **Hand it over**, to
  one of the teams or people from `GET deployments/N/hand_over/`;
- what happened, from its requests, and who can use it, with **Delete it now**, which asks first;
- when its teardown failed, the reason and what it left (`left_behind`), with **Try deleting it
  again**.

**Approvals** lists the requests waiting for the user's answer, oldest first; the tab shows how
many. Each one shows the rule that holds and its condition, the requester's reason, their answers
with those the condition reads highlighted, the quota the request uses, and what happens if nobody
answers in time. To deny, the approver writes a comment; approving needs none. On a phone, Deny
and Approve stay at the bottom of the screen. The portal reads the approvals from two endpoints:

| Endpoint | What it gives |
|---|---|
| `GET /api/v2/portal/approvals/` | The approvals waiting for the user's answer, oldest first, at most 100: `count` and `results`. |
| `GET /api/v2/portal/approvals/N/` | An approval of a catalog request that the user can see, waiting or settled. |

| Field | What it holds |
|---|---|
| `id`, `status`, `timed_out`, `created`, `expires` | The workflow approval, and when it times out. |
| `kind`, `rule`, `condition`, `approvers`, `fallback`, `required`, `approved`, `answers` | Why it waits and who answered, as in the request's [`approvals`](#approvals). |
| `can_answer`, `cannot_answer`, `own` | Whether the user can answer it, why not, and whether the request is theirs: nobody approves their own. |
| `request` | The request: `id`, `kind`, `status`, `created`, `reason`, `lifetime_days`, `item`, `team`, `requester`, `answers` (each with its survey `question`, in the survey's order), the `action` it runs, and the `deployment` it is about, with its expiry, extensions and `until`. |
| `quota` | For a new deployment of an item with a quota, the usage of the team or user it counts against, this request included. |

### Requests from another catalog

When another catalog, such as ServiceNow, is your users' front door, it requests items through the
API as the portal does, and hears back through webhooks. awxnext has no integration of its own
for it.

The other catalog calls the API as a user of its own, with a personal token (`/api/v2/tokens/`,
`write` scope) sent as `Authorization: Bearer <token>`. Give that user `CatalogItem Requester` on
the items, or `Organization CatalogItem Requester`, and make it a member of the teams it requests
for. Then:

1. To request an item, post its `answers`, `team`, `reason` and `lifetime_days` to
   `catalog_items/N/request/`, as in [Requests](#requests). Keep the request's `id`.
1. To follow it, read `catalog_requests/N/`: its `status` is `awaiting_approval` or `denied`
   while its [approvals](#approvals) decide, then follows its job's. Its `explanation` says why it
   was denied, or why it couldn't start.
1. Once a `deployment` item's request is `successful`, read the deployment, `deployments/N/`, from
   the request's `deployment`: its `outputs` and `connection`. Secret outputs show as
   `$encrypted$`; the owner shows one through `deployments/N/reveal/`, which records each
   showing.

The deployment belongs to the user that requested it, and to the request's team. To give it to
someone else, post `{"user": N}` or `{"team": N}` to `deployments/N/hand_over/`.

Rather than read the request again and again, link a webhook notification template to the item.
Post `{"id": N}` to `catalog_items/N/notification_templates_requests/`, as for the
[reminders](#deployments). Each change of a request's state then sends one of these events:

| Event | When |
|---|---|
| `waiting` | The request waits for approval. |
| `approved` | Its approvals are all given. A request that needs none sends no event until it runs. |
| `denied` | An approver denied it, or nobody answered in time. |
| `running` | Its job started. |
| `ready` | A new deployment is ready: its request succeeded and the deployment exists, with its outputs. |
| `done` | Any other request succeeded: an item that runs once, an extension or an action. |
| `failed` | Its job failed, or it couldn't start. |
| `canceled` | Its job was canceled, or an extension that waited was canceled when the deployment expired. |
| `expiring` | The deployment expires within 24 hours, then within 2 hours, with `hours_left`. |
| `deleted` | A deletion succeeded: the teardown ran. |

The webhook gets a JSON body:

| Field | What it holds |
|---|---|
| `event` | The event, from the table above. |
| `time` | When the request changed state. |
| `request` | The request: `id`, `url` (its page in the portal), `kind`, `status` at that time, `catalog_item`, `item_name`, `requester` and `team` by name, `reason`, `explanation`, `lifetime_days`, `created` and `deployment` (its id). |
| `deployment` | Empty unless the request is about a deployment: its `id`, `name`, `url`, `status`, `expires_at`, `catalog_item`, `item_name`, `owner`, `team`, `outputs` and `connection` as it is now, secrets masked. |

A request's events go out in order, each once, within a few seconds of the change: awxnext
records each change with the change itself, and its leader sends them. An email template linked
the same way gets the event as text, with a link to the request. A failed delivery isn't retried:
it shows as a failed notification in `/api/v2/notifications/`, and the other catalog catches up by
reading the request.

For example, to make a PostgreSQL database a ServiceNow catalog item, with awxnext's item 4 asking
for `name` and `size`:

1. In ServiceNow, create the catalog item with the variables `name`, `size` and `lifetime_days`,
   and a field on the requested item (`sc_req_item`) for awxnext's request, such as
   `u_awxnext_request`.
1. In its flow, add a REST step that posts the request to awxnext, with the token in an
   `Authorization: Bearer` header and the item's variables in place of `${...}`, and saves the
   `id` of the answer in `u_awxnext_request`:

   ```http
   POST https://awxnext.example.com/api/v2/catalog_items/4/request/
   Content-Type: application/json

   {"answers": {"name": "${name}", "size": "${size}"}, "lifetime_days": ${lifetime_days}, "reason": "${number}: ${short_description}"}
   ```

1. In ServiceNow, add a Scripted REST API resource, `POST /api/x_acme/awxnext/event`, that
   updates the requested item from each event:

   ```javascript
   (function process(request, response) {
     var event = request.body.data;
     var ritm = new GlideRecord('sc_req_item');
     if (!ritm.get('u_awxnext_request', String(event.request.id))) {
       response.setStatus(404);
       return;
     }
     ritm.work_notes = 'awxnext: ' + event.event + (event.request.explanation ? ': ' + event.request.explanation : '');
     if (event.event == 'ready') {
       ritm.work_notes += '\nHost: ' + event.deployment.outputs.host;
       ritm.state = 3; // Closed Complete
     } else if (event.event == 'denied' || event.event == 'failed') {
       ritm.state = 4; // Closed Incomplete
     }
     ritm.update();
     response.setStatus(204);
   })(request, response);
   ```

1. In awxnext, create a webhook template for that resource, with the user and password of a
   ServiceNow integration user, and link it to the item:

   ```sh
   curl -X POST https://awxnext.example.com/api/v2/notification_templates/ \
     -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"name": "ServiceNow", "organization": 1, "notification_type": "webhook",
          "notification_configuration": {"url": "https://acme.service-now.com/api/x_acme/awxnext/event",
            "http_method": "POST", "headers": {}, "username": "awxnext", "password": "'"$SN_PASSWORD"'"}}'
   curl -X POST https://awxnext.example.com/api/v2/catalog_items/4/notification_templates_requests/ \
     -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"id": 12}'
   ```

   `12` is the template's `id` from the first answer.

The requested item then shows each step in its work notes, and closes when the database is ready
or the request is denied or fails.

## Settings

The Settings pages group AWX's settings by what they set up rather than by AWX's categories:
System, Sign-in with a page for each login method, Jobs, Logging, and Login page and UI. Each
row says what its setting does, with the setting's AWX name below it (click it to copy it). Changes
are kept from page to page until you save them, and Save writes each one to its AWX category
(`PATCH /api/v2/settings/<category>/`): the API, the `awx` CLI and the `awx.awx` collection see the
same categories and names as in AWX. A setting changed from its default is marked where it is,
with its default and a "Reset" link, which takes effect when you save. "Changed from default" lists
every one of them, with its value, its default and a link to where it is set. The search above the
menu (press `/`) finds a setting by its label, its AWX name or its help, and the command palette
(`Ctrl K`) finds settings too. Leaving the settings with changes not saved asks first, and so does
closing or reloading the tab; going from one settings page to another keeps them. The settings
AWX has but awxnext ignores are hidden:
the end of their page says how many, with a link that shows them, under "No effect in awxnext",
on every page until you hide them again. A link to one of them, or a change to one, shows them
too. Rows are labelled by what they do; the API keeps AWX's labels. The former addresses (`/settings/authentication`,
`/settings/github-org`...) lead to the new pages.

Sign-in starts with a card for each login method, those on first, with what each is set to; the
menu marks them too. A method is on as the server decides it: OIDC with a provider URL and a
client ID, LDAP with a server URI, SAML with an identity provider, GitHub, Google and Microsoft
Entra ID with a client ID and a secret. A method's page says whether it is on, and "Turn off…"
resets its settings. GitHub's six AWX categories are one form: github.com or GitHub Enterprise,
then any account, the members of an organization or those of a team, which pick the category it
saves to (`github`, `github-org`... `github-enterprise-team`). Each category keeps its own
settings, as in AWX, and the form names the others set up, which the login page offers too. Each
form shows what to give the provider, with a copy button, where the provider asks for it: the
callback URL before the client ID, OIDC's redirect URI once OIDC is on, and SAML's metadata URL
once the service provider's certificate is set.

To sign in with Microsoft Entra ID, use the OpenID Connect page, unless the settings come from
AWX. Its provider URL is your tenant's, `https://login.microsoftonline.com/<tenant ID>/v2.0`, so
only your tenant's accounts sign in, guests included, and awxnext checks the ID token. The
Microsoft Entra ID page is AWX's `azuread-oauth2`: the `common` tenant's v1 endpoints, which need
a multi-tenant app registration, and the display name as the username.

- **At each login** (`OIDC_PROMPT`) sets the authorization request's `prompt`. `select_account`
  shows Entra's account picker instead of signing in with the browser's current account. `login`
  asks for the password again, and `consent` shows the consent screen again. When it's not set,
  the provider decides.
- **Groups:** Entra's `groups` claim holds the groups' object IDs, not their names, and a user in
  more than 200 groups gets no `groups` claim at all. To map access by name, define app roles on
  the app registration, assign them to groups, and set `OIDC_GROUPS_CLAIM` to `roles`.

| AWX category | Page |
|---|---|
| `system`, `named-url` | System |
| `authentication` | Sign-in |
| `oidc`, `ldap`, `saml` | Sign-in: OpenID Connect, LDAP, SAML |
| `github`, `github-org`, `github-team`, `github-enterprise`, `github-enterprise-org`, `github-enterprise-team` | Sign-in: GitHub, a section each |
| `google-oauth2`, `azuread-oauth2` | Sign-in: Google, Microsoft Entra ID |
| `jobs` | Jobs: launch safety, secret lookups, limits and timeouts, content, execution, output and events |
| `logging` | Logging |
| `ui` | Login page and UI |

The pages are drawn from `ui/src/lib/settingsLayout.ts`, which places each setting on a page, in
a section, in order, with its label and its control. A setting it does not place is still shown,
under "Other settings" on its category's page. Tests keep the layout complete: `go run
./cmd/gendocs` writes the API's settings to `ui/src/lib/settings.generated.json` (a Go test fails
when it is out of date), and the layout's test fails when a setting with an effect is not
placed, is placed on a page that does not show its category, or when the layout names a setting
the API does not have.

Each kind of value has its control, and is saved in AWX's format:

- **Durations and sizes** are typed in a unit (minutes, hours, MiB...) and stored in seconds or
  bytes. The value that means none, such as 0 for "No limit" or -1 for any number of sessions, is
  a checkbox.
- **Secrets** (passwords, client secrets, private keys) show "Set" once set, never their value:
  Replace types a new one, Remove empties it on save. AWX's clients read them as `$encrypted$`.
- **Lists** are chips, or checkboxes when their items are known (the loggers sent to the
  aggregator). **Environment variables** and **LDAP connection options** are names and values;
  the options keep their numbers.
- **LDAP searches** are a base DN, a scope and a filter; a user search may have several, of which
  exactly one must find the user. **SAML's identity providers** and its organization by language
  are cards, its contacts and security small forms; the keys a form does not show are kept.
- **The logo** is an image file (GIF, PNG or JPEG), stored as a data URL. Callback URLs, the
  metadata URL and the installation's ID are read-only, with a copy button.

**Organization and team maps** are rows: what people join, who, as what, and what happens when
they no longer match. Add an organization or a team, then choose who gets each role: everyone
who signs in, these people (usernames, emails, or names matching a pattern such as
`/^ops-/i`), no one, or no change. In LDAP's maps, people are members of groups, given by
distinguished name, and organizations also have auditors. An organization map and its team map
are saved in AWX's format, without the options left at their default, so AWX's clients read
them unchanged. LDAP's superusers and auditors by group, and SAML's organizations, teams and
flags from attributes, are forms too.

**Try with a user**, under each login's maps, shows what signing in would give someone: give a
username, and an email (for LDAP, the groups the user is a member of, one distinguished name
per line; for SAML, its attributes, one `name = value` per line). The server evaluates the maps
as shown, unsaved edits included, with the code a login runs, and saves nothing. It answers with
the superuser and auditor flags, the roles granted and why, those left as they are, those taken
away if the user has them, and the organizations and teams that would be created. An existing
user's email is used when none is given. The API is `POST /api/v2/settings/maps/test/`, for
superusers: `login` is `default` (the maps OIDC uses), `github`, `github-org`, `github-team`,
`github-enterprise`, `github-enterprise-org`, `github-enterprise-team`, `google-oauth2`,
`azuread-oauth2`, `saml`, or `ldap`, `ldap1`... `ldap5`; `settings` holds the values not saved
yet, of that login's map settings only, checked as saving them would check them; `groups` and
`attributes` describe the user for LDAP and SAML.

**Test the connection** ends each LDAP server's section, OIDC's provider and SAML's identity
providers. It tries the settings as shown, unsaved edits included, and reports each step, up to
the first that fails, with why; nothing is saved, and no one is signed in.

- **LDAP** connects to each server URI in turn (with StartTLS when it is on) and binds as the
  bind DN, or anonymously. Give a username to also find the user, by the DN template or the user
  searches, which must find exactly one entry; read their groups; check the required and denied
  groups; and show their DN, name, email, groups, and what the maps would give them. Give their
  password too to check it, with a bind as the user, as at login.
- **OIDC** reads the discovery document at the provider URL, warns of scopes the provider does
  not list, and counts the signing keys of its `jwks_uri`. The client secret is not checked:
  providers answer a token request without a user in different ways.
- **SAML** checks that the service provider's certificate is valid and matches its private key.
  Then, for each identity provider, its certificate's dates (a warning 30 days before it
  expires), its metadata, and that its sign-in URL answers. The metadata is read from the URL
  given in the test, or else from the entity ID when that is a URL, as many identity providers
  serve it there; the entity ID, the signing certificate and the sign-in URL are compared with
  the settings. With neither, that step is skipped.

The API is `POST /api/v2/settings/connections/test/`, for superusers: `login` is `ldap`,
`ldap1`... `ldap5`, `oidc` or `saml`; `settings` holds the values not saved yet, of that login's
settings only (a secret left as `$encrypted$` is the saved one); `username` and `password` are
LDAP's user, and `metadata_urls` gives SAML's metadata URL by identity provider name. It answers
`ok` and the `steps`, each with its `step`, `idp` for SAML, `status` (`ok`, `warning`, `skipped`
or `failed`) and `detail`, and for LDAP the `user` found.

Only three settings with no effect, whose shape is free-form, are still JSON:
`OAUTH2_PROVIDER`, `SOCIAL_AUTH_SAML_SP_EXTRA` and `SOCIAL_AUTH_SAML_EXTRA_DATA`.

## Observe

- **Metrics:** `/metrics` (Prometheus; `AWXNEXT_METRICS_TOKEN` to protect it) and
  `/api/v2/metrics/`, AWX's.
- **Traces:** OpenTelemetry, with `OTEL_EXPORTER_OTLP_ENDPOINT`.
- **Event streams and rules:** their metrics and traces are in
  [Metrics and traces](#metrics-and-traces).
- **Logs:** JSON on stderr (`AWXNEXT_LOG_FORMAT=json`), and AWX's external logging to
  logstash, Splunk, Loggly, Sumo Logic or any HTTPS, TCP or UDP receiver (Settings, Logging).
- **Instance health:** the Instances page, `awxnext list-instances`, and
  `/api/v2/instances/N/health_check/`.

## Security scanning

Four things look for vulnerabilities in awxnext. None merges anything: the owner reviews and
merges every PR.

- **secubot** ([secfix.yml](../.github/workflows/secfix.yml)) scans the code, its dependencies
  and the all-in-one image, has Claude triage each finding, and proposes fixes as draft PRs.
- **Dependabot** raises alerts and proposes upgrades of the dependencies.
- **CodeQL** analyzes the code and the workflows on each push.
- **`make vuln`** runs scanners by hand, on both images.

No scan fails CI: a vulnerability becomes an alert, an issue or a PR.

### secubot

[secubot](https://github.com/damhau/secubot) is the owner's reusable workflow. awxnext calls it
from [secfix.yml](../.github/workflows/secfix.yml), pinned to a release's commit, and
configures it in [.secfix.yml](../.secfix.yml) ([its reference](https://github.com/damhau/secubot/blob/main/docs/configuration.md)).
A run has five steps:

1. **Scan**, with pinned scanners:
   - Trivy on the files: the Go modules, the UI's npm packages, the Helm chart and Compose
     files, secrets and licences;
   - Trivy on the image the Dockerfile builds by default;
   - govulncheck, which reports only what the code reaches;
   - gosec and Semgrep on the code.

   Each scanner's results go to Security, Code scanning, under `secubot/<tool>`. Trivy skips
   the IDs listed in [.trivyignore](../.trivyignore).
2. **Select.** secubot drops some findings:
   - those below high severity (`policy.min_severity`);
   - those under `paths.skip`: tests and their data, the docs, the compatibility harness and
     the built UI;
   - those suppressed or already handled (a `secfix/<id>` branch, a PR or an issue);
   - dependency upgrades with a minor or patch fix, which are left to Dependabot.

   It ranks the rest and keeps the first `policy.max_triage` (10). At most `policy.max_prs`
   (3) become fixes in a run; the others wait for the next one.
3. **Triage.** Claude reads the code, without running anything, in one session: an analysis.
   secubot can add a second session, a review that tries to refute it, but `.secfix.yml` turns
   it off (`policy.challenge: false`) to halve the cost. What happens next depends on the
   verdict:
   - a false positive is suppressed for 30 days if Claude is highly confident and the finding
     is not critical; otherwise it becomes an issue, for a human to confirm;
   - a real finding goes to a fix;
   - a doubtful one, a hard-coded secret or a suspected prompt injection becomes an issue.
4. **Fix.** Claude edits the code. Besides reading and editing, it may run only `.secfix.yml`'s
   `build` and `test` commands, the rescan, and `go get` and `go mod tidy` for a Go module.
   secubot then runs `build` (`make fmt-check build`) and `test` (`make test test-ui`) itself,
   and scans again. It allows up to 3 attempts.

   A fix never touches `.github/`, `.trivyignore`, `SECURITY.md` or `CLAUDE.md`, and changes
   authentication or cryptography code only when the finding is there.
5. **Publish.** A step with no model checks each patch again, then opens it as a **draft** PR
   labelled `security` on a `secfix/<id>` branch, or opens a `[secfix:<id>]` issue.

The run's summary lists each finding handled, its verdict and what became of it, with the
reasoning. It also lists the findings left alone, and why. Claude runs on the
`ANTHROPIC_API_KEY` secret, within `.secfix.yml`'s budgets: $1.50 per triage (one session for
each finding) and $5 per fix.

**Today it runs by hand, and publishes** (since 2026-11-02): `gh workflow run secfix.yml`,
or Actions, secfix, Run workflow. Each fix's patch is also kept in the run's
`secubot-fix-<id>` artifacts. To try it without publishing, put `dry-run: true` back under
`with:` in `secfix.yml`: a dry run scans, triages and fixes, but opens nothing. A daily
schedule is written in `secfix.yml`, commented out: uncomment it to run secubot daily.
harden-runner only records the jobs' outbound connections (`egress-policy: audit`). `block`
alone would break the run: secubot's lists lack repo1.maven.org (the tests' PostgreSQL) and
Docker Hub (the image's base images). Since v1.2.0, `extra-allowed-endpoints` under `with:`
can add them; awxnext has not done this.

How to handle what it publishes:

- **A PR.** secubot opens PRs as the owner's secubot GitHub App, whose ID and key are the
  `SECUBOT_APP_ID` and `SECUBOT_APP_PRIVATE_KEY` secrets, so CI runs on them as on any PR.
  Without those secrets it falls back to the workflow's `GITHUB_TOKEN`: GitHub starts no
  workflow on that token's events, so close and reopen such a PR to run CI, and Actions must
  be allowed to create PRs (Settings, Actions, General). Then merge the PR, or close it:
  secubot then leaves the finding alone for 90 days.
- **An issue.** Close an issue as **Not planned**, with a comment saying why, to dismiss the
  finding for 180 days.
- **A finding that goes away** (a merged Dependabot PR, a fix by hand): the next run closes
  its secubot issue or PR, except the issues of hard-coded secrets.
- **Noise.** Add a pattern to `paths.skip`, or an entry to `suppressions`, with its reason and
  an expiry date. Both are reviewed like code. The suppressions triage decides are kept on
  the `secfix-state` branch.

secubot builds and scans the stage `.secfix.yml` names (`scanners.trivy_image.target`):
`all-in-one`, the image `main` publishes and the permanent instance runs. It contains the
binary. Only `make vuln-images` scans the `awxnext` image's distroless base.

To turn secubot off, delete `secfix.yml`. The `secfix/*` branches and `secfix-state` can be
deleted at any time.

### Scanning by hand

The [Makefile](../Makefile) pins its own scanners:

- **Go code** (`make vuln-go`): govulncheck, which reports only the vulnerabilities the code
  reaches, in its modules and in the standard library of the Go that builds it (the
  `toolchain` line of `go.mod`).
- **UI dependencies** (`make vuln-web`): `npm audit` in `ui/`, build and test tools included.
- **Images** (`make vuln-images`): both images built, then scanned with Trivy: Debian packages,
  the all-in-one image's Python packages (ansible-core, ansible-runner and theirs), and the Go
  modules and standard library of the binaries. Any HIGH or CRITICAL vulnerability that has a
  fix fails the scan.
- **SBOMs** (`make sbom`): CycloneDX SBOMs in `sbom/`, one for each image and one for the UI's
  npm dependencies. The images hold the UI as built files, which neither the image scan nor
  the image SBOMs see; the UI's SBOM lists the packages built into it (its `dependencies`,
  not the build and test tools).

`make vuln` runs the three scans (the images need Docker; Trivy keeps its database in the
`awxnext-trivy-cache` volume). Until 2026-11-01, a `security.yml` workflow ran them in CI;
secubot replaced it (M9-04). When one fails:

- **Upgrade** what has the fix: the Go module (`go get`), the npm package, Go itself (the
  `toolchain` line of `go.mod`; the Dockerfile's `golang:1.26` takes the latest patch release),
  or a base image. The all-in-one image upgrades its Debian packages as it builds, so Debian's
  fixes reach the next build.
- **Accept** an image finding that cannot be fixed or does not apply by adding its ID to
  [.trivyignore](../.trivyignore), with the reason and an expiry date (`CVE-2026-12345
  exp:2027-01-31`), after which it fails the scan again. secubot's Trivy reads the same file.

### Dependabot

Dependabot watches the Go modules, the UI's npm packages, the workflows' actions and the
Dockerfile's base images ([dependabot.yml](../.github/dependabot.yml)):

- **Alerts** (Security, Dependabot) when a vulnerability is published in a Go module, an npm
  package or an action awxnext uses. Base images are not covered: secubot's Trivy scans the
  all-in-one image, and `make vuln-images` both.
- **Security updates:** a PR for each alert that has a fix, opened as soon as the alert is
  raised.
- **Version updates,** weekly: the minor and patch releases of each ecosystem in one PR, each
  new major version in a PR of its own. A release is proposed once it is 7 days old, as
  malicious releases are usually found and pulled sooner.
- **No major versions for the UI** (vite 8, react-router 8, TypeScript 7,
  `@vitejs/plugin-react` 6...): a UI major upgrade becomes a roadmap task when the owner
  chooses, with the UI checked in the browser, not a Dependabot PR. vitest 5 also needs Node
  22.12 to run the tests locally. The `node` base image's major is left out too: Node 22 builds
  the UI, in the Dockerfile and in the workflows' `node-version`, and they move together, by
  hand. Dependabot's alerts still report the UI's vulnerabilities. When the only fix is a
  major version, secubot may propose it as a draft PR, and the owner decides.

CI runs on each PR, whatever it changes, and the owner reviews and merges them. `main` has no
branch protection: the owner's pushes go straight to it, and nothing but the owner's review
stands between a PR and `main`. The PRs carry the `dependencies` label.

The Dockerfile's base images keep floating tags (`golang:1.26`, `node:22`,
`python:3.14-slim-bookworm`, distroless's `static-debian12:nonroot`), not digests. Each build
takes their latest patch release, so a fix in Go's standard library, which is compiled into the
binaries, or in Debian reaches the next image without waiting for a PR. Digests would make
builds reproducible, but the all-in-one image already upgrades its Debian packages and
installs the latest PostgreSQL client as it builds, so it would not be reproducible anyway.
secubot's image scan catches a tag that brings a vulnerability into the all-in-one image, and
`make vuln-images` into either image. Dependabot proposes the next
lines (Go 1.27, Python 3.13...); uv is pinned to a release (`ghcr.io/astral-sh/uv:0.13.5`).
For Python, it proposes only the lines that the image's ansible-core runs on (3.12 to 3.14 for
ansible-core 2.20). When ansible-core moves, move that bound in `dependabot.yml` too.

### CodeQL

CodeQL's default setup (a repository setting, with no workflow file) analyzes the Go code, the
UI's TypeScript, the Python scripts and the workflows on each push to main, on each PR and
weekly. Its findings are under Security, Code scanning. Each one is fixed, filed as an issue
(labelled `security` when it gives access or rights a user should not have, `bug`
otherwise), or dismissed with the reason: a false positive, or behaviour that is meant, such
as a notification's `disable_ssl_verification`. An alert that an issue tracks stays open until
the fix closes it.

### Pinned actions

Every action in the workflows is pinned to a commit, with its version in a comment:
`uses: actions/checkout@<commit> # v4.4.0`. Whoever controls an action can move its tags to
other code, but a commit always names the same code. Dependabot's PRs move the commit and the
comment together. To add an action,
look up the commit of its release, `gh api repos/<owner>/<action>/commits/<version> --jq
.sha`, or pin it with [pinact](https://github.com/suzuki-shunsuke/pinact). CI fails on an
action used by tag (`make pinned-actions`).

### Reporting a vulnerability

Report a vulnerability in awxnext privately, through **Report a vulnerability** on the
repository's Security tab, not in an issue. [SECURITY.md](../SECURITY.md) says what is
supported (the latest image from `main`) and what to include, and promises an acknowledgement
within 7 days. The report becomes a draft security advisory, where the fix is discussed and
from which it is published.
