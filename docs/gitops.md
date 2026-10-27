# GitOps sources

A GitOps source applies YAML declarations from a git project to one
organization (optionally placing objects in a folder).

```
POST /api/v2/gitops_sources/
{"name": "platform", "organization": 1, "project": 7, "path": "awx/", "prune": true}
```

| Field | Meaning |
|---|---|
| `project` | Project in the same organization; its synced revision is applied. |
| `path` | Directory scanned recursively for `*.yml` / `*.yaml`. Files without a `kind` (playbooks, vars files) are ignored. |
| `prune` | Delete objects of this source that are no longer declared. |
| `adopt_existing` | Take over unmanaged objects with a declared name instead of failing. |
| `sync_on_project_update` | Apply after each successful project update. |

`POST /api/v2/gitops_sources/{id}/sync/` applies the source now;
`?dry_run=true` (or `{"dry_run": true}`) returns the plan without writing.
The response, also stored in `last_report`, lists every change:
`create`, `update` (with the changed fields), `unchanged`, `delete`, `error`.

A sync is atomic: if any declaration fails, nothing changes and the source
keeps its previous `last_revision`.

## Ownership

Objects created by a source have `managed_by: gitops:<id>`. The API rejects
edits to them (and to their hosts, groups, survey and workflow nodes) with
409; change the files instead. Deleting the source releases its objects.

## Declarations

References are names within the organization. Fields not declared are reset
to their defaults, so deleting a line reverts the setting.

```yaml
kind: Label
name: web
---
kind: Inventory
name: Production
content:            # Ansible YAML inventory; hosts and groups not listed are removed
  all:
    vars: {ntp: pool.ntp.org}
    children:
      web:
        hosts:
          web[01:03]:
---
kind: InventorySource
name: aws
spec:
  inventory: Production
  source: ec2
  credential: aws-prod
---
kind: JobTemplate
name: Deploy web
spec:
  project: web-app
  inventory: Production
  playbook: deploy.yml
  credentials: [prod-ssh]
  labels: [web]
  instance_groups: [default]
  survey_enabled: true
  survey_spec: {name: deploy, description: "", spec: [{question_name: Version, variable: version, type: text, required: true}]}
---
kind: WorkflowJobTemplate
name: Release
nodes:
  - id: deploy
    template: {kind: JobTemplate, name: Deploy web}
    success: [approve]
  - id: approve
    approval: {name: Sign off, timeout: 3600}
    success: [verify]
  - id: verify
    template: {kind: JobTemplate, name: Smoke test}
    limit: web01          # node prompts are plain keys
    when: nodes.deploy.status == 'successful'   # conditions and loops: see workflows.md
    loop: "['web01', 'web02']"
    loop_var: target
---
kind: Schedule
name: Nightly
spec:
  template: {kind: JobTemplate, name: Deploy web}
  rrule: "DTSTART;TZID=Europe/Zurich:20250101T020000 RRULE:FREQ=DAILY"
---
kind: NotificationTemplate
name: Chat
spec:
  notification_type: webhook
  notification_configuration: {url: "https://hooks.example.com/x", http_method: POST, headers: {}}
---
kind: Team
name: Web operators
members: [alice, bob]
```

Kinds are applied in dependency order (Label, Team, Project, Inventory,
InventorySource, NotificationTemplate, JobTemplate, WorkflowJobTemplate,
Schedule) and pruned in reverse. Credentials are referenced, never declared:
secrets stay out of git.
