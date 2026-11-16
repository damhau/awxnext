# secubot state

This orphan branch is written by the secubot publish job. `suppressions.yml` lists
findings that triage and the adversarial review agreed are false positives, and (with
`source: upstream`) dependency findings that no released version fixes yet: these lapse as
soon as a scanner reports a fixed version. Every entry has a reason and an expiry date;
expired entries are dropped and re-triaged.

Delete an entry (or this branch) to force re-triage. Permanent suppressions belong in
`.secfix.yml` on the default branch, where they are code-reviewed.
