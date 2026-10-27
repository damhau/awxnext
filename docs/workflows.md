# Workflow conditions and loops

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
