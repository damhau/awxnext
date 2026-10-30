#!/usr/bin/env python3
"""Fill a development awxnext with demo content for UI work.

    AWXNEXT_URL=http://localhost:8052 uv run --no-project scripts/seed-demo.py [--jobs]

or, with the Docker Compose install (the script is in the image):

    docker compose exec awxnext seed-demo --jobs

Creates an organization with folders, teams, users, a git project (a local
repository of small playbooks that run with ansible_connection=local),
inventories, job templates with surveys, a workflow with an approval and
schedules. With --jobs it also launches a handful of jobs, some failing.
Only uses the public API; safe to run twice (existing objects are reused).

Signs in as AWXNEXT_AUTH ("user:password"), or AWXNEXT_ADMIN_USER and
AWXNEXT_ADMIN_PASSWORD, or admin:admin. The playbook repository is created
in AWXNEXT_DEMO_REPO (default: a directory in the system temp dir); it must
be on the machine that runs awxnext, since the project clones it by path.
"""
import base64
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

URL = os.environ.get("AWXNEXT_URL", "http://localhost:8052").rstrip("/")
_creds = os.environ.get("AWXNEXT_AUTH") or "%s:%s" % (os.environ.get("AWXNEXT_ADMIN_USER") or "admin", os.environ.get("AWXNEXT_ADMIN_PASSWORD") or "admin")
AUTH = "Basic " + base64.b64encode(_creds.encode()).decode()


def call(method, path, body=None, ok=(200, 201, 202, 204)):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(URL + "/api/v2/" + path.lstrip("/"), data=data, method=method,
                                 headers={"Authorization": AUTH, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        if e.code in ok:
            return json.loads(raw) if raw else {}
        raise SystemExit(f"{method} {path}: {e.code} {raw}")


def find(res, **params):
    q = "&".join(f"{k}={urllib.request.quote(str(v))}" for k, v in params.items())
    r = call("GET", f"{res}/?{q}")
    return r["results"][0] if r["count"] else None


def ensure(res, lookup, body):
    found = find(res, **lookup)
    if found:
        return found
    return call("POST", f"{res}/", body)


PLAYS = {
    "deploy/rolling.yml": """- name: Rolling deploy {{ app_name | default('app') }}
  hosts: all
  gather_facts: false
  serial: "{{ serial | default(8) }}"
  max_fail_percentage: 25
  tasks:
    - name: Pull image
      ansible.builtin.command: "sleep 0.{{ 3 + (inventory_hostname | length) % 5 }}"
    - name: Drain from load balancer
      ansible.builtin.command: "true"
    - name: Render config
      ansible.builtin.copy:
        content: "version={{ app_version | default('v1') }}\\n"
        # The job's own temporary directory: jobs run as job users of their
        # own and cannot replace each other's files in /tmp.
        dest: "{{ lookup('env', 'TMPDIR') | default('/tmp', true) }}/awxnext-demo-{{ inventory_hostname }}.conf"
    - name: Wait for /healthz
      ansible.builtin.assert:
        that: inventory_hostname not in (fail_hosts | default([]))
        fail_msg: "Status code was 503 and not [200]: HTTP Error 503: Service Unavailable"
    - name: Enable in load balancer
      ansible.builtin.debug:
        msg: "{{ inventory_hostname }} back in rotation"
""",
    "deploy/rollback.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: Roll back to previous release
      ansible.builtin.debug:
        msg: rolled back
""",
    "tests/smoke.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: GET /healthz
      ansible.builtin.debug:
        msg: ok
    - name: Checkout flow
      ansible.builtin.command: "true"
      changed_when: false
""",
    "ops/scale_workers.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: Scale workers
      ansible.builtin.debug:
        msg: "replicas={{ replicas | default(4) }}"
""",
    "db/pg_minor_upgrade.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: Upgrade postgres packages
      ansible.builtin.command: "sleep 0.2"
""",
    "edge/reload_certs.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: Reload nginx
      ansible.builtin.command: "true"
""",
    "cache/flush.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: FLUSHALL
      ansible.builtin.debug:
        msg: flushed
""",
    "patching/patch.yml": """- hosts: all
  gather_facts: false
  tasks:
    - name: Apply updates
      ansible.builtin.command: "sleep 0.1"
    - name: Reboot if needed
      ansible.builtin.debug:
        msg: no reboot needed
""",
}


def make_repo():
    base = os.environ.get("AWXNEXT_DEMO_REPO") or os.path.join(tempfile.gettempdir(), "awxnext-demo-repo")
    if os.path.isdir(os.path.join(base, ".git")):
        return base
    os.makedirs(base, exist_ok=True)
    for name, content in PLAYS.items():
        p = os.path.join(base, name)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w") as f:
            f.write(content)
    git = ["git", "-c", "user.name=Demo", "-c", "user.email=demo@example.com", "-c", "init.defaultBranch=main"]
    subprocess.run(git + ["init", "-q"], cwd=base, check=True)
    subprocess.run(git + ["add", "-A"], cwd=base, check=True)
    subprocess.run(git + ["commit", "-q", "-m", "Demo playbooks"], cwd=base, check=True)
    return base


def wait(path, field="status", done=("successful", "failed", "error", "canceled"), timeout=120):
    end = time.time() + timeout
    while time.time() < end:
        obj = call("GET", path)
        if obj.get(field) in done:
            return obj
        time.sleep(0.5)
    raise SystemExit(f"timeout waiting for {path}")


def wait_for_server(timeout=90):
    """Waits until the API answers, so the script can run right after a start."""
    end = time.time() + timeout
    while True:
        try:
            with urllib.request.urlopen(URL + "/api/v2/ping/", timeout=5):
                return
        except (urllib.error.URLError, OSError) as e:
            if time.time() > end:
                raise SystemExit(f"awxnext is not answering at {URL}: {e}")
            time.sleep(2)


def main():
    wait_for_server()
    call("PATCH", "settings/jobs/", {"PROJECT_ALLOW_FILE_URLS": True})
    org = ensure("organizations", {"name": "Acme Infrastructure"}, {"name": "Acme Infrastructure", "description": "Platform, apps and network automation"})
    ensure("organizations", {"name": "Acme Retail"}, {"name": "Acme Retail", "description": "Store systems"})
    oid = org["id"]

    folders = {}
    tree = [("apps", None), ("checkout", "apps"), ("billing", "apps"), ("search", "apps"),
            ("network", None), ("core-switches", "network"), ("firewalls", "network"),
            ("platform", None), ("security", None), ("patching", "security"), ("cis-hardening", "security"), ("sandbox", None)]
    for name, parent in tree:
        body = {"name": name, "organization": oid}
        look = {"name": name, "organization": oid}
        if parent:
            body["parent"] = folders[parent]
            look["parent"] = folders[parent]
        folders[name] = ensure("folders", look, body)["id"]

    users = {}
    for username, first, last in [("priya", "Priya", "Raman"), ("mokafor", "Mehmet", "Okafor"), ("lena", "Lena", "Brandt"),
                                  ("jonas", "Jonas", "Weber"), ("tomas", "Tomás", "Ferreira")]:
        users[username] = ensure("users", {"username": username}, {"username": username, "first_name": first, "last_name": last,
                                                                    "email": f"{username}@acme.example", "password": "demo"})["id"]
    teams = {}
    for name, members in [("Payments SRE", ["priya", "mokafor"]), ("Network team", ["jonas"]), ("Release managers", ["lena", "tomas"])]:
        teams[name] = ensure("teams", {"name": name, "organization": oid}, {"name": name, "organization": oid})["id"]
        for m in members:
            call("POST", f"users/{users[m]}/roles/", {"id": find_role(teams[name], "member_role")}, ok=(204, 400))

    for label in ["prod-critical", "eu", "web", "db", "network"]:
        ensure("labels", {"name": label, "organization": oid}, {"name": label, "organization": oid})

    repo = make_repo()
    proj = ensure("projects", {"name": "acme-playbooks", "organization": oid},
                  {"name": "acme-playbooks", "organization": oid, "scm_type": "git", "scm_url": "file://" + repo,
                   "description": "Deploy, ops and patching playbooks", "folder": folders["platform"]})
    synced = wait(f"projects/{proj['id']}/", done=("successful", "failed", "error"))
    if synced["status"] != "successful":
        update = synced.get("summary_fields", {}).get("last_job", {}).get("id")
        raise SystemExit(f"project sync {synced['status']}; see /jobs/project_update/{update} in the UI. "
                         "Is git installed where awxnext runs, and can it read " + repo + "?")

    local = "ansible_connection: local\nansible_python_interpreter: '{{ ansible_playbook_python }}'\n"
    invs = {}
    for name, hosts, group, desc in [
        ("prod-eu-west", [f"checkout-eu1-{i:02d}" for i in range(1, 4)] + [f"checkout-eu2-{i:02d}" for i in range(1, 9)], "checkout_api", "Checkout API hosts, EU west"),
        ("staging-eu", ["checkout-stg-01", "checkout-stg-02"], "checkout_api", "Staging"),
        ("prod-edge", ["edge-01", "edge-02", "edge-03"], "nginx", "Edge proxies"),
        ("prod-eu-db", ["pg-eu-01", "pg-eu-02"], "postgres", "Postgres primaries and replicas"),
        ("prod-eu-cache", ["redis-eu-01"], "redis", "Redis"),
    ]:
        inv = ensure("inventories", {"name": name, "organization": oid}, {"name": name, "organization": oid, "variables": local, "description": desc})
        invs[name] = inv["id"]
        g = ensure("groups", {"name": group, "inventory": inv["id"]}, {"name": group, "inventory": inv["id"]})
        for h in hosts:
            host = ensure("hosts", {"name": h, "inventory": inv["id"]}, {"name": h, "inventory": inv["id"]})
            call("POST", f"groups/{g['id']}/hosts/", {"id": host["id"]}, ok=(204, 400))

    survey_deploy = {"name": "Deploy", "description": "", "spec": [
        {"question_name": "Version", "variable": "app_version", "type": "text", "required": True, "default": "v2.14.3"},
        {"question_name": "Strategy", "variable": "deploy_strategy", "type": "multiplechoice", "choices": ["rolling", "canary"], "default": "rolling", "required": True},
        {"question_name": "Change ticket", "variable": "change_ticket", "type": "text", "required": False, "min": 0, "max": 20},
    ]}
    jts = {}
    for name, play, inv, folder, extra in [
        ("checkout-api · rolling deploy", "deploy/rolling.yml", "prod-eu-west", "checkout", {"survey_enabled": True, "extra_vars": "app_name: checkout-api\nserial: 8\n", "ask_limit_on_launch": True, "ask_variables_on_launch": True}),
        ("checkout-api · rollback", "deploy/rollback.yml", "prod-eu-west", "checkout", {}),
        ("checkout-worker · scale out", "ops/scale_workers.yml", "prod-eu-west", "checkout", {}),
        ("checkout-db · minor version upgrade", "db/pg_minor_upgrade.yml", "prod-eu-db", "checkout", {}),
        ("checkout · smoke tests", "tests/smoke.yml", "prod-eu-west", "checkout", {}),
        ("nginx edge · reload certificates", "edge/reload_certs.yml", "prod-edge", "checkout", {}),
        ("redis cache · flush (manual only)", "cache/flush.yml", "prod-eu-cache", "checkout", {}),
        ("checkout-api · staging deploy", "deploy/rolling.yml", "staging-eu", "checkout", {"extra_vars": "app_name: checkout-api\n"}),
        ("billing · invoice workers deploy", "deploy/rolling.yml", "staging-eu", "billing", {}),
        ("monthly patching · EU", "patching/patch.yml", "prod-eu-west", "patching", {}),
        ("core switches · backup configs", "ops/scale_workers.yml", "prod-edge", "core-switches", {}),
    ]:
        body = {"name": name, "project": proj["id"], "playbook": play, "inventory": invs[inv], "folder": folders[folder]}
        body.update(extra)
        jt = ensure("job_templates", {"name": name, "organization": oid}, body)
        jts[name] = jt["id"]
        if extra.get("survey_enabled"):
            call("POST", f"job_templates/{jt['id']}/survey_spec/", survey_deploy)
    rolling = jts["checkout-api · rolling deploy"]
    for lab in ["prod-critical", "eu", "web"]:
        call("POST", f"job_templates/{rolling}/labels/", {"id": find("labels", name=lab, organization=oid)["id"]}, ok=(204, 400))

    wf = ensure("workflow_job_templates", {"name": "checkout release train", "organization": oid},
                {"name": "checkout release train", "organization": oid, "folder": folders["checkout"], "description": "Staging, smoke, approval, prod"})
    if call("GET", f"workflow_job_templates/{wf['id']}/workflow_nodes/")["count"] == 0:
        n1 = call("POST", f"workflow_job_templates/{wf['id']}/workflow_nodes/", {"unified_job_template": jts["checkout-api · staging deploy"], "identifier": "staging"})
        n2 = call("POST", f"workflow_job_template_nodes/{n1['id']}/success_nodes/", {"unified_job_template": jts["checkout · smoke tests"], "identifier": "smoke"})
        appr = call("POST", f"workflow_job_template_nodes/{n2['id']}/success_nodes/", {"identifier": "approve"})
        call("POST", f"workflow_job_template_nodes/{appr['id']}/create_approval_template/", {"name": "Release manager sign-off", "timeout": 3600})
        call("POST", f"workflow_job_template_nodes/{appr['id']}/success_nodes/", {"unified_job_template": rolling, "identifier": "prod"})
        call("POST", f"workflow_job_template_nodes/{n1['id']}/failure_nodes/", {"unified_job_template": jts["checkout-api · rollback"], "identifier": "rollback"})

    for name, ujt, rrule in [
        ("Nightly smoke", jts["checkout · smoke tests"], "DTSTART:20250101T020000Z RRULE:FREQ=DAILY;INTERVAL=1"),
        ("Thursday release window", wf["id"], "DTSTART;TZID=Europe/Zurich:20250102T060000 RRULE:FREQ=WEEKLY;BYDAY=TH"),
        ("Edge certificates", jts["nginx edge · reload certificates"], "DTSTART:20250106T020000Z RRULE:FREQ=WEEKLY;BYDAY=MO"),
    ]:
        if not find("schedules", name=name):
            path = "workflow_job_templates" if ujt == wf["id"] else "job_templates"
            call("POST", f"{path}/{ujt}/schedules/", {"name": name, "rrule": rrule})

    if "--jobs" in sys.argv:
        runs = [(jts["checkout · smoke tests"], {})] * 3 + [
            (rolling, {"extra_vars": {"app_version": "v2.14.2"}}),
            (jts["checkout-worker · scale out"], {}),
            (jts["checkout-db · minor version upgrade"], {}),
            (rolling, {"extra_vars": {"app_version": "v2.14.3", "fail_hosts": ["checkout-eu2-03", "checkout-eu2-06", "checkout-eu2-07"]}}),
            (jts["checkout-api · staging deploy"], {}),
        ]
        for jt, body in runs:
            job = call("POST", f"job_templates/{jt}/launch/", body)
            wait(f"jobs/{job['id']}/")
        wj = call("POST", f"workflow_job_templates/{wf['id']}/launch/", {})
        print("launched workflow job", wj.get("id"))
    print("demo content ready in", org["name"])


def find_role(team_id, field):
    team = call("GET", f"teams/{team_id}/")
    return team["summary_fields"]["object_roles"][field]["id"]


if __name__ == "__main__":
    main()
