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
| `{type: source, source: 4}` | The list of choice source 4, from a JSON API or a job ([Choice sources](#choice-sources)) |

Choices are listed as the launching user, so they only include objects that
user can see, up to 500. Git refs come from the project's last sync. The UI
reads them from `GET /api/v2/job_templates/<id>/survey_choices/?variable=<name>`
(`&inventory=<id>` for an inventory picked at launch); workflow job templates
have the same endpoint. An answer outside the list is rejected at launch.

`&answers=<JSON object>` gives the answers so far. Only the answers to earlier
questions count, as with `show_if`: a question's choices never depend on a later
one. Password answers are left out. When a list shows labels other than its values, the reply has `labels`, the
label of each value.

## Choice sources

A choice source is an organization's list of choices from outside awxnext: VLANs
from NetBox, applications from a CMDB, or golden images that a job finds. Any
survey of the organization's templates can use it as
`choices_from: {type: source, source: 4}`, so the launch form, the operator view
and the portal list it.

To create a source, post to `/api/v2/choice_sources/`. `Organization Admin` and
`ChoiceSource Admin` change a source; `ChoiceSource Use` lets a survey owner pick
it without seeing its credential. A survey that newly lists a source needs `Use`
on it. A source that a survey lists can't be deleted: take it out of the survey
first.

### Kinds

A source's `kind` sets where the list comes from:

- **`http`:** a JSON API that the server calls, never the browser. Set `url`,
  and optionally `credential` and `parameters`.
- **`job`:** what a job template's run publishes with `set_stats`. Set
  `job_template`, which takes `Execute`, and optionally `refresh_minutes`.

| Field | Kind | What it sets |
|---|---|---|
| `url` | `http` | The API's URL. Its host must be listed in `CHOICE_SOURCE_HOSTS`. |
| `credential` | `http` | How awxnext authenticates: an `HTTP API Token` credential (a header, `Authorization: Bearer <token>` by default), `HTTP API Basic Authentication`, or `OAuth 2.0 Client Credentials` (awxnext gets a token from `token_url`, whose host must be listed too). Credential lookups work as for jobs. You need `Use` on the credential. |
| `parameters` | `http` | Query parameters added to the URL, by name. A value can use the earlier answers and the user: `{"site": "{{ answers.datacenter }}", "owner": "{{ user.username }}"}`. Values are URL-encoded; an unanswered question gives an empty value. Password answers are never passed. |
| `cache_seconds` | `http` | How long awxnext keeps a list before calling the API again for the same parameters. Default: 300. |
| `job_template` | `job` | The template whose run publishes the list. |
| `refresh_minutes` | `job` | Minutes between refreshes. Default: 0, only by hand. |

A job source has one list for everyone: its template runs without answers, and
only `filter` narrows the list. A refresh runs the template as a job with the
launch type `choice_source`. When a refresh fails or publishes no list, the
source keeps its last list: `last_error` says why, `last_job` is the refresh
that failed, and `last_refresh` and `last_refresh_job` are when and by which job
the list was last published.

### Reading the list

Four [expr](https://expr-lang.org/docs/language-definition) expressions read
the API's reply, or the job's `set_stats` data, as `data`:

| Field | What it gives | Example |
|---|---|---|
| `items` | The list, from `data`. Empty: `data` itself must be a list. | `data.results` |
| `value` | An item's answer, a string or a number, from `item`. Empty: the item itself. | `string(item.vid)` |
| `label` | What people see, from `item` and `value`. Empty: the value. | `item.name + " (" + value + ")"` |
| `filter` | Whether to list an item, from `item`, `value`, `label`, `answers` (the earlier answers, by variable) and `user` (the user's `id`, `username`, `first_name`, `last_name`, `email` and `teams`, by name). Empty: all. | `item.site == answers.datacenter` |

Duplicate and empty values are left out. A list holds at most 500 items.

### Limits and failures

awxnext calls an API only within these limits:

- It calls only the hosts listed in `CHOICE_SOURCE_HOSTS` (Settings, Jobs):
  `netbox.example.com`, `netbox.example.com:8443`, `*.example.com`, or
  `http://cmdb.cmdb.svc:8080` for plain HTTP. An entry without a scheme
  matches https only. Empty, the default: http sources call nothing. Redirects
  must stay on listed hosts.
- It refuses loopback, link-local and unspecified addresses and cloud metadata
  services, even for a listed host, unless `CHOICE_SOURCE_ALLOWED_LOCAL_ADDRESSES`
  lists them: an address (`127.0.0.1`), a network (`127.0.0.0/8`), or an
  address and port (`127.0.0.1:8080`).
- A reply must answer within 5 seconds, with a 2xx status and at most 1 MB of
  JSON.

When the API can't be reached, awxnext lists the last list it got for the same
parameters, for up to a day. With nothing cached, the question lists nothing and
says why, and a launch that answers it is refused with that reason.

At launch, the answer is checked against the cached list, or a fresh one. A
catalog request keeps the label each answer was shown with (`answer_labels`), so
the request still shows it after the API changes.

### Testing a source

`POST /api/v2/choice_sources/N/test/` lists the source as a survey would, with
`answers` in the body for the parameters and the filter. An `http` source calls
the API without using or filling the cache. The reply gives `count`, the first
50 `choices` with their labels, a `sample` of three items to write the
expressions from, the `url` called, and `error`. When the expressions can't read
the reply, `reply` has its first 2,000 characters.

`POST /api/v2/choice_sources/N/refresh/` empties an `http` source's cache, or
launches a `job` source's refresh and returns its job (`409` while one runs).

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

## Computed questions (`value_from`)

A question with `value_from` is never asked: the server computes its answer
when the template launches, from an
[expr](https://expr-lang.org/docs/language-definition) expression. Use it for
values worked out rather than asked, such as a hostname built from the
requester's name or the gateway of a picked VLAN:

```yaml
- question_name: VLAN
  variable: vlan
  type: multiplechoice
  required: true
  choices_from: {type: source, source: 12}
- question_name: Hostname
  variable: hostname
  type: text
  required: true
  value_from: 'answers.env + "-" + user.username + "-" + string(item("vlan").vid)'
- question_name: Gateway
  variable: gateway
  type: text
  required: true
  value_from: 'item("vlan").gateway'
- question_name: Database password
  variable: db_password
  type: password
  required: true
  value_from: password(24)
```

An expression reads:

| Name | What it is |
|---|---|
| `item("vlan")` | The item picked in an earlier `multiplechoice` question, as its choice source listed it (`item.vid`, `item.gateway`). For a `multiselect`, the list of picked items. For a list that isn't from a choice source, the answer itself. |
| `answers` | The answers to the earlier questions, by variable, computed ones included. Password answers aren't in it. |
| `user` | Who launches: `id`, `username`, `first_name`, `last_name`, `email` and `teams` (names). For a workflow's node, the user who launched the workflow. |
| `team` | For a portal request, the team it's for: `id` and `name`. Otherwise `nil`. |
| `request` | For a portal request, and the nodes of its workflow: `id`, `lifetime_days` and `expires_at` (RFC 3339, `nil` when the item keeps nothing). Otherwise `nil`. |
| `password(n)` | A random password of `n` letters and digits, from 8 to 128. Only on a `password` question. |

Use `?.` and `??` where a value can be `nil`: `team?.name ?? user.username`,
`request?.expires_at ?? "never"`.

When you save the survey, the server refuses:

- `value_from` on a `multiplechoice` or `multiselect` question, or with a
  `default`;
- `item()` of a question that isn't an earlier list, and `answers` of a question
  that isn't earlier or is a password;
- `password()` outside a `password` question, and a length outside 8 to 128.

At launch:

- The answer is converted to the question's type: a number or a boolean
  becomes text for `text`, `textarea` and `password`; `integer` and `float`
  take numbers. `min`, `max` and `pattern` don't apply.
- An expression that fails, or gives `nil` for a required question, refuses
  the launch: `Could not compute 'hostname': …`. For a question that isn't
  required, `nil` leaves the variable unset.
- A launch, a schedule, a portal request or a catalog item that sets a
  computed variable is refused: `'hostname' is computed by the server and
  can't be set at launch.` A workflow's node, a webhook or a callback that
  passes one is ignored, and the computed value wins.
- `show_if` applies as to any question.

A generated password is stored encrypted, shown as `$encrypted$` in the job's
variables, and masked in its output, as a password answer is
([Secrets in job output](operations.md#secrets-in-job-output)). A relaunch
reuses it. AWX clients ignore `value_from`, as they ignore `choices_from`: the
launch form of the `awx` CLI and the collection doesn't ask computed questions,
because they aren't in `variables_needed_to_start`.
