# Workflow conditions, loops and outputs

Besides AWX's success, failure and always links, a workflow node can carry a
condition and a loop. Both are expressions in the
[expr language](https://expr-lang.org/docs/language-definition), which reads
like Ansible's `when:` clauses. Undefined variables are `nil` rather than errors.

## What an expression sees

- The variables the node's job would get: the node's own extra variables, then
  the artifacts (`set_stats`) of the nodes before it, then the workflow's extra
  variables, later ones winning, as in AWX.
- `nodes`: every node of the workflow by identifier, with `status` (`successful`,
  `failed`, `running`, `pending`, `skipped`...), `failed`, `job` and `artifacts`,
  plus `items` for a loop node that has started.
- `workflow`: `id` and `name` of the workflow job.

```
deploy_env == 'prod' and replicas > 2
'web' in target_groups
nodes.build.status == 'successful' and nodes.build.artifacts.image_tag != nil
(nodes['smoke-test']?.status ?? 'skipped') == 'successful'
```

Identifiers with dashes need brackets (`nodes['smoke-test']`), and `?.` avoids
an error when a node does not exist. `??` must be wrapped in parentheses when
combined with a comparison.

## Conditions (`when`)

A node whose parents lead to it runs only when its condition is true. When it
is false, the node is skipped like a node on a path not taken: it shows "Skipped
by its condition" with the expression (`skip_reason` in the API), and its
children run only through other parents. A skipped node is not a failure.

A condition that cannot be evaluated (for example `nodes.nope.status` for a node
that does not exist) fails the node with the error in its job explanation, so
its failure path runs.

## Loops (`loop`, `loop_var`, `loop_concurrency`)

A loop node runs its template once per item of the list its `loop` expression
gives (`regions`, `['eu', 'us']`, `filter(hosts, # != 'db1')`). Each item is a
separate job named after the item, with the item in `loop_var` (default `item`)
and its position, from 0, in `loop_index`.

- `loop_concurrency` limits how many items run at once; 0 runs them all at once,
  1 runs them in order.
- The node succeeds when every item succeeded and fails otherwise; failed items
  do not stop the remaining ones.
- An empty list succeeds at once.
- The items' artifacts are merged in item order and passed to the next nodes.
- The API shows `loop_items` and `iterations` (the job of each item) on the
  workflow job node. Approval nodes cannot loop.

Conditions are checked before the loop starts, so a node can have both.

## Typed outputs

A playbook passes values to the steps after it with `set_stats`: they become
the job's artifacts, which the next nodes get as variables and see in
`nodes.<id>.artifacts`. A job template can declare those outputs, each with a
name, a type and whether it is required:

```json
"outputs": [
  {"name": "regions", "type": "list", "required": true},
  {"name": "replicas", "type": "integer", "required": false}
]
```

The types are `string`, `integer`, `float`, `boolean`, `list` and `object`. In
the UI, the template form has an Outputs section. Declaring outputs is
optional; a template without them works as before.

### Publishing outputs

```yaml
- ansible.builtin.set_stats:
    data:
      regions: "{{ found_regions }}"
      replicas: "{{ count }}"
  run_once: true
```

- Use `run_once: true` (or `aggregate: false`). Without it the task runs on
  every host and, by default, adds each host's values together: lists are
  concatenated, numbers added and strings joined (`"v1"` becomes `"v1v1v1"` on
  three hosts), all still of the right type.
- `per_host: true` values are kept per host and never reach the artifacts, so
  they cannot be outputs.
- Since ansible-core 2.19, `"{{ count }}"` keeps its type (an integer stays
  one). Older versions turn numbers into strings, which conversion below
  absorbs.

### Checking

When a job of the template ends successfully, its artifacts are checked
against the declaration:

- a required output that is missing or `null` fails the job;
- a value of the wrong type fails the job;
- outputs that are not declared pass unchecked.

The job fails with a message such as `The playbook succeeded but its outputs do
not match the template: "regions" must be a list, not a string ("eu,us").`, in
its explanation and at the end of its output. In a workflow, the failure is the
producing node's, so its failure path runs and the nodes that use the output do
not.

A value is converted when the declared type allows it without guessing: a
string that reads as a number to `integer` or `float`, `"true"` and `"false"`
to `boolean`, a number to `string`, and an integral number to `integer`.
Nothing converts to or from `list` and `object`. The converted values are what
the job's artifacts hold, so the next steps' conditions, loops and variables get
them typed: `replicas > 2` works when the playbook published `"3"`.

Every job of the template is checked, inside a workflow or not, including each
item of a loop and each slice of a sliced job. A loop node merges its items'
artifacts as before.

### Expressions

Saving a node checks its condition and loop against the outputs declared by
the templates of the workflow's nodes:

- `nodes.discover.artifacts.region` is refused when `discover`'s template
  declares outputs but no `region`;
- a loop over `nodes.discover.artifacts.replicas` is refused when `replicas` is
  declared as anything but a list.

References to nodes whose template declares nothing, and plain variables
(which may come from extra variables or a survey), are not checked.

In the workflow editor, "Run only when" and "Items" list the outputs of the
steps before the selected one, with their types; clicking one inserts its
reference. "Items" lists only list outputs.

### Design choices

- The declaration belongs to the job template, so a mismatch fails the job,
  wherever it runs: a green job inside a failed node would hide where the
  problem is.
- Only job templates declare outputs: they are where `set_stats` runs. A nested
  workflow passes its nodes' artifacts through as before.
- Undeclared artifacts pass, so existing playbooks keep working and outputs can
  be declared one at a time.
