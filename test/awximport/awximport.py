#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11,<3.13"
# dependencies = [
#   "awxkit==24.6.1",
#   "setuptools<81",  # awxkit 24.6.1 imports pkg_resources
#   "pyyaml>=6",
# ]
# ///
"""Tests `awxnext import-awx` against a real AWX (.github/workflows/import-awx.yml).

  seed       fills an AWX through its API with what an installation holds
  run-job    syncs a project, launches a job template and waits for it
  snapshot   counts each resource, to show that a dry run writes nothing
  reports    checks the importer's reports: warnings, and dry run against import
  compare    compares the AWX and the awxnext through /api/v2

The comparison goes one way: what AWX has must be in awxnext, with the same
fields, memberships, roles, survey specs and settings. Foreign keys are compared
by name, since imported rows may get new ids. known-differences.yml lists what
differs on purpose, each with its reason. A difference it does not list fails
the run, and so does an entry that no longer matches anything, so the list stays
true.

Passwords come from AWX_PASSWORD and AWXNEXT_PASSWORD.
"""

import argparse
import fnmatch
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import time

import yaml
from awxkit.api.client import Connection

HERE = pathlib.Path(__file__).parent
KNOWN = HERE / "known-differences.yml"

# What the job checks (playbooks/check_secrets.yml). Test values, on a cluster
# that lives for one run.
TOKEN = "token é€ from awx"
BLOB = "line one\nline two"
SURVEY_SECRET = "survey secret from awx"
SCHEDULE_SECRET = "schedule secret from awx"
USER_PASSWORD = "Imported-pw-1"
JOB_TEMPLATE = "check-secrets"
JOB_PROJECT = "awxnext-repo"
PLAYBOOK = "test/awximport/playbooks/check_secrets.yml"


class API:
    """/api/v2 of one server, over awxkit's connection."""

    def __init__(self, url, user, password):
        self.conn = Connection(url)
        self.conn.login(user, password)
        self.url = url

    def request(self, method, path, body=None, params=None, ok=(200, 201, 202, 204)):
        path = "/api/v2/" + path.lstrip("/")
        if method == "get":
            res = self.conn.get(path, query_parameters=params)
        else:
            res = getattr(self.conn, method)(path, json=body)
        if res.status_code not in ok:
            raise SystemExit(f"{method.upper()} {path} {body if body is not None else ''}: {res.status_code} {res.text[:2000]}")
        return res.json() if res.content else {}

    def get(self, path, **params):
        return self.request("get", path, params=params or None)

    def post(self, path, body=None):
        return self.request("post", path, body if body is not None else {})

    def patch(self, path, body):
        return self.request("patch", path, body)

    def all(self, path, **params):
        """Every row of a list, following the pages."""
        out, page = [], 1
        while True:
            data = self.get(path, page_size=200, page=page, **params)
            out.extend(data["results"])
            if not data.get("next"):
                return out
            page += 1

    def one(self, path, **params):
        rows = self.all(path, **params)
        if len(rows) != 1:
            raise SystemExit(f"{path} {params}: {len(rows)} rows")
        return rows[0]

    def wait(self, path, timeout=900):
        """Polls a job or a project until it is done."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            obj = self.get(path)
            if obj["status"] in ("successful", "failed", "error", "canceled"):
                return obj
            time.sleep(3)
        raise SystemExit(f"{path}: still {obj['status']} after {timeout} s")


def awx():
    return API(os.environ["AWX_URL"], "admin", os.environ["AWX_PASSWORD"])


def awxnext():
    return API(os.environ["AWXNEXT_URL"], "admin", os.environ["AWXNEXT_PASSWORD"])


# --- seed ---


def ssh_key():
    """A real private key: a multi-line secret, which AWX validates."""
    with tempfile.TemporaryDirectory() as d:
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", d + "/k"], check=True)
        return pathlib.Path(d, "k").read_text()


def seed(args):
    a = awx()
    ctype = {c["namespace"]: c["id"] for c in a.all("credential_types/", managed="true")}

    # Organizations, users, teams.
    acme = a.post("organizations/", {"name": "Acme", "description": "the main one"})["id"]
    globex = a.post("organizations/", {"name": "Globex", "max_hosts": 5000})["id"]
    users = {}
    for name, extra in {
        "alice": {"first_name": "Alice", "last_name": "Admin", "email": "alice@example.com"},
        "bob": {"email": "bob@example.com"},
        "carol": {"is_system_auditor": True},
        "dave": {"is_superuser": True},
        "erin": {"first_name": "Erin"},
    }.items():
        users[name] = a.post("users/", {"username": name, "password": USER_PASSWORD, **extra})["id"]
    a.post(f"organizations/{acme}/admins/", {"id": users["alice"]})
    a.post(f"organizations/{acme}/users/", {"id": users["bob"]})
    a.post(f"organizations/{globex}/users/", {"id": users["erin"]})
    ops = a.post("teams/", {"name": "Ops", "organization": acme, "description": "operators"})["id"]
    devs = a.post("teams/", {"name": "Devs", "organization": acme})["id"]
    sec = a.post("teams/", {"name": "Sec", "organization": globex})["id"]
    a.post(f"teams/{ops}/users/", {"id": users["bob"]})
    a.post(f"teams/{sec}/users/", {"id": users["erin"]})

    # Credentials: a custom type, several managed ones, a secret lookup, and
    # those a user or a team owns. AWX requires one of the three owners.
    check_type = a.post("credential_types/", {
        "name": "Import check",
        "kind": "cloud",
        "description": "secrets a job reads",
        "inputs": {"fields": [
            {"id": "token", "label": "Token", "type": "string", "secret": True},
            {"id": "blob", "label": "Blob", "type": "string", "secret": True, "multiline": True},
            {"id": "plain", "label": "Plain", "type": "string"},
        ], "required": ["token"]},
        "injectors": {
            "env": {"IMPORT_CHECK_TOKEN": "{{ token }}", "IMPORT_CHECK_FILE": "{{ tower.filename }}"},
            "file": {"template": "{{ blob }}"},
            "extra_vars": {"import_check_plain": "{{ plain }}"},
        },
    })["id"]

    def credential(name, namespace, inputs, **extra):
        body = {"name": name, "credential_type": ctype.get(namespace, namespace), "inputs": inputs}
        if "user" not in extra and "team" not in extra:
            body["organization"] = extra.pop("organization", acme)
        return a.post("credentials/", {**body, **extra})["id"]

    check = credential("acme-check", check_type, {"token": TOKEN, "blob": BLOB, "plain": "plain from awx"})
    machine = credential("acme-machine", "ssh", {
        "username": "root", "password": "machine-pw", "ssh_key_data": ssh_key(),
        "become_method": "sudo", "become_username": "root", "become_password": "become-pw",
    })
    credential("acme-scm", "scm", {"username": "git", "password": "scm-pw"})
    credential("acme-vault", "vault", {"vault_password": "vault-pw", "vault_id": "prod"})
    aws = credential("acme-aws", "aws", {"username": "AKIA", "password": "aws-secret"})
    registry = credential("acme-registry", "registry", {"host": "quay.io", "username": "robot", "password": "reg-pw"})
    galaxy = credential("acme-galaxy", "galaxy_api_token", {"url": "https://galaxy.example.com/", "token": "galaxy-token"})
    kv = credential("acme-kv", "hashivault_kv", {"url": "https://vault.example.com", "token": "vault-token", "api_version": "v2"})
    looked_up = credential("acme-machine-lookup", "ssh", {"username": "root"})
    a.post("credential_input_sources/", {
        "input_field_name": "password", "target_credential": looked_up, "source_credential": kv,
        "metadata": {"secret_path": "secret/data/app", "secret_key": "password", "secret_backend": "secret"},
    })
    alice_cred = credential("alice-personal", "ssh", {"username": "alice", "password": "alice-pw"}, user=users["alice"])
    credential("ops-shared", "ssh", {"username": "ops", "password": "ops-pw"}, team=ops)
    globex_machine = credential("globex-machine", "ssh", {"username": "globex", "password": "globex-pw"}, organization=globex)

    # Execution environments: the job's pulls without a credential, since this
    # registry credential is not a real one, so only a template that never runs
    # uses the other.
    ee = a.post("execution_environments/", {
        "name": "acme-ee", "image": "quay.io/ansible/awx-ee:24.6.1", "organization": acme, "pull": "missing", "description": "acme's",
    })["id"]
    private_ee = a.post("execution_environments/", {
        "name": "acme-private-ee", "image": "quay.io/example/private:1", "organization": acme, "credential": registry, "pull": "always",
    })["id"]
    a.patch(f"organizations/{acme}/", {"default_environment": ee})
    a.post(f"organizations/{acme}/galaxy_credentials/", {"id": galaxy})
    a.post(f"organizations/{acme}/galaxy_credentials/", {"id": a.one("credentials/", name="Ansible Galaxy")["id"]})

    # Instance groups, away from what the job uses: with no instance in them, it
    # would wait forever. It runs on the default group.
    pods = a.post("instance_groups/", {
        "name": "pods", "is_container_group": True, "max_concurrent_jobs": 4, "max_forks": 20,
        "pod_spec_override": "apiVersion: v1\nkind: Pod\nmetadata:\n  namespace: awx\nspec:\n  containers:\n  - image: quay.io/ansible/awx-ee:24.6.1\n    name: worker\n    args: ['ansible-runner', 'worker', '--private-data-dir=/runner']\n",
    })["id"]
    policy = a.post("instance_groups/", {"name": "policy", "policy_instance_percentage": 50, "policy_instance_minimum": 1})["id"]
    a.post(f"organizations/{globex}/instance_groups/", {"id": policy})
    a.post(f"organizations/{globex}/instance_groups/", {"id": pods})

    # Labels, inventories: regular, smart, constructed; groups and hosts.
    inv = a.post("inventories/", {"name": "main", "organization": acme, "description": "hosts",
                                  "variables": json.dumps({"from_inventory": "inventory var"})})["id"]
    web = a.post(f"inventories/{inv}/groups/", {"name": "web", "variables": "from_group: group var\n"})["id"]
    web_a = a.post(f"inventories/{inv}/groups/", {"name": "web-a"})["id"]
    db = a.post(f"inventories/{inv}/groups/", {"name": "db"})["id"]
    a.post(f"groups/{web}/children/", {"id": web_a})
    host = a.post(f"inventories/{inv}/hosts/", {"name": "localhost", "variables": "ansible_connection: local\nfrom_host: host var\n"})["id"]
    a.post(f"groups/{web_a}/hosts/", {"id": host})
    for name, group, enabled in (("web-1", web, True), ("web-2", web_a, False), ("db-1", db, True)):
        h = a.post(f"inventories/{inv}/hosts/", {"name": name, "enabled": enabled, "description": f"{name} host"})["id"]
        a.post(f"groups/{group}/hosts/", {"id": h})
    a.post("inventories/", {"name": "smart-web", "organization": acme, "kind": "smart", "host_filter": "name__icontains=web"})
    constructed = a.post("constructed_inventories/", {
        "name": "constructed-prod", "organization": acme, "limit": "web", "verbosity": 1, "update_cache_timeout": 60,
        "source_vars": "plugin: constructed\nstrict: false\ngroups:\n  shutdown: not (ansible_host is defined)\n",
    })["id"]
    small = a.post("inventories/", {"name": "small", "organization": acme})["id"]
    a.post(f"inventories/{small}/hosts/", {"name": "small-1"})
    a.post(f"inventories/{constructed}/input_inventories/", {"id": inv})
    a.post(f"inventories/{constructed}/input_inventories/", {"id": small})
    big = a.post("inventories/", {"name": "big", "organization": globex})["id"]
    a.post(f"inventories/{big}/instance_groups/", {"id": policy})
    # 100 at a time: AWX's BULK_HOST_MAX_CREATE.
    for start in range(0, args.hosts, 100):
        a.post("bulk/host_create/", {"inventory": big, "hosts": [
            {"name": f"big-{n}.example.com", "variables": f"n: {n}\n"} for n in range(start, min(start + 100, args.hosts))]})

    # Projects: this repository, for the job, and its inventory source.
    project = a.post("projects/", {
        "name": JOB_PROJECT, "organization": acme, "scm_type": "git", "scm_url": args.project_url,
        "scm_branch": args.project_branch, "scm_clean": True, "allow_override": True, "timeout": 300, "default_environment": ee,
    })["id"]
    done = a.wait(f"projects/{project}/")
    if done["status"] != "successful":
        raise SystemExit(f"project sync: {done['status']}")
    a.post("inventory_sources/", {
        "name": "from-project", "inventory": inv, "source": "scm", "source_project": project,
        "source_path": "test/awximport/inventory.ini", "overwrite": True, "update_cache_timeout": 30,
    })
    a.post("inventory_sources/", {"name": "from-ec2", "inventory": inv, "source": "ec2", "credential": aws,
                                  "source_vars": "regions: [eu-west-1]\n", "limit": "web", "verbosity": 2})

    # Job templates: one for the job, one with most fields set.
    jt = a.post("job_templates/", {
        "name": JOB_TEMPLATE, "description": "the secrets reach the job", "job_type": "run",
        "inventory": inv, "project": project, "playbook": PLAYBOOK, "ask_variables_on_launch": True,
        "extra_vars": json.dumps({"from_template": "template var"}),
    })["id"]
    a.post(f"job_templates/{jt}/credentials/", {"id": check})
    survey = {"name": "check", "description": "a password question", "spec": [
        {"question_name": "Secret", "question_description": "a default that is encrypted", "variable": "survey_secret",
         "type": "password", "required": True, "default": SURVEY_SECRET, "min": 0, "max": 64},
        {"question_name": "Colour", "variable": "colour", "type": "multiplechoice", "required": False,
         "default": "blue", "choices": ["blue", "green"]},
    ]}
    a.post(f"job_templates/{jt}/survey_spec/", survey)
    a.patch(f"job_templates/{jt}/", {"survey_enabled": True})
    full = a.post("job_templates/", {
        "name": "deploy", "job_type": "check", "inventory": inv, "project": project, "playbook": PLAYBOOK,
        "forks": 7, "limit": "web", "verbosity": 2, "job_tags": "a,b", "skip_tags": "c", "start_at_task": "first",
        "timeout": 600, "force_handlers": True, "become_enabled": True, "diff_mode": True, "allow_simultaneous": True,
        "use_fact_cache": True, "host_config_key": "callback-key", "job_slice_count": 3, "execution_environment": private_ee,
        "webhook_service": "github", "scm_branch": "main", "prevent_instance_group_fallback": True,
        "ask_limit_on_launch": True, "ask_tags_on_launch": True, "ask_inventory_on_launch": True, "ask_credential_on_launch": True,
        "ask_labels_on_launch": True, "ask_forks_on_launch": True, "ask_instance_groups_on_launch": True,
    })["id"]
    a.post(f"job_templates/{full}/credentials/", {"id": machine})
    a.post(f"job_templates/{full}/labels/", {"name": "prod", "organization": acme})
    a.post(f"job_templates/{full}/labels/", {"name": "web", "organization": acme})
    a.post(f"job_templates/{full}/instance_groups/", {"id": pods})

    # A workflow with an approval node, a job, and a survey.
    wf = a.post("workflow_job_templates/", {
        "name": "release", "organization": acme, "description": "with an approval", "allow_simultaneous": True,
        "extra_vars": "from_workflow: workflow var\n", "ask_variables_on_launch": True, "inventory": inv, "limit": "web",
    })["id"]
    a.post(f"workflow_job_templates/{wf}/survey_spec/", {"name": "w", "description": "", "spec": [
        {"question_name": "Token", "variable": "wf_secret", "type": "password", "required": False, "default": "workflow secret"},
    ]})
    a.patch(f"workflow_job_templates/{wf}/", {"survey_enabled": True})
    gate = a.post(f"workflow_job_templates/{wf}/workflow_nodes/", {"identifier": "gate"})["id"]
    a.post(f"workflow_job_template_nodes/{gate}/create_approval_template/", {"name": "approve", "description": "ship it?", "timeout": 3600})
    first = a.post(f"workflow_job_templates/{wf}/workflow_nodes/", {"identifier": "first", "unified_job_template": jt})["id"]
    after = a.post(f"workflow_job_templates/{wf}/workflow_nodes/", {
        "identifier": "after", "unified_job_template": full, "limit": "web-1", "all_parents_must_converge": True})["id"]
    fail = a.post(f"workflow_job_templates/{wf}/workflow_nodes/", {"identifier": "on-fail", "unified_job_template": project})["id"]
    a.post(f"workflow_job_template_nodes/{first}/success_nodes/", {"id": gate})
    a.post(f"workflow_job_template_nodes/{gate}/success_nodes/", {"id": after})
    a.post(f"workflow_job_template_nodes/{first}/failure_nodes/", {"id": fail})
    a.post(f"workflow_job_template_nodes/{gate}/always_nodes/", {"id": fail})
    # Replaces the template's machine credential: AWX refuses the same one.
    a.post(f"workflow_job_template_nodes/{after}/credentials/", {"id": looked_up})

    # Schedules: a template's, with an encrypted survey answer, a disabled
    # workflow's, and AWX's own on the management jobs are already there.
    rrule = "DTSTART;TZID=Europe/Zurich:20300101T060000 RRULE:FREQ=DAILY;INTERVAL=1"
    a.post(f"job_templates/{jt}/schedules/", {"name": "nightly", "rrule": rrule, "description": "every day",
                                              "extra_data": {"survey_secret": SCHEDULE_SECRET, "colour": "green"}})
    a.post(f"job_templates/{full}/schedules/", {"name": "limited", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=MO;COUNT=4",
                                                "limit": "web-1", "enabled": False})
    a.post(f"workflow_job_templates/{wf}/schedules/", {"name": "weekly", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=WEEKLY;INTERVAL=1"})
    a.post(f"projects/{project}/schedules/", {"name": "hourly", "rrule": "DTSTART:20300101T000000Z RRULE:FREQ=HOURLY;INTERVAL=1"})

    # Notification templates: email and webhook, which awxnext has, and slack,
    # which it skips with a warning.
    email = a.post("notification_templates/", {
        "name": "ops-mail", "organization": acme, "notification_type": "email",
        "notification_configuration": {"host": "smtp.example.com", "port": 587, "username": "mailer", "password": "smtp-pw",
                                       "use_tls": True, "use_ssl": False, "sender": "awx@example.com",
                                       "recipients": ["ops@example.com", "dev@example.com"], "timeout": 30},
        "messages": {"started": {"message": "{{ job.name }} started"}, "success": None, "error": None, "workflow_approval": None},
    })["id"]
    hook = a.post("notification_templates/", {
        "name": "ops-hook", "organization": acme, "notification_type": "webhook",
        "notification_configuration": {"url": "https://hooks.example.com/awx", "username": "u", "password": "hook-pw", "http_method": "PUT",
                                       "headers": {"X-Env": "prod"}, "disable_ssl_verification": True},
    })["id"]
    if not args.trial:
        a.post("notification_templates/", {
            "name": "ops-slack", "organization": acme, "notification_type": "slack",
            "notification_configuration": {"channels": ["#ops"], "token": "xoxb-1"},
        })
    a.post(f"job_templates/{full}/notification_templates_success/", {"id": email})
    a.post(f"job_templates/{full}/notification_templates_error/", {"id": hook})
    a.post(f"projects/{project}/notification_templates_error/", {"id": hook})
    a.post(f"workflow_job_templates/{wf}/notification_templates_approvals/", {"id": email})
    a.post(f"organizations/{acme}/notification_templates_started/", {"id": hook})
    a.post(f"organizations/{acme}/notification_templates_approvals/", {"id": email})

    # Role grants, to users and to teams, on several kinds of object.
    def role(path, name):
        return a.get(path)["summary_fields"]["object_roles"][name]["id"]

    grants = [
        (f"users/{users['bob']}", f"job_templates/{jt}/", "execute_role"),
        (f"users/{users['bob']}", f"inventories/{inv}/", "use_role"),
        (f"users/{users['bob']}", f"credentials/{check}/", "use_role"),
        (f"users/{users['erin']}", f"credentials/{globex_machine}/", "use_role"),
        (f"users/{users['erin']}", f"projects/{project}/", "read_role"),
        (f"users/{users['erin']}", f"workflow_job_templates/{wf}/", "approval_role"),
        (f"users/{users['alice']}", f"organizations/{globex}/", "auditor_role"),
        (f"users/{users['erin']}", f"instance_groups/{policy}/", "use_role"),
        (f"teams/{ops}", f"job_templates/{full}/", "admin_role"),
        (f"teams/{ops}", f"projects/{project}/", "use_role"),
        (f"teams/{ops}", f"credentials/{machine}/", "use_role"),
        (f"teams/{ops}", f"organizations/{acme}/", "workflow_admin_role"),
        (f"teams/{devs}", f"inventories/{inv}/", "adhoc_role"),
        (f"teams/{devs}", f"organizations/{acme}/", "project_admin_role"),
        (f"teams/{sec}", f"organizations/{globex}/", "notification_admin_role"),
        (f"teams/{sec}", f"teams/{ops}/", "read_role"),
    ]
    for who, obj, name in grants:
        a.post(f"{who}/roles/", {"id": role(obj, name)})
    a.post(f"users/{users['bob']}/roles/", {"id": role(f"credentials/{alice_cred}/", "use_role")})

    # Login settings, one of them encrypted.
    a.patch("settings/ldap/", {
        "AUTH_LDAP_SERVER_URI": "ldap://ldap.example.com",
        "AUTH_LDAP_BIND_DN": "cn=svc,dc=example,dc=com",
        "AUTH_LDAP_BIND_PASSWORD": "bind-pw",
        "AUTH_LDAP_START_TLS": True,
        "AUTH_LDAP_USER_SEARCH": ["ou=people,dc=example,dc=com", "SCOPE_SUBTREE", "(uid=%(user)s)"],
        "AUTH_LDAP_GROUP_SEARCH": ["ou=groups,dc=example,dc=com", "SCOPE_SUBTREE", "(objectClass=groupOfNames)"],
        "AUTH_LDAP_USER_ATTR_MAP": {"first_name": "givenName", "email": "mail"},
        "AUTH_LDAP_ORGANIZATION_MAP": {"Acme": {"users": True, "admins": "cn=admins,ou=groups,dc=example,dc=com"}},
        "AUTH_LDAP_TEAM_MAP": {"Ops": {"organization": "Acme", "users": "cn=ops,ou=groups,dc=example,dc=com"}},
    })
    a.patch("settings/saml/", {
        "SOCIAL_AUTH_SAML_SP_ENTITY_ID": "https://awx.example.com",
        "SOCIAL_AUTH_SAML_ORG_INFO": {"en-US": {"name": "acme", "displayname": "Acme", "url": "https://example.com"}},
        "SOCIAL_AUTH_SAML_TECHNICAL_CONTACT": {"givenName": "Tech", "emailAddress": "tech@example.com"},
    })
    a.patch("settings/github/", {"SOCIAL_AUTH_GITHUB_KEY": "gh-key", "SOCIAL_AUTH_GITHUB_SECRET": "gh-secret"})
    print("seeded")


# --- jobs ---


def run_job(args):
    """Syncs the project and launches the template, on whichever server."""
    a = awxnext() if args.server == "awxnext" else awx()
    project = a.one("projects/", name=JOB_PROJECT)
    update = a.post(f"projects/{project['id']}/update/")
    done = a.wait(f"project_updates/{update['project_update']}/")
    if done["status"] != "successful":
        print(a.conn.get(f"/api/v2/project_updates/{update['project_update']}/stdout/", query_parameters={"format": "txt"}).text)
        raise SystemExit(f"project sync on {args.server}: {done['status']}")
    jt = a.one("job_templates/", name=JOB_TEMPLATE)
    # As AWX's UI asks for a password's stored default: AWX refuses a required
    # question left out, even if it has a default.
    job = a.post(f"job_templates/{jt['id']}/launch/", {"extra_vars": {"survey_secret": "$encrypted$"}})
    done = a.wait(f"jobs/{job['job']}/")
    print(a.conn.get(f"/api/v2/jobs/{job['job']}/stdout/", query_parameters={"format": "txt"}).text)
    if done["status"] != "successful":
        raise SystemExit(f"job on {args.server}: {done['status']}: {done.get('job_explanation', '')}")
    print(f"job on {args.server}: successful")


# --- reports ---


def read_report(path):
    """The JSON report that import-awx prints."""
    text = pathlib.Path(path).read_text()
    return json.JSONDecoder().raw_decode(text[text.index("{"):])[0]


def known_differences():
    return yaml.safe_load(KNOWN.read_text()) or {}


def reports(args):
    dry, real = read_report(args.dry_run), read_report(args.report)
    problems = []
    if dry["counts"] != real["counts"]:
        problems.append(f"the dry run and the import count differently: {dry['counts']} and {real['counts']}")
    if sorted(dry["warnings"] or []) != sorted(real["warnings"] or []):
        problems.append("the dry run and the import warn differently")
    expected = known_differences().get("warnings") or []
    used = set()
    for w in real["warnings"] or []:
        hit = next((e for e in expected if fnmatch.fnmatchcase(w, e["match"])), None)
        if hit:
            used.add(hit["match"])
        else:
            problems.append(f"warning not in known-differences.yml: {w}")
    problems += [f"known warning no longer raised: {e['match']}" for e in expected if e["match"] not in used]
    lines = ["## Import report", "", "| Object | Imported |", "|---|---|"]
    lines += [f"| {k} | {v} |" for k, v in sorted(real["counts"].items())]
    lines += ["", f"{len(real['warnings'] or [])} warnings:", ""] + [f"- {w}" for w in real["warnings"] or []]
    summary("\n".join(lines) + "\n")
    finish(problems)


def summary(text):
    print(text)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as f:
            f.write(text + "\n")


def finish(problems):
    if problems:
        summary("## Problems\n\n" + "\n".join(f"- {p}" for p in problems) + "\n")
        sys.exit(1)


def snapshot(args):
    a = awxnext()
    counts = {r: a.get(r, page_size=1)["count"] for r in (
        "organizations", "users", "teams", "credentials", "credential_types", "execution_environments", "instance_groups",
        "inventories", "hosts", "groups", "projects", "job_templates", "workflow_job_templates", "schedules", "notification_templates", "labels")}
    pathlib.Path(args.out).write_text(json.dumps(counts, indent=1, sort_keys=True) + "\n")


# --- compare ---

# Rows' own identity and what running jobs change: job history is not imported.
IGNORED = {
    "id", "url", "related", "summary_fields", "created", "modified",
    "status", "last_job_run", "last_job_failed", "next_job_run", "last_updated", "last_update_failed",
    "current_job", "last_job", "current_update", "last_update", "scm_revision", "last_job_host_summary", "ansible_facts_modified",
    "unified_job_template",  # in a schedule's key
    "has_active_failures", "hosts_with_active_failures", "groups_with_active_failures", "inventory_sources_with_failures",
    "last_login", "ldap_dn", "password",
    "capacity", "consumed_capacity", "percent_capacity_remaining", "jobs_running", "jobs_total", "instances",
}

# Foreign key fields and what they point at.
REFS = {
    "organization": "organizations", "inventory": "inventories", "project": "projects", "credential": "credentials",
    "credential_type": "credential_types", "execution_environment": "execution_environments",
    "default_environment": "execution_environments", "source_project": "projects", "webhook_credential": "credentials",
    "signature_validation_credential": "credentials", "source_credential": "credentials", "target_credential": "credentials",
}


class Side:
    """One server's objects, by natural key."""

    def __init__(self, api):
        self.api, self.objs, self.names = api, {}, {}

    def load(self, typ, key, **params):
        self.objs[typ], self.names[typ] = {}, {}
        for o in self.api.all(typ + "/", **params):
            k = key(self, o)
            self.objs[typ][k] = o
            self.names[typ][o["id"]] = k

    def name(self, typ, id_):
        return None if id_ is None else self.names.get(typ, {}).get(id_, f"unknown {typ} {id_}")

    def rows(self, path):
        return self.api.all(path)


def in_org(field="name"):
    return lambda s, o: f"{s.name('organizations', o.get('organization'))}/{o[field]}"


def in_inventory(s, o):
    return f"{s.name('inventories', o['inventory'])}/{o['name']}"


def schedule_key(s, o):
    return f"{o['summary_fields']['unified_job_template']['name']}/{o['name']}"


# In dependency order: a key may use an earlier type's names.
RESOURCES = [
    ("organizations", lambda s, o: o["name"]),
    ("users", lambda s, o: o["username"]),
    ("teams", in_org()),
    ("credential_types", lambda s, o: f"{o['kind']}/{o['name']}"),
    ("credentials", in_org()),
    ("execution_environments", in_org()),
    ("instance_groups", lambda s, o: o["name"]),
    ("labels", in_org()),
    ("inventories", in_org()),
    ("hosts", in_inventory),
    ("groups", in_inventory),
    ("projects", in_org()),
    ("inventory_sources", in_inventory),
    ("job_templates", in_org()),
    ("workflow_job_templates", in_org()),
    ("system_job_templates", lambda s, o: o["job_type"]),
    ("schedules", schedule_key),
    ("notification_templates", in_org()),
]

NOTIFICATIONS = {"notification_templates_started": "notification_templates", "notification_templates_success": "notification_templates",
                 "notification_templates_error": "notification_templates"}

# Sublists compared by the names they hold. Those whose order means something
# (which group or credential comes first) are kept in order.
SUBLISTS = {
    "organizations": {"users": "users", "admins": "users", "teams": "teams", "galaxy_credentials": "credentials",
                      "instance_groups": "instance_groups", "notification_templates_approvals": "notification_templates", **NOTIFICATIONS},
    "teams": {"users": "users"},
    "inventories": {"instance_groups": "instance_groups", "input_inventories": "inventories"},
    "groups": {"hosts": "hosts", "children": "groups"},
    "job_templates": {"credentials": "credentials", "labels": "labels", "instance_groups": "instance_groups", **NOTIFICATIONS},
    "workflow_job_templates": {"labels": "labels", "notification_templates_approvals": "notification_templates", **NOTIFICATIONS},
    "projects": NOTIFICATIONS,
}
ORDERED = {"galaxy_credentials", "instance_groups", "input_inventories"}

LOGIN_SETTINGS = ["authentication", "ldap", "saml", "oidc", "github", "github-org", "github-team", "github-enterprise",
                  "github-enterprise-org", "github-enterprise-team", "google-oauth2", "azuread-oauth2"]


def normal(side, obj):
    """An object's comparable fields, foreign keys by name."""
    out = {}
    for k, v in obj.items():
        if k in IGNORED:
            continue
        out[k] = side.name(REFS[k], v) if k in REFS else v
    return out


def role_set(rows):
    return sorted(f"{r['summary_fields'].get('resource_type', '')}/{r['summary_fields'].get('resource_name', '')}/{r['name']}" for r in rows)


def nodes(side, wf_id):
    """A workflow's graph, by identifier."""
    rows = side.rows(f"workflow_job_templates/{wf_id}/workflow_nodes/")
    ids = {r["id"]: r["identifier"] for r in rows}
    out = {}
    for r in rows:
        ujt = r["summary_fields"].get("unified_job_template") or {}
        n = {k: v for k, v in normal(side, r).items() if k not in ("workflow_job_template", "unified_job_template")}
        n["unified_job_template"] = [ujt.get("name"), ujt.get("unified_job_type")]
        for kind in ("success_nodes", "failure_nodes", "always_nodes"):
            n[kind] = sorted(ids.get(i, str(i)) for i in r[kind])
        n["credentials"] = sorted(side.name("credentials", c["id"]) for c in side.rows(f"workflow_job_template_nodes/{r['id']}/credentials/"))
        out[r["identifier"]] = n
    return out


class Comparison:
    def __init__(self, awx_api, next_api):
        self.awx, self.next = Side(awx_api), Side(next_api)
        self.diffs, self.extra, self.matched = [], [], {}

    def diff(self, path, a, b):
        if a != b:
            self.diffs.append({"path": path, "awx": a, "awxnext": b})

    def same(self, path, a, b):
        """Compares two dicts key by key; keys on one side only are not compared."""
        for k in sorted(set(a) & set(b)):
            self.diff(f"{path}/{k}", a[k], b[k])

    def run(self):
        # All the names first: an organization's sublists name what comes after it.
        for typ, key in RESOURCES:
            self.awx.load(typ, key)
            self.next.load(typ, key)
        for typ, _ in RESOURCES:
            self.compare_type(typ)
        self.roles()
        self.details()
        self.settings()
        return self

    def compare_type(self, typ):
        a, b = self.awx.objs[typ], self.next.objs[typ]
        self.matched[typ] = [len(a), len(b)]
        for k, o in a.items():
            if k not in b:
                self.diffs.append({"path": f"{typ}/{k}", "awx": "exists", "awxnext": "missing"})
                continue
            self.same(f"{typ}/{k}", normal(self.awx, o), normal(self.next, b[k]))
            for name, target in SUBLISTS.get(typ, {}).items():
                self.sublist(f"{typ}/{k}/{name}", f"{typ}/{o['id']}/{name}/", f"{typ}/{b[k]['id']}/{name}/", target, name in ORDERED)
        self.extra += [f"{typ}/{k}" for k in b if k not in a]

    def sublist(self, path, awx_path, next_path, target, ordered):
        def names(side, p):
            got = [side.name(target, r["id"]) for r in side.rows(p)]
            return got if ordered else sorted(got)

        self.diff(path, names(self.awx, awx_path), names(self.next, next_path))

    def roles(self):
        for typ in ("users", "teams"):
            for k, o in self.awx.objs[typ].items():
                other = self.next.objs[typ].get(k)
                if other:
                    self.diff(f"{typ}/{k}/roles", role_set(self.awx.rows(f"{typ}/{o['id']}/roles/")),
                              role_set(self.next.rows(f"{typ}/{other['id']}/roles/")))

    def details(self):
        for typ in ("job_templates", "workflow_job_templates"):
            for k, o in self.awx.objs[typ].items():
                other = self.next.objs[typ].get(k)
                if not other:
                    continue
                self.diff(f"{typ}/{k}/survey_spec", self.awx.api.get(f"{typ}/{o['id']}/survey_spec/"),
                          self.next.api.get(f"{typ}/{other['id']}/survey_spec/"))
                self.diff(f"{typ}/{k}/webhook_key", self.awx.api.get(f"{typ}/{o['id']}/webhook_key/"),
                          self.next.api.get(f"{typ}/{other['id']}/webhook_key/"))
        for k, o in self.awx.objs["workflow_job_templates"].items():
            other = self.next.objs["workflow_job_templates"].get(k)
            if other:
                a, b = nodes(self.awx, o["id"]), nodes(self.next, other["id"])
                self.diff(f"workflow_job_templates/{k}/nodes", sorted(a), sorted(b))
                for n in sorted(set(a) & set(b)):
                    self.same(f"workflow_job_templates/{k}/nodes/{n}", a[n], b[n])
        for k, o in self.awx.objs["credentials"].items():
            other = self.next.objs["credentials"].get(k)
            if other:
                def sources(side, cid):
                    return sorted(({"input_field_name": r["input_field_name"], "metadata": r["metadata"],
                                    "source_credential": side.name("credentials", r["source_credential"])}
                                   for r in side.rows(f"credentials/{cid}/input_sources/")), key=lambda r: r["input_field_name"])
                self.diff(f"credentials/{k}/input_sources", sources(self.awx, o["id"]), sources(self.next, other["id"]))

    def settings(self):
        for category in LOGIN_SETTINGS:
            a, b = self.awx.api.get(f"settings/{category}/"), self.next.api.get(f"settings/{category}/")
            self.same(f"settings/{category}", a, b)

    def logins(self):
        """Imported users log in with the password they had in AWX."""
        for user in ("alice", "bob", "carol", "dave", "erin"):
            res = Connection(self.next.api.url)
            res.login(user, USER_PASSWORD)
            me = res.get("/api/v2/me/")
            if me.status_code != 200:
                self.diffs.append({"path": f"users/{user}/login", "awx": "logs in", "awxnext": me.status_code})
        return self

    def dump(self, d):
        d = pathlib.Path(d)
        d.mkdir(exist_ok=True)
        for name, side in (("awx", self.awx), ("awxnext", self.next)):
            (d / f"{name}.json").write_text(json.dumps(side.objs, indent=1, sort_keys=True, default=str))


def compare(args):
    c = Comparison(awx(), awxnext()).run().logins()
    if args.out:
        c.dump(args.out)
    known = known_differences().get("differences") or []
    used, unlisted, listed = set(), [], []
    for d in c.diffs:
        hit = next((e for e in known if fnmatch.fnmatchcase(d["path"], e["match"])), None)
        if hit:
            used.add(hit["match"])
            listed.append((d, hit))
        else:
            unlisted.append(d)
    stale = [e["match"] for e in known if e["match"] not in used]
    if args.out:
        (pathlib.Path(args.out) / "differences.json").write_text(json.dumps(
            {"unlisted": unlisted, "listed": [d for d, _ in listed], "extra_in_awxnext": c.extra}, indent=1, default=str))
    show = lambda v: re.sub(r"\s+", " ", json.dumps(v, default=str))[:300].replace("|", "\\|")
    lines = ["## AWX and awxnext through /api/v2", "", "| Resource | AWX | awxnext |", "|---|---|---|"]
    lines += [f"| {t} | {a} | {b} |" for t, (a, b) in c.matched.items()]
    lines += ["", f"{len(c.diffs)} differences: {len(listed)} listed in known-differences.yml, {len(unlisted)} not.", ""]
    if unlisted:
        lines += ["| Where | AWX | awxnext |", "|---|---|---|"]
        lines += [f"| `{d['path']}` | `{show(d['awx'])}` | `{show(d['awxnext'])}` |" for d in unlisted[:300]]
    if c.extra:
        lines += ["", f"In awxnext only ({len(c.extra)}): " + ", ".join(c.extra[:40])]
    summary("\n".join(lines) + "\n")
    finish([f"{len(unlisted)} differences not listed"] * bool(unlisted) + [f"known difference no longer found: {m}" for m in stale])


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("seed")
    s.add_argument("--hosts", type=int, default=2000)
    s.add_argument("--project-url", default="https://github.com/damhau/awxnext")
    s.add_argument("--project-branch", default="main")
    s.add_argument("--trial", action="store_true", help="to try this script on an awxnext: leave out what only AWX accepts")
    s.set_defaults(fn=seed)
    s = sub.add_parser("run-job")
    s.add_argument("server", choices=["awx", "awxnext"])
    s.set_defaults(fn=run_job)
    s = sub.add_parser("snapshot")
    s.add_argument("--out", required=True)
    s.set_defaults(fn=snapshot)
    s = sub.add_parser("reports")
    s.add_argument("dry_run")
    s.add_argument("report")
    s.set_defaults(fn=reports)
    s = sub.add_parser("compare")
    s.add_argument("--out")
    s.set_defaults(fn=compare)
    args = p.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
