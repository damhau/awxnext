# Example rulebooks

The rulebooks the import is tested with, copied unchanged, each directory with its
project's Apache-2.0 license:

- `ansible-rulebook-1.3.2/`: `tests/examples/*.yml` of the ansible-rulebook 1.3.2 sdist
  on PyPI (`ansible_rulebook-1.3.2.tar.gz`), from https://github.com/ansible/ansible-rulebook.
- `ansible.eda-2.13.0/`: `extensions/eda/rulebooks/*.yml` of the ansible.eda 2.13.0
  collection on Ansible Galaxy, from https://github.com/ansible/event-driven-ansible.

`TestExamples` translates each one, and compares the reports with `../examples.golden.json`.
