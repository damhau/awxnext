"""Fills the expected outputs of jinja_cases.json with real Jinja2, rendering
each case the way AWX renders credential injectors (CredentialType.inject_credential:
an ImmutableSandboxedEnvironment, input fields by name, tower/awx.filename).

Regenerate after changing the cases (see template_test.go):

    go test ./internal/credtypes -run TestInjectorsRenderLikeAWX -update
    uv run --with jinja2==3.1.6 python -I internal/credtypes/testdata/jinja_oracle.py internal/credtypes/testdata/jinja_cases.json
"""

import json
import sys

from jinja2 import sandbox


class Namespace:
    pass


def namespace(files):
    tower = Namespace()
    if "" in files:
        tower.filename = files[""]
    elif files:
        tower.filename = Namespace()
        for name, path in files.items():
            setattr(tower.filename, name, path)
    return tower


def main(path):
    with open(path) as f:
        cases = json.load(f)
    env = sandbox.ImmutableSandboxedEnvironment()
    for case in cases:
        ns = dict(case.get("vars") or {})
        ns["tower"] = ns["awx"] = namespace(case.get("files") or {})
        case.pop("want", None)
        case.pop("error", None)
        try:
            case["want"] = env.from_string(case["template"]).render(**ns)
        except Exception as e:  # noqa: BLE001 - any failure is the expected outcome
            case["error"] = f"{type(e).__name__}: {e}"
    with open(path, "w") as f:
        json.dump(cases, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")


if __name__ == "__main__":
    main(sys.argv[1])
