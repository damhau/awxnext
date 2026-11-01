# Security policy

awxnext stores credentials (SSH keys, cloud and vault secrets, tokens) and runs playbooks on the
hosts it manages. A flaw in it can expose those secrets, or let someone run code on those hosts
or on awxnext's own servers. Please report one privately, as below.

## Supported versions

awxnext has no releases yet. Only the latest image built from `main` is supported:
`ghcr.io/damhau/awxnext-all-in-one:main`, also tagged `main-<commit time>-<commit>`. Fixes go
into `main` and reach the next image; older images are not patched. Check that a flaw is still
there in the latest image before reporting it, if you can.

## Reporting a vulnerability

Report it through GitHub: the repository's **Security** tab, then **Report a vulnerability**
(<https://github.com/damhau/awxnext/security/advisories/new>). Only the maintainer sees the
report. Please do not open a public issue, pull request or discussion about it.

Include what you can of:

- the version (the image tag, or the commit);
- what an attacker needs (a user account and its roles, network access, a crafted project or
  inventory...) and what they gain;
- the steps to reproduce it, or a proof of concept;
- a fix, if you have one in mind.

## What to expect

- **Acknowledgement within 7 days.**
- An assessment, and a fix or the reason there will be none, in the same private advisory, where
  we can discuss it with you.
- Once the fix is in an image from `main`, the advisory is published, with credit to you unless
  you prefer otherwise, and a CVE when the flaw warrants one.

Please give us the time to ship the fix before disclosing the flaw yourself.

## Out of scope

- Vulnerabilities in AWX itself, Ansible, ansible-runner or an execution environment image:
  report them to their projects. A way through awxnext to reach them is in scope.
- What a user can already do with the rights they were given: running a playbook on the hosts
  of an inventory they may use, or reading the output of a job they may see.
- Known vulnerabilities in dependencies that awxnext's code does not reach: CI checks for them
  daily (docs/operations.md, "Security scanning") and Dependabot proposes the upgrades.
