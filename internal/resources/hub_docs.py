"""Reads the docs of a collection's modules and plugins with ansible-doc.

awxnext sends the collection's artifact on stdin. The script extracts it into
a temporary directory of its own, runs ansible-doc --metadata-dump on it, and
prints the docs as JSON. ansible-doc imports some of the collection's Python
code (doc fragments, filter and test plugins): awxnext runs the script where
that code can't reach the server, in a container or as a job user.
"""

import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile

MAX_BYTES = 1 << 30
NO_ARTIFACT = 4
FAILED = 5


def extract(stream, dest):
    total = 0
    with tarfile.open(fileobj=stream, mode="r|gz") as tf:
        for m in tf:
            name = os.path.normpath(m.name)
            if os.path.isabs(name) or name == "." or name == ".." or name.startswith("../"):
                continue
            path = os.path.join(dest, name)
            if m.isdir():
                os.makedirs(path, mode=0o700, exist_ok=True)
            elif m.isfile():
                total += m.size
                if total > MAX_BYTES:
                    raise ValueError("the artifact is too large")
                os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
                src = tf.extractfile(m)
                with open(path, "wb") as f:
                    shutil.copyfileobj(src, f)


def main():
    doc, fqcn = sys.argv[1], sys.argv[2]
    namespace, name = fqcn.split(".", 1)
    root = tempfile.mkdtemp(prefix="awxnext-docs-")
    try:
        dest = os.path.join(root, "ansible_collections", namespace, name)
        os.makedirs(dest, mode=0o700)
        try:
            extract(sys.stdin.buffer, dest)
        except (tarfile.TarError, OSError, ValueError) as e:
            print("The artifact can't be extracted: %s" % e, file=sys.stderr)
            return NO_ARTIFACT
        while sys.stdin.buffer.read(1 << 16):
            pass
        env = {k: v for k, v in os.environ.items() if k in ("PATH", "LANG", "LC_ALL", "TMPDIR")}
        env.update(
            HOME=root,
            ANSIBLE_COLLECTIONS_PATH=root,
            ANSIBLE_COLLECTIONS_SCAN_SYS_PATH="false",
            ANSIBLE_LOCAL_TEMP=os.path.join(root, ".ansible-tmp"),
            ANSIBLE_CONFIG=os.path.join(root, "ansible.cfg"),
            ANSIBLE_NOCOLOR="1",
            ANSIBLE_DEPRECATION_WARNINGS="false",
        )
        r = subprocess.run(
            [doc, "--metadata-dump", "--no-fail-on-errors", fqcn],
            cwd=root, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        if r.returncode != 0:
            sys.stderr.write(r.stderr.decode("utf-8", "replace")[-4000:])
            return FAILED
        dump = json.loads(r.stdout)
        out = {}
        for kind, plugins in (dump.get("all") or {}).items():
            if kind == "keyword" or not plugins:
                continue
            for plugin in plugins.values():
                if isinstance(plugin, dict):
                    for d in (plugin, plugin.get("doc")):
                        if isinstance(d, dict):
                            d.pop("filename", None)
                            d.pop("path", None)
            out[kind] = plugins
        text = json.dumps(out).replace(dest + "/", "").replace(dest, "").replace(root, "")
        sys.stdout.write(text)
        return 0
    finally:
        shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
