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
