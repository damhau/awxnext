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
   - for secret lookups that log in as the server itself (Vault's `kubernetes_role`, Azure Key
     Vault without a client secret), list their hosts in
     `CREDENTIAL_LOOKUP_SERVER_IDENTITY_HOSTS` ([Secret lookups](#secret-lookups));
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
