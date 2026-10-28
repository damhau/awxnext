#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11,<3.13"
# dependencies = [
#   "ansible-core==2.16.14",
#   "awxkit==24.6.1",
#   "setuptools<81",  # awxkit 24.6.1 imports pkg_resources
#   "pytz",  # the collection's schedule_rrule lookup
#   "python-dateutil>=2.7.0",
#   "pyyaml>=6",
# ]
# ///
"""Runs AWX's own client test suites against an awxnext and reports each test.

Suites:
  collection  the awx.awx integration targets from AWX's release tag: each
              target is a role, run as a playbook against the server
  cli         smoke tests of the awx CLI (awxkit)
  terraform   the acceptance tests of the ansible/aap Terraform provider,
              after its own fixture playbook (with awx.awx standing in for
              the certified collections)

Results are compared with compat/known-failures.yml. A failure that is not
listed, or a listed test that now passes, fails the run, so the list stays
true. --update-known rewrites it from the current results.

    docker compose -f compat/docker-compose.yml up -d --build --wait
    uv run --script compat/run.py [--suite collection] [--only job_launch]
"""

import argparse
import json
import os
import re
import secrets
import shutil
import ssl
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

import yaml

AWX_TAG = "24.6.1"
TF_PROVIDER_TAG = "v1.5.0"
HERE = Path(__file__).resolve().parent
KNOWN = HERE / "known-failures.yml"
CACHE = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "awxnext-compat"
BIN = Path(sys.executable).parent


def sh(cmd, **kw):
    return subprocess.run(cmd, check=True, **kw)


def fetch(repo, tag, dest, sparse=None):
    """Shallow clone of a tag, cached between runs."""
    if dest.exists():
        return dest
    tmp = dest.with_suffix(".tmp")
    shutil.rmtree(tmp, ignore_errors=True)
    args = ["git", "clone", "-q", "--depth", "1", "--branch", tag, "-c", "advice.detachedHead=false"]
    if sparse:
        args += ["--filter=blob:none", "--sparse"]
    sh(args + [repo, str(tmp)])
    if sparse:
        sh(["git", "-C", str(tmp), "sparse-checkout", "set", *sparse])
    tmp.rename(dest)
    return dest


class Result:
    def __init__(self, suite, name, status, seconds=0.0, detail="", log=""):
        self.suite, self.name, self.status, self.seconds, self.detail, self.log = suite, name, status, seconds, detail, log

    def as_dict(self):
        return {k: getattr(self, k) for k in ("suite", "name", "status", "seconds", "detail", "log")}


def first_failure(output):
    """The first failing task that was not ignored, and its message, from ansible-playbook output."""
    for section in re.split(r"(?m)^(?=TASK \[)", output):
        if re.search(r"(?m)^\.\.\.ignoring$", section):
            continue
        m = re.search(r"(?ms)^(?:fatal:|failed:|ERROR!).*", section)
        if not m:
            continue
        first = section.splitlines()[0]
        task = first[6 : first.rfind("]")] if first.startswith("TASK [") else ""
        msg = re.search(r'"msg": "((?:[^"\\]|\\.)*)"', m.group(0))
        msg = msg.group(1) if msg else m.group(0).splitlines()[0]
        assertion = re.search(r'"assertion": "((?:[^"\\]|\\.)*)"', m.group(0))
        if assertion:
            msg += f": {assertion.group(1)}"
        return (f"{task}: {msg}" if task else msg)[:400]
    return ""


class Runner:
    def __init__(self, args):
        self.args = args
        self.report = Path(args.report).resolve()
        self.logs = self.report / "logs"
        self.logs.mkdir(parents=True, exist_ok=True)
        self.env = dict(
            os.environ,
            CONTROLLER_HOST=args.host,
            CONTROLLER_USERNAME=args.username,
            CONTROLLER_PASSWORD=args.password,
            CONTROLLER_VERIFY_SSL="false",
            ANSIBLE_NOCOLOR="1",
            ANSIBLE_HOST_KEY_CHECKING="False",
            ANSIBLE_LOCALHOST_WARNING="False",
            ANSIBLE_INVENTORY_UNPARSED_WARNING="False",
            ANSIBLE_DEPRECATION_WARNINGS="False",
            PATH=f"{BIN}{os.pathsep}{os.environ.get('PATH', '')}",
        )
        self.collection_dir = None
        self.demo_ready = False

    def selected(self, name):
        return not self.args.only or any(o in name for o in self.args.only)

    # The awx.awx collection from the AWX tag, with its integration targets as roles.
    def collection(self):
        if self.collection_dir:
            return self.collection_dir
        src = fetch("https://github.com/ansible/awx", AWX_TAG, CACHE / f"awx-{AWX_TAG}", sparse=["awx_collection"])
        root = self.report / "work" / "collections"
        dest = root / "ansible_collections" / "awx" / "awx"
        shutil.rmtree(root, ignore_errors=True)
        shutil.copytree(src / "awx_collection", dest, ignore=shutil.ignore_patterns("tests", "test"))
        targets = src / "awx_collection" / "tests" / "integration" / "targets"
        for t in targets.iterdir():
            shutil.copytree(t, dest / "roles" / t.name)
        self.env["ANSIBLE_COLLECTIONS_PATH"] = str(root)
        self.collection_dir = dest
        return dest

    def playbook(self, name, playbook, cwd, timeout):
        start = time.monotonic()
        log = self.logs / f"{name}.log"
        try:
            p = subprocess.run([str(BIN / "ansible-playbook"), str(playbook)], cwd=cwd, env=self.env,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=timeout)
            out, status = p.stdout, "pass" if p.returncode == 0 else "fail"
        except subprocess.TimeoutExpired as e:
            out, status = (e.stdout or b"").decode(errors="replace") if isinstance(e.stdout, bytes) else (e.stdout or ""), "timeout"
        log.write_text(out)
        detail = "" if status == "pass" else (first_failure(out) or f"{status}, see log")
        return status, time.monotonic() - start, detail, str(log.relative_to(self.report))

    def run_target(self, target):
        dest = self.collection()
        work = self.report / "work"
        play = work / f"play-{target}.yml"
        play.write_text(yaml.safe_dump([{
            "name": target, "hosts": "localhost", "connection": "local", "gather_facts": False,
            "vars": {"ansible_python_interpreter": "{{ ansible_playbook_python }}"},
            "roles": [f"awx.awx.{target}"],
        }]))
        return self.playbook(f"collection-{target}", play, work, self.args.timeout)

    def demo_data(self):
        """AWX's preload data (Default, Demo Project, Demo Inventory, ...), which
        the suites expect: the demo_data target, then the Demo Credential that
        AWX's installer also creates and the target does not."""
        if self.demo_ready:
            return None
        self.demo_ready = True
        result = self.run_target("demo_data")
        play = self.report / "work" / "preload.yml"
        play.write_text(yaml.safe_dump([{
            "name": "preload", "hosts": "localhost", "connection": "local", "gather_facts": False,
            "vars": {"ansible_python_interpreter": "{{ ansible_playbook_python }}"},
            "tasks": [
                {"awx.awx.credential": {"name": "Demo Credential", "credential_type": "Machine", "user": self.args.username,
                                        "inputs": {"username": self.args.username}}},
                {"awx.awx.job_template": {"name": "Demo Job Template", "credentials": ["Demo Credential"]}},
            ],
        }]))
        status, _, detail, _ = self.playbook("preload", play, play.parent, self.args.timeout)
        if result[0] != "pass" or status != "pass":
            print(f"  demo data setup failed: {result[2] or detail}", flush=True)
        return result

    def suite_collection(self):
        dest = self.collection()
        targets = sorted(p.name for p in (dest / "roles").iterdir())
        targets.remove("demo_data")
        results = []
        for t in ["demo_data", *targets]:
            if not self.selected(t):
                continue
            status, secs, detail, log = self.demo_data() if t == "demo_data" else self.run_target(t)
            print(f"  {status:7} {secs:6.1f}s  {t}  {detail}", flush=True)
            results.append(Result("collection", t, status, secs, detail, log))
        return results

    # awx CLI smoke tests: each step runs a command; later steps use earlier ids.
    def suite_cli(self):
        self.collection()
        self.demo_data()
        tag = secrets.token_hex(4)
        org, inv, user = f"compat-cli-{tag}", f"compat-cli-inv-{tag}", f"compat-cli-{tag}"
        state = {}
        work = self.report / "work"
        work.mkdir(parents=True, exist_ok=True)

        def awx(*a, stdin=None):
            cmd = [str(BIN / "awx"), "--conf.host", self.args.host, "--conf.username", self.args.username,
                   "--conf.password", self.args.password, "--conf.color", "false", "-k", *a]
            return subprocess.run(cmd, env=dict(self.env, PYTHONWARNINGS="ignore"), input=stdin,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=600)

        def j(p):
            return json.loads(p.stdout)

        def any_exit(p):  # argparse exits 2 after printing a subcommand's help
            p.returncode = 0
            return p

        def launch_check(p):
            assert "successful" in p.stdout, "job did not end successful"
            ids = re.findall(r'"id":\s*(\d+)', p.stdout)
            state["job"] = ids[0] if ids else ""

        steps = [
            ("ping", lambda: awx("ping"), lambda p: j(p)["version"]),
            ("login", lambda: awx("login"), lambda p: j(p)["token"]),
            ("me", lambda: awx("me"), lambda p: j(p)["results"][0]["username"] == self.args.username or 1 / 0),
            ("list", lambda: awx("organizations", "list", "-f", "json"), lambda p: j(p)["count"] >= 1 or 1 / 0),
            ("create", lambda: awx("organizations", "create", "--name", org, "-f", "json"), lambda p: state.update(org=j(p)["id"])),
            ("get by name", lambda: awx("organizations", "get", org, "-f", "json"), lambda p: j(p)["name"] == org or 1 / 0),
            ("modify", lambda: awx("organizations", "modify", org, "--description", "changed", "-f", "json"), lambda p: j(p)["description"] == "changed" or 1 / 0),
            ("create with lookup by name", lambda: awx("inventory", "create", "--name", inv, "--organization", org, "-f", "json"), lambda p: state.update(inv=j(p)["id"])),
            ("create host", lambda: awx("hosts", "create", "--name", "h1", "--inventory", inv, "--variables", '{"a": 1}', "-f", "json"), None),
            ("filter", lambda: awx("hosts", "list", "--inventory", str(state.get("inv")), "-f", "json"), lambda p: j(p)["count"] == 1 or 1 / 0),
            ("human output", lambda: awx("hosts", "list", "--inventory", str(state.get("inv")), "-f", "human"), lambda p: "h1" in p.stdout or 1 / 0),
            ("credential with inputs", lambda: awx("credentials", "create", "--name", f"compat-cli-{tag}", "--organization", org,
                                                   "--credential_type", "Machine", "--inputs", '{"username": "u", "password": "p"}', "-f", "json"),
             lambda p: j(p)["inputs"]["password"] == "$encrypted$" or 1 / 0),
            ("create user", lambda: awx("users", "create", "--username", user, "--password", "Compat-pw-1", "-f", "json"), None),
            ("grant role", lambda: awx("users", "grant", user, "--organization", org, "--role", "member"), None),
            ("revoke role", lambda: awx("users", "revoke", user, "--organization", org, "--role", "member"), None),
            ("launch and monitor", lambda: awx("job_templates", "launch", "Demo Job Template", "--monitor", "-f", "json"), launch_check),
            ("job stdout", lambda: awx("jobs", "stdout", str(state.get("job"))), lambda p: "PLAY RECAP" in p.stdout or 1 / 0),
            ("settings", lambda: awx("settings", "list", "-f", "json"), lambda p: "TOWER_URL_BASE" in j(p) or 1 / 0),
            ("help from OPTIONS", lambda: any_exit(awx("job_templates", "create", "--help")), lambda p: "--playbook" in p.stdout + p.stderr or 1 / 0),
            ("export", lambda: awx("export", "--organizations", org, "--inventory", inv), lambda p: (work / "export.json").write_text(p.stdout) and (j(p)["organizations"] or 1 / 0)),
            ("delete", lambda: awx("inventory", "delete", inv), None),
            ("import", lambda: awx("import", stdin=(work / "export.json").read_text() if (work / "export.json").exists() else "{}"), None),
            ("imported", lambda: awx("inventory", "get", inv, "-f", "json"), lambda p: j(p)["name"] == inv or 1 / 0),
            ("cleanup", lambda: awx("organizations", "delete", org), None),
        ]
        results = []
        for name, run, check in steps:
            if not self.selected(name):
                continue
            start = time.monotonic()
            log = self.logs / f"cli-{re.sub(r'[^a-z0-9]+', '-', name)}.log"
            try:
                p = run()
                log.write_text(p.stdout + p.stderr)
                if p.returncode != 0:
                    lines = [l.strip() for l in (p.stdout + p.stderr).splitlines() if l.strip()]
                    raise RuntimeError(f"exit {p.returncode}: {lines[-1] if lines else ''}")
                if check:
                    check(p)
                status, detail = "pass", ""
            except Exception as e:  # noqa: BLE001 - a step fails for any reason
                status, detail = "fail", f"{type(e).__name__}: {e}"[:400]
            secs = time.monotonic() - start
            print(f"  {status:7} {secs:6.1f}s  {name}  {detail}", flush=True)
            results.append(Result("cli", name, status, secs, detail, str(log.relative_to(self.report))))
        return results

    # The Terraform provider: its fixture playbook, then go test on its acceptance tests.
    def suite_terraform(self):
        self.collection()
        self.demo_data()
        src = fetch("https://github.com/ansible/terraform-provider-aap", TF_PROVIDER_TAG, CACHE / f"terraform-provider-aap-{TF_PROVIDER_TAG}")
        work = self.report / "work" / "terraform"
        shutil.rmtree(work, ignore_errors=True)
        shutil.copytree(src / "testing", work)
        play = yaml.safe_load((work / "playbook.yml").read_text())
        play[0]["tasks"] = fixture_tasks(play[0]["tasks"])
        play[0]["vars"] = {"ansible_python_interpreter": "{{ ansible_playbook_python }}"}
        (work / "fixtures.yml").write_text(yaml.safe_dump(play, sort_keys=False))
        status, secs, detail, log = self.playbook("terraform-fixtures", work / "fixtures.yml", work, self.args.timeout)
        print(f"  {status:7} {secs:6.1f}s  fixtures  {detail}", flush=True)
        if status != "pass":
            return [Result("terraform", "fixtures", status, secs, detail, log)]
        env = dict(self.env, TF_ACC="1", AAP_HOSTNAME=self.args.host, AAP_USERNAME=self.args.username,
                   AAP_PASSWORD=self.args.password, AAP_INSECURE_SKIP_VERIFY="true")
        for line in (work / "acceptance_test_vars.env").read_text().splitlines():
            m = re.match(r'export (\w+)="(.*)"', line.strip())
            if m:
                env[m.group(1)] = m.group(2)
        cmd = ["go", "test", "-count=1", "-json", "-timeout", "60m", "./internal/provider/"]
        if self.args.only:
            cmd += ["-run", "|".join(self.args.only)]
        p = subprocess.run(cmd, cwd=src, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        (self.logs / "terraform-go-test.json").write_text(p.stdout)
        tests, output = {}, {}
        for line in p.stdout.splitlines():
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            name = ev.get("Test", "")
            if not name.startswith("TestAcc") or "/" in name:  # unit tests do not reach the server
                continue
            if ev.get("Action") == "output":
                output.setdefault(name, []).append(ev.get("Output", ""))
            elif ev.get("Action") in ("pass", "fail", "skip"):
                tests[name] = (ev["Action"], ev.get("Elapsed", 0.0))
        results = [Result("terraform", "fixtures", "pass", secs)]
        for name, (action, elapsed) in sorted(tests.items()):
            if action == "skip":
                continue
            out = "".join(output.get(name, []))
            detail = ""
            if action == "fail":
                errs = [l.strip() for l in out.splitlines() if "Error" in l or "error" in l]
                detail = (errs[0] if errs else out.strip()[-300:])[:400]
            log = self.logs / f"terraform-{name}.log"
            log.write_text(out)
            print(f"  {action:7} {elapsed:6.1f}s  {name}  {detail}", flush=True)
            results.append(Result("terraform", name, action, elapsed, detail, str(log.relative_to(self.report))))
        if not tests:
            results.append(Result("terraform", "go test", "fail", 0, p.stdout.strip()[-400:], "logs/terraform-go-test.json"))
        return results


def fixture_tasks(tasks):
    """The provider's fixture tasks with awx.awx in place of the certified
    collections: EDA tasks dropped (out of scope), the platform token
    attempt replaced by its controller token fallback."""
    out = []
    for t in tasks:
        if "block" in t:
            if any("ansible.platform.token" in b for b in t["block"]):
                out += fixture_tasks(t.get("rescue", []))
                continue
            block = fixture_tasks(t["block"])
            if not block:  # only EDA tasks
                continue
            t = dict(t, block=block)
            if "rescue" in t:
                t["rescue"] = fixture_tasks(t["rescue"])
        module = next((k for k in t if k.startswith("ansible.")), None)
        if module and module.startswith("ansible.eda."):
            continue
        if module and module.startswith(("ansible.controller.", "ansible.platform.")):
            t = {("awx.awx." + k.split(".", 2)[2] if k == module else k): v for k, v in t.items()}
        out.append(t)
    return out


def load_known():
    data = yaml.safe_load(KNOWN.read_text()) if KNOWN.exists() else None
    return {s: dict(v or {}) for s, v in (data or {}).items()}


def compare(results, known):
    """Splits results into new failures and listed tests that now pass."""
    new, fixed = [], []
    for r in results:
        listed = r.name in known.get(r.suite, {})
        if r.status != "pass" and not listed:
            new.append(r)
        elif r.status == "pass" and listed:
            fixed.append(r)
    return new, fixed


def write_known(results, known):
    lines = [
        "# Tests of compat/run.py expected to fail, each with the issue that tracks it",
        "# (or why it is out of scope). A failure missing here, or a test listed here",
        "# that passes, fails the run: keep this list true.",
    ]
    by_suite = {}
    for r in results:
        by_suite.setdefault(r.suite, {})
        if r.status != "pass":
            by_suite[r.suite][r.name] = known.get(r.suite, {}).get(r.name, "TODO: open an issue")
    for suite in sorted(set(by_suite) | set(known)):
        entries = dict(known.get(suite, {}))
        if suite in by_suite:  # only the suites that ran are rewritten
            entries = by_suite[suite]
        lines.append(f"{suite}:" + ("" if entries else " {}"))
        for name, why in sorted(entries.items()):
            lines.append(f"  {json.dumps(name)}: {json.dumps(why)}")
    KNOWN.write_text("\n".join(lines) + "\n")


def markdown(results, known, new, fixed, server):
    out = [f"# awxnext compatibility report", "",
           f"awxnext {server} · awx.awx and awxkit {AWX_TAG} · terraform-provider-aap {TF_PROVIDER_TAG}", ""]
    out += ["| Suite | Passed | Known failures | New failures | Now passing |", "|---|---|---|---|---|"]
    for suite in sorted({r.suite for r in results}):
        rs = [r for r in results if r.suite == suite]
        passed = sum(r.status == "pass" for r in rs)
        kf = sum(r.status != "pass" and r.name in known.get(suite, {}) for r in rs)
        out.append(f"| {suite} | {passed}/{len(rs)} | {kf} | {sum(r.suite == suite for r in new)} | {sum(r.suite == suite for r in fixed)} |")
    if new:
        out += ["", "## New failures", "", "| Suite | Test | Failure |", "|---|---|---|"]
        out += [f"| {r.suite} | {r.name} | {r.detail.replace('|', '/')} |" for r in new]
    if fixed:
        out += ["", "## Listed as failing but passing (remove from known-failures.yml)", ""]
        out += [f"- {r.suite} / {r.name}" for r in fixed]
    failing = [r for r in results if r.status != "pass" and r not in new]
    if failing:
        out += ["", "## Known failures", "", "| Suite | Test | Tracked by | Failure |", "|---|---|---|---|"]
        out += [f"| {r.suite} | {r.name} | {known[r.suite][r.name]} | {r.detail.replace('|', '/')} |" for r in failing]
    return "\n".join(out) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--host", default=os.environ.get("CONTROLLER_HOST", "https://localhost:8054"))
    ap.add_argument("--username", default=os.environ.get("CONTROLLER_USERNAME", "admin"))
    ap.add_argument("--password", default=os.environ.get("CONTROLLER_PASSWORD", "compat"))
    ap.add_argument("--suite", action="append", choices=["collection", "cli", "terraform"], help="default: all")
    ap.add_argument("--only", action="append", help="run tests whose name contains this (repeatable)")
    ap.add_argument("--timeout", type=int, default=900, help="seconds per playbook")
    ap.add_argument("--report", default="compat-report")
    ap.add_argument("--update-known", action="store_true", help="rewrite known-failures.yml from this run")
    args = ap.parse_args()

    with urllib.request.urlopen(args.host.rstrip("/") + "/api/v2/ping/", timeout=30, context=ssl._create_unverified_context()) as r:
        server = json.load(r).get("version", "?")
    runner = Runner(args)
    results = []
    for suite in args.suite or ["collection", "cli", "terraform"]:
        print(f"== {suite}", flush=True)
        results += getattr(runner, f"suite_{suite}")()

    known = load_known()
    if args.update_known:
        write_known(results, known)
        known = load_known()
    new, fixed = compare(results, known)
    if args.only:  # a partial run cannot tell what now passes overall
        fixed = [r for r in fixed if runner.selected(r.name)]
    md = markdown(results, known, new, fixed, server)
    (runner.report / "report.md").write_text(md)
    (runner.report / "report.json").write_text(json.dumps([r.as_dict() for r in results], indent=1))
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as f:
            f.write(md)
    print()
    print(md)
    sys.exit(1 if new or fixed else 0)


if __name__ == "__main__":
    main()
