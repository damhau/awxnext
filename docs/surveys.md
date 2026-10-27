# Survey additions

Surveys keep AWX's format; the keys below are additions that AWX clients
ignore. The server checks them when the survey is saved and again at launch.

## Choices from the system (`choices_from`)

A `multiplechoice` or `multiselect` question can list its choices when someone
launches, instead of a fixed `choices` list:

| `choices_from` | Choices |
|---|---|
| `{type: hosts}` | Host names of the inventory the job runs on (including one picked at launch) |
| `{type: groups, inventory: 3}` | Group names of inventory 3 |
| `{type: git_refs, refs: tags}` | Tags of the template's project, newest first; `branches` or `all` |
| `{type: git_refs, project: 7, refs: branches}` | Branches of project 7 |
| `{type: resource, resource: inventories, field: name, filter: {organization: 1}}` | Any API list, filtered with AWX filters |

Choices are listed as the launching user, so they only include objects that
user can see, up to 500. Git refs come from the project's last sync. The UI
reads them from `GET /api/v2/job_templates/<id>/survey_choices/?variable=<name>`
(`&inventory=<id>` for an inventory picked at launch); workflow job templates
have the same endpoint. An answer outside the list is rejected at launch.

## Conditional questions (`show_if`)

```yaml
- question_name: Change ticket
  variable: ticket
  type: text
  required: true
  show_if: {variable: env, values: [prod, staging]}
```

The question is shown, and required, only when the answer to an earlier
question (`env`) is one of `values`; for a `multiselect`, when any of its answers
is. Otherwise the question is skipped and an answer to it is dropped. A
question that depends on a hidden question is hidden too.

## Answer format (`pattern`)

Text, textarea and password questions can require their answer to match a
regular expression (`pattern`, matched against the whole answer, RE2 syntax).
`pattern_hint` is the message shown when it does not:

```yaml
pattern: "CHG-[0-9]+"
pattern_hint: Use a change number like CHG-123
```
