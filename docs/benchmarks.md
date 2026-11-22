# Benchmarks against AWX

awxnext and AWX 24.6.1 measured the same way, on the same machine, through their public API
only: [awxbench](../cmd/awxbench/main.go) ([internal/bench](../internal/bench/bench.go)) runs
the same steps against both.

## How

The [bench workflow](../.github/workflows/bench.yml) (run by hand, `workflow_dispatch`) uses one
GitHub-hosted `ubuntu-latest` runner:

1. Creates a kind cluster.
2. Installs AWX 24.6.1 with awx-operator 2.19.1, with the operator's defaults: one web and one
   task replica, and its own PostgreSQL 15.
3. Installs awxnext with its Helm chart: one replica, the bundled PostgreSQL 16.
4. Runs awxbench against each, one after the other. Both run their jobs in container group pods
   with the same execution environment, `quay.io/ansible/awx-ee:24.6.1`.

For each install, awxbench creates an organization, a local inventory, and a git project
holding [bench/playbooks](../bench/playbooks). Then it measures:

| Measure | What awxbench does |
|---|---|
| Event ingestion | A job looping over 5,000 items (`events.yml`, a `debug` task): events stored, divided by the time from the job's start to `event_processing_finished`. That time includes Ansible running the loop in the pod, the same work for both. |
| Hosts list | `GET /api/v2/hosts/?inventory=N&page_size=200&order_by=name` on an inventory of 10,000 hosts (made with `bulk/host_create`, 100 at a time), 50 times. |
| Hosts search | The same with `search=host-0042`. |
| Jobs list | `GET /api/v2/unified_jobs/?page_size=50&order_by=-id`, 50 times. |
| Launches accepted | 50 launches of a one-task job (`hello.yml`), 10 at a time: launches per second until all are accepted. |
| Small jobs finished | Those 50 jobs per minute, from the first launch until all have finished. Each runs in its own pod, so pod start-up time dominates. |

## Results

[Run 41053494703](https://github.com/damhau/awxnext/actions/runs/41053494703), 2026-10-30, commit
8a586b3:

| Measure | AWX 24.6.1 | awxnext |
|---|---|---|
| Event ingestion (events/s) | 82 (5,010 events in 61.0 s) | 227 (5,006 events in 22.0 s) |
| Hosts list, page of 200 (p50 / p95) | 1,580 / 1,733 ms | 115 / 130 ms |
| Hosts search (p50 / p95) | 957 / 1,072 ms | 76 / 90 ms |
| Jobs list, page of 50 (p50 / p95) | 249 / 275 ms | 3 / 5 ms |
| Launches accepted (per s) | 1.1 | 105 |
| Small jobs finished (per min) | 42 | 57 |

The event counts differ because AWX adds a few events of its own (5,010 against 5,006). Both
APIs answer `ping` with version 24.6.1: awxnext reports the AWX version its API follows.

## Reading the numbers

- **Small installs on one machine.** Both run with their defaults and share one runner's CPUs.
  The numbers compare the two under the same conditions; they are not the capacity of a
  production cluster.
- **One run.** GitHub's runners vary from run to run. Treat differences of a few percent as
  noise; the differences above are much larger than that.
- **Jobs finished per minute is mostly Kubernetes.** Most of each small job's time is starting
  its pod, so the gap (42 against 57) is smaller than for the API measures.
- **The lists measure the API, not the UI.** AWX's UI makes other requests (counts, related
  objects); awxnext's makes its own.

## Run it again

From the Actions tab, run **bench** (inputs: hosts, events, launches). Or run awxbench against
any install, AWX or awxnext:

```sh
go build -o awxbench ./cmd/awxbench
AWXBENCH_PASSWORD=... ./awxbench run --url https://awx.example.com --out awx.json
AWXBENCH_PASSWORD=... ./awxbench run --url https://awxnext.example.com --out awxnext.json
./awxbench table "AWX=awx.json" "awxnext=awxnext.json"
```

`--container-group` runs the jobs in a container group the run creates, as the workflow does;
without it, they run where each install runs jobs by default. `--project` and `--branch` name
the repository holding `bench/playbooks` (this one by default). Each run creates its own objects,
named `bench-<time>`, and leaves them in place.

## Event rules

AWX has no event rules of its own, so this measure is awxnext's alone. `awxbench rules` creates
two CloudEvents streams without a rate limit and a rule that reads the first: a CEL filter on the
type and the severity, the key `event.data.host`, `once_within` an hour, and `post_event` to the
second stream. Then it posts events in batches (`application/cloudevents-batch+json`) and waits
until the rule has decided every event that its filter holds for. One event in ten is a warning,
which the filter drops; the events are about 100 hosts, so the rule posts 100 events and
throttles the others.

| Measure | What awxbench measures |
|---|---|
| Ingestion | Events accepted per second, from the first post until the last is accepted. |
| Decisions | Events decided per second, from the first post until the rule has decided the last. |
| Lag | The time from the last post accepted until the rule has decided the last event. |

### Results

2026-11-15, the Compose stack of `compat/` (awxnext and PostgreSQL 16, one node) on an AMD Ryzen
9 7950X under WSL 2, with awxbench in the awxnext container:

| Events (decided) | Per request, at a time | Ingestion (events/s) | Decisions (events/s) | Lag |
|---|---|---|---|---|
| 50,000 (45,000) | 100, 8 | 2,958 | 2,643 | 0.12 s |
| 50,000 (45,000) | 500, 16 | 3,038 | 2,670 | 0.40 s |

The rule keeps up with ingestion: it decides the last event a fraction of a second after it is
stored. A stream stores its events one request at a time, in order, so one stream's ingestion
bounds the measure, not the rule. To run it:

```sh
AWXBENCH_PASSWORD=... ./awxbench rules --url https://awxnext.example.com --events 50000
```

`--batch` and `--concurrency` set the events per request and the requests at a time, and
`--keys` the number of hosts.

## Secrets in job output

AWX doesn't mask secrets in job output, so this measure is awxnext's alone
([what awxnext masks](operations.md#secrets-in-job-output)). Each event goes through one
`strings.Replacer` that holds every form of every secret the run was handed. The replacer is a
trie, so it reads each event once, however many secrets there are.

**Per event:** `BenchmarkMask` ([internal/jobs](../internal/jobs/mask_test.go)) masks the events
of a `debug` loop, one in 100 of them printing a secret:

| Secrets | Time per event | Allocations per event |
|---|---|---|
| None | 16 ns | 0 |
| 1 | 0.86 µs | 13 |
| 20, and a 27-line private key | 2.5 µs | 94 |

**Per job:** `awxbench masking` runs `events.yml` over 100,000 items twice: without secrets, and
with a credential of 20 secret fields whose first one every 100th item prints. Then it measures
event ingestion as `awxbench run` does: events stored, divided by the time from the job's start
to `event_processing_finished`.

### Results

2026-11-18, the Compose stack of `compat/` (awxnext and PostgreSQL 16, one node) on an AMD Ryzen
9 7950X under WSL 2, with awxbench in the awxnext container and the jobs run in it. The second
run launched the same two templates in the opposite order:

| Run | Job | Events/s | Ansible's loop | Last item to all events stored |
|---|---|---|---|---|
| 1 | Without secrets | 347 (100,006 in 288.3 s) | 282.6 s | 4.2 s |
| 1 | With 20 secrets | 320 (100,006 in 312.7 s) | 306.4 s | 4.7 s |
| 2 | With 20 secrets | 312 (100,006 in 320.3 s) | 314.5 s | 4.2 s |
| 2 | Without secrets | 269 (100,006 in 371.2 s) | 364.9 s | 4.7 s |

Ansible's loop takes about 3 ms per item, and the job that runs second is the slower one, with or
without secrets. Masking costs 2.5 µs per event, about 0.25 s for 100,000 events, which is less
than the variation between runs. The event rate is bound by Ansible, not by masking. To run it:

```sh
AWXBENCH_PASSWORD=... ./awxbench masking --url https://awxnext.example.com --events 100000 --secrets 20
```

`--secrets` sets the number of secret fields. Against AWX, both jobs do the same work.

## Reports

The reports are awxnext's alone ([Reports](operations.md#reports)). `awxbench reports` writes a
large history straight into the report tables, since running a million jobs would measure the
job runner instead: 1,000,000 runs of 500 templates over 400 days, in two organizations, and
6,000,000 host days, 5,000 hosts that 3 templates each run every day. Then it requests each
report over 30, 90 and 400 days, 10 times each, as three readers:

- the admin, filtered on one organization;
- the admin, over both organizations;
- a user with the Organization Execute role on one organization, who reads half the history
  through the jobs' rights.

### Results

2026-11-22, the Compose stack of `compat/` (awxnext and PostgreSQL 16 with its default settings,
one node) on an AMD Ryzen 9 7950X under WSL 2, with awxbench in the awxnext container. Times are
p50 / p95, in ms:

| Report | Days | Admin, one organization | Admin, both | Organization role |
|---|---|---|---|---|
| Totals | 30 | 104 / 134 | 115 / 121 | 143 / 184 |
| Totals | 90 | 226 / 235 | 245 / 251 | 328 / 335 |
| Totals | 400 | 371 / 392 | 482 / 520 | 731 / 761 |
| Templates | 30 | 90 / 98 | 164 / 169 | 123 / 130 |
| Templates | 90 | 263 / 281 | 509 / 530 | 343 / 373 |
| Templates | 400 | 1,111 / 1,204 | 1,979 / 2,029 | 1,094 / 1,153 |
| Trends | 30 | 25 / 27 | 40 / 41 | 44 / 52 |
| Trends | 90 | 75 / 83 | 132 / 134 | 104 / 106 |
| Trends | 400 | 356 / 369 | 590 / 616 | 309 / 315 |
| Hosts | 30 | 310 / 337 | 397 / 447 | 230 / 245 |
| Hosts | 90 | 680 / 705 | 923 / 940 | 513 / 554 |
| Hosts | 400 | 2,063 / 2,260 | 2,842 / 4,114 | 2,052 / 2,229 |
| Launches | 30 | 37 / 42 | 44 / 45 | 62 / 64 |
| Launches | 90 | 98 / 119 | 95 / 102 | 128 / 132 |
| Launches | 400 | 166 / 232 | 205 / 227 | 285 / 302 |

Over 30 and 90 days every report answers in under a second. Over 400 days, the hosts report reads
all 6,000,000 host days and takes 2 to 3 s, and templates, with its medians and percentiles over
a million runs, 1 to 2 s.

Three choices made these numbers, each measured against the first version:

- **Rights once per key.** Checked on each row, the jobs' rights took 18.5 s for the hosts report
  over 90 days with the organization role. A report now checks them once per template,
  organization and launcher of its days (`report_keys`, 625 keys here), then joins the rows to
  the keys that passed: 0.5 s.
- **Aggregate before joining.** Counting distinct hosts in two steps, and summing a host's days
  before looking up its inventory, took the hosts per month over 30 days from 965 to 133 ms. An
  index on the day lets a short period skip the rest of the table.
- **A plan for each request.** Each report runs in a read-only transaction with
  `plan_cache_mode = force_custom_plan` and `work_mem` at 32 MB. After a few runs, PostgreSQL
  had switched to a generic plan that read 6,000,000 rows without parallel workers and sorted
  them on disk: 8.1 s instead of 0.9 s.

To run it against a test install, with its database:

```sh
AWXBENCH_PASSWORD=... ./awxbench reports --url https://awxnext.example.com --database-url postgres://...
./awxbench reports --database-url postgres://... --clean
```

`--runs`, `--days`, `--templates`, `--hosts` and `--host-templates` size the history, and
`--requests` sets the requests per report. The seeded rows show in the install's reports until
`--clean` deletes them; their two organizations and their user stay.
