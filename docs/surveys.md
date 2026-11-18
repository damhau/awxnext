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
| `filter` | Whether to list an item, from `item`, `value`, `label`, `answers` (the earlier answers, by variable) and `user` (the user's `id`, `username` and `teams`, by name). Empty: all. | `item.site == answers.datacenter` |

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
