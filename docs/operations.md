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
| `enabled` | `false` turns the window off. |

A window covers the rules that it lists, the launches on the hosts of the groups that it lists,
or, when it lists neither, every rule of its organization. It covers only the rules of its own
organization. Listing a rule takes the right to change it, and listing a group the right to
change its inventory. Deleting a rule or a group takes it out of the windows that list it.

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
| `revision`, `imported` | The project's revision the file came from; when the rulebook was last imported. |
| `import_error` | Why the last import couldn't read the rulebook. The objects of the import before stay as they were. |
| `report` | What became of each ruleset, source, rule and action: `translated`, `partial` or `not_translated`, the `reasons` for what isn't, the `notes` where awxnext behaves otherwise than ansible-rulebook, and the `object` each piece became. `summary` counts the rules by status. |

`/api/v2/rulebooks/N/event_streams/`, `event_connectors/` and `event_rules/` list the objects a
rulebook's imports made, and `POST /api/v2/rulebooks/N/import/` imports it again. Whoever can
view the project can view its rulebooks and their reports; adding, changing, deleting and
importing a rulebook takes the right to change the project. Rulebooks aren't a GitOps kind:
their project's git repository already holds them.

In the UI, a project's **Rulebooks** tab lists them, imports them again and uploads one; a
rulebook's page shows its report, with a link to each object. The editor of an imported rule
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
certificate check. A stream's `additional_data_headers` names request headers its events
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
  rule ([Event rules](#event-rules)). Disabling turns both off.
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
Publishing through the API comes with roadmap M13-02.

### Who sees a collection

You see and install a namespace's collections when you can see the namespace: as a member,
admin or auditor of its organization, a system administrator or auditor, or with the
`CollectionNamespace Read` or `CollectionNamespace Admin` role on it. Anyone else gets `404`, on
the collection, its versions and its download alike, and lists leave it out: a client then goes
on to its next server, and the namespaces of other organizations stay unknown. Every call but
the API's root needs a user: a personal token (`/api/v2/tokens/`, `read` scope is enough), sent
as `Authorization: Token <token>`, which is what `ansible-galaxy` sends, or `Bearer <token>`;
basic authentication while `AUTH_BASIC_ENABLED` is on; or a session.

### URLs

| What | Path |
|---|---|
| Server for `ansible-galaxy` (the published repository) | `/api/galaxy/content/published/`, or `/api/galaxy/` |
| Collections | `/api/galaxy/v3/plugin/ansible/content/published/collections/index/` |
| A collection, its versions | `.../index/<namespace>/<name>/`, `.../index/<namespace>/<name>/versions/` |
| A version | `.../index/<namespace>/<name>/versions/<version>/` |
| Its artifact | `/api/galaxy/v3/plugin/ansible/content/published/collections/artifacts/<namespace>-<name>-<version>.tar.gz` |

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

### Jobs

A job installs its project's `collections/requirements.yml` from its organization's Galaxy
credentials, in order. To install from the hub, add the managed **Collection Hub** credential to
the organization's Galaxy credentials (`POST /api/v2/organizations/<id>/galaxy_credentials/`).
It holds no token: each install gets a read token of its own, for the namespaces of the
project's organization, deleted when the install ends. Should that fail, it expires 5 minutes
after the install's timeout (`DEFAULT_PROJECT_UPDATE_TIMEOUT`, an hour without one). An install
the requirements cache answers gets no token.

The install reaches awxnext where it runs:

| Where the job runs | The hub's address |
|---|---|
| A process on awxnext's node | `AWXNEXT_INTERNAL_URL`, else the listen address on loopback |
| A container on awxnext's node, or a container group's pod | `AWXNEXT_SERVICE_URL`, else `AWXNEXT_BASE_URL` |
| An execution node | `AWXNEXT_BASE_URL`, which its agent connects to |

### Storage

Collection artifacts are kept in the database by default. To keep them in object storage, set
the `HUB_STORAGE_*` settings (Settings, System, or `/api/v2/settings/system/`), as the
[job event archives'](#job-event-archives): `HUB_STORAGE` (`database`, `s3`, `azure_blob`), the
bucket or container, the endpoint, the region and the credential. Objects are named
`<prefix><INSTALL_UUID>/<sha256>/<namespace>-<name>-<version>.tar.gz` (`HUB_STORAGE_PREFIX`,
`collections/` by default). Artifacts stored before a change stay where they are; a deleted
version's object is deleted at the next maintenance (every 10 minutes). A backup holds the
database only: keep the bucket's own backups or versioning.

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
`python:3.12-slim-bookworm`, distroless's `static-debian12:nonroot`), not digests. Each build
takes their latest patch release, so a fix in Go's standard library, which is compiled into the
binaries, or in Debian reaches the next image without waiting for a PR. Digests would make
builds reproducible, but the all-in-one image already upgrades its Debian packages and
installs the latest PostgreSQL client as it builds, so it would not be reproducible anyway.
secubot's image scan catches a tag that brings a vulnerability into the all-in-one image, and
`make vuln-images` into either image. Dependabot proposes the next
lines (Go 1.27, Python 3.13...); uv is pinned to a release (`ghcr.io/astral-sh/uv:0.13.5`).

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
