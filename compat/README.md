# Compatibility suites

`run.py` points AWX's own clients at an awxnext and reports every test:

| Suite | What runs | One test is |
|---|---|---|
| `collection` | the `awx.awx` integration targets from AWX 24.6.1, each run as a role, and `targets/` here for modules the collection ships no test for (`license`, `subscriptions`: what an upstream AWX without a subscription answers) | a target (module) |
| `cli` | smoke steps of the `awx` CLI from awxkit 24.6.1: login, CRUD, lookups by name, roles, launch and monitor, stdout, settings, export/import | a step |
| `terraform` | the acceptance tests of `ansible/terraform-provider-aap` v1.5.0, after its fixture playbook (`awx.awx` stands in for `ansible.controller`/`ansible.platform`; EDA fixtures are dropped) | a Go test |

Each run writes `report.md`, `report.json` and one log per test to the report
directory (`compat-report/` by default).

## Running it

You need Docker, [uv](https://docs.astral.sh/uv/), Go and Terraform for the
`terraform` suite. The sources of the collection and the provider are cloned
once into `~/.cache/awxnext-compat`.

```sh
docker compose -f compat/docker-compose.yml up -d --build --wait   # throwaway awxnext
uv run --script compat/run.py                                       # all suites
uv run --script compat/run.py --suite collection --only job_launch  # one target
docker compose -f compat/docker-compose.yml down
```

The suites reach awxnext at `https://localhost:8054`, through a Caddy proxy
with a self-signed certificate, as AWX's own test environment serves HTTPS
(the `organization` target checks that certificates are verified). Plain
HTTP stays on port 8053. `--host`, `--username` and `--password` point the
suites at another instance.

They expect AWX's preload data: the `demo_data` target creates it, and the
harness adds the `Demo Credential` that AWX's installer also creates. The
suites create and delete objects, so use a throwaway install.

## Known failures

`known-failures.yml` lists the tests expected to fail, each with the issue that
tracks it. The run fails when a test fails that is not listed, or when a listed
test passes, so the list stays true: fix the issue, then remove its entries.
`--update-known` rewrites the list for the suites that ran, keeping the issue of
each entry still failing and marking new ones `TODO`.

## CI

`.github/workflows/compat.yml` runs every suite nightly and on demand (Actions,
Compatibility, Run workflow, optionally one suite). The report is in the run
summary and in the `compat-report` artifact, with the server log.
