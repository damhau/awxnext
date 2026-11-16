"""awxnext's event connector runner.

It runs one ansible.eda source plugin, passes each event through the
connector's filters and posts it to the connector's event stream. awxnext
sends it on the process's stdin at each start, with its configuration (the
CONFIG global): it uses the standard library only, so that it runs in any
image that has the collection and the plugin's own dependencies.
"""

import asyncio
import datetime
import inspect
import json
import os
import runpy
import shutil
import ssl
import sys
import tempfile
import time
import traceback
import urllib.error
import urllib.request
import uuid

STATUS_PREFIX = "awxnext-status "
STATUS_EVERY = 5
MAX_BACKOFF = 30

# Exit codes awxnext reads.
EXIT_PLUGIN = 1
EXIT_CONFIG = 2
EXIT_REFUSED = 3


def log(msg):
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    print(stamp + " " + msg, flush=True)


class ConfigError(Exception):
    pass


class Refused(Exception):
    pass


def collection_roots():
    roots = []
    for var in ("ANSIBLE_COLLECTIONS_PATH", "ANSIBLE_COLLECTIONS_PATHS"):
        roots += [p for p in os.environ.get(var, "").split(os.pathsep) if p]
    roots += [os.path.expanduser("~/.ansible/collections"), "/usr/share/ansible/collections"]
    roots += [p for p in sys.path if p]
    out = []
    for r in roots:
        r = os.path.abspath(os.path.expanduser(r))
        if os.path.basename(r) == "ansible_collections":
            r = os.path.dirname(r)
        if r not in out:
            out.append(r)
    return out


def find_plugin(fqcn, kind):
    parts = fqcn.split(".")
    if len(parts) != 3:
        raise ConfigError("%s is not a plugin's full name (namespace.collection.name)" % fqcn)
    ns, coll, name = parts
    if (ns, coll) == ("eda", "builtin"):
        ns, coll = "ansible", "eda"
    for root in collection_roots():
        path = os.path.join(root, "ansible_collections", ns, coll, "extensions", "eda", "plugins", kind, name + ".py")
        if os.path.isfile(path):
            if root not in sys.path:
                sys.path.append(root)
            return path
    raise ConfigError("%s: no %s plugin %s.%s.%s in the collections of %s" % (fqcn, kind, ns, coll, name, ", ".join(collection_roots())))


def load(fqcn, kind):
    path = find_plugin(fqcn, kind)
    module = runpy.run_path(path)
    if "main" not in module:
        raise ConfigError("%s has no main function" % fqcn)
    return module["main"]


def substitute(value, files):
    if isinstance(value, str):
        for placeholder, path in files.items():
            value = value.replace(placeholder, path)
        return value
    if isinstance(value, list):
        return [substitute(v, files) for v in value]
    if isinstance(value, dict):
        return {k: substitute(v, files) for k, v in value.items()}
    return value


def write_files(specs):
    """Writes the credentials' files where only this process's user can read them."""
    if not specs:
        return {}, None
    d = tempfile.mkdtemp(prefix="awxnext-connector-")
    os.chmod(d, 0o700)
    paths = {}
    for i, f in enumerate(specs):
        path = os.path.join(d, "file%d" % i)
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as out:
            out.write(f.get("content", ""))
        paths[f["placeholder"]] = path
    return paths, d


class Stats:
    def __init__(self):
        self.sent = 0
        self.dropped = 0
        self.rejected = 0
        self.retries = 0
        self.last_error = ""
        self.unreachable_since = None


class SourceQueue(asyncio.Queue):
    """The queue a source plugin puts its events in: each event goes through
    the filters, then gets its meta as ansible-rulebook's insert_meta_info
    gives it. put waits while the buffer is full, which holds the source back;
    put_nowait drops the oldest event instead."""

    def __init__(self, maxsize, filters, source_name, source_type, stats):
        super().__init__(maxsize)
        self.filters = filters
        self.source_name = source_name
        self.source_type = source_type
        self.stats = stats

    def prepare(self, event):
        for fn, args in self.filters:
            event = fn(event, **args)
        if not isinstance(event, dict):
            event = {"data": event}
        meta = event.setdefault("meta", {})
        if not isinstance(meta, dict):
            meta = event["meta"] = {"value": meta}
        meta.setdefault("source", {"name": self.source_name, "type": self.source_type})
        meta.setdefault("received_at", datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"))
        meta.setdefault("uuid", str(uuid.uuid4()))
        return event

    async def put(self, item):
        await super().put(self.prepare(item))

    def put_nowait(self, item):
        item = self.prepare(item)
        if self.full():
            super().get_nowait()
            self.stats.dropped += 1
        super().put_nowait(item)


class Poster:
    def __init__(self, cfg, queue, stats):
        base = cfg["base_url"].rstrip("/")
        self.url = base + cfg["post_path"]
        self.token = cfg["token"]
        self.batch_size = max(1, int(cfg.get("batch_size", 100)))
        self.connector = cfg.get("connector", 0)
        self.queue = queue
        self.stats = stats
        self.context = None
        if self.url.startswith("https:"):
            self.context = ssl.create_default_context(cadata=cfg.get("ca") or None)
            if cfg.get("insecure"):
                # The node's own choice, as the agent's --insecure.
                self.context.check_hostname = False
                self.context.verify_mode = ssl.CERT_NONE

    def cloud_event(self, event):
        meta = event.get("meta", {})
        return {
            "specversion": "1.0",
            "id": str(meta.get("uuid") or uuid.uuid4()),
            "source": "/api/v2/event_connectors/%s/" % self.connector,
            "type": str(meta.get("source", {}).get("type") or "event"),
            "time": meta.get("received_at"),
            "datacontenttype": "application/json",
            "data": event,
        }

    def post_once(self, events):
        body = json.dumps([self.cloud_event(e) for e in events], default=str).encode()
        req = urllib.request.Request(self.url, data=body, method="POST", headers={
            "Content-Type": "application/cloudevents-batch+json",
            "Authorization": "Connector " + self.token,
            "User-Agent": "awxnext-connector",
        })
        try:
            with urllib.request.urlopen(req, timeout=30, context=self.context) as resp:  # noqa: S310 - awxnext's own URL
                return resp.status, resp.headers.get("Retry-After"), ""
        except urllib.error.HTTPError as e:
            detail = ""
            try:
                detail = json.loads(e.read() or b"{}").get("detail", "")
            except ValueError:
                pass
            return e.code, e.headers.get("Retry-After"), detail
        except (OSError, urllib.error.URLError) as e:
            return 0, None, str(getattr(e, "reason", e))

    async def deliver(self, events):
        """Posts events until awxnext takes them, waiting while it is unreachable."""
        backoff = 1
        loop = asyncio.get_running_loop()
        while True:
            status, retry_after, detail = await loop.run_in_executor(None, self.post_once, events)
            if status in (200, 201, 202):
                if self.stats.unreachable_since is not None:
                    log("awxnext took events again, after %d s" % (time.monotonic() - self.stats.unreachable_since))
                    self.stats.unreachable_since = None
                self.stats.sent += len(events)
                return
            if status in (401, 403, 404):
                raise Refused("awxnext refused the connector (HTTP %d): it was stopped, moved or deleted" % status)
            if status in (400, 413):
                if len(events) > 1:
                    half = len(events) // 2
                    await self.deliver(events[:half])
                    await self.deliver(events[half:])
                    return
                self.stats.rejected += 1
                log("awxnext rejected an event (HTTP %d): %s" % (status, detail))
                return
            self.stats.retries += 1
            self.stats.last_error = "HTTP %d: %s" % (status, detail) if status else detail
            if self.stats.unreachable_since is None:
                self.stats.unreachable_since = time.monotonic()
                log("awxnext is unreachable (%s): buffering events" % self.stats.last_error)
            wait = backoff
            if retry_after and retry_after.isdigit():
                wait = max(1, int(retry_after))
            else:
                backoff = min(MAX_BACKOFF, backoff * 2)
            await asyncio.sleep(wait)

    async def run(self):
        while True:
            events = [await self.queue.get()]
            while len(events) < self.batch_size and not self.queue.empty():
                events.append(self.queue.get_nowait())
            await self.deliver(events)
            for _ in events:
                self.queue.task_done()


def status_line(queue, stats, state):
    return STATUS_PREFIX + json.dumps({
        "state": state,
        "sent": stats.sent,
        "buffered": queue.qsize(),
        "dropped": stats.dropped,
        "rejected": stats.rejected,
        "retries": stats.retries,
        "unreachable": stats.unreachable_since is not None,
        "last_error": stats.last_error,
    })


async def report(queue, stats):
    while True:
        print(status_line(queue, stats, "running"), flush=True)
        await asyncio.sleep(STATUS_EVERY)


async def run_source(main, queue, args):
    if inspect.iscoroutinefunction(main):
        await main(queue, args)
    else:
        await asyncio.get_running_loop().run_in_executor(None, main, queue, args)


async def amain(cfg):
    files, tmp = write_files(cfg.get("files"))
    try:
        for k, v in substitute(cfg.get("env") or {}, files).items():
            os.environ[k] = str(v)
        args = substitute(cfg.get("args") or {}, files)
        source = cfg["source"]
        main = load(source, "event_source")
        filters = []
        for f in cfg.get("filters") or []:
            (name, fargs), = f.items()
            if name in ("eda.builtin.insert_meta_info", "ansible.eda.insert_meta_info"):
                continue
            filters.append((load(name, "event_filter"), substitute(fargs or {}, files)))
        stats = Stats()
        queue = SourceQueue(max(1, int(cfg.get("buffer_size", 1000))), filters, cfg.get("name", ""), source, stats)
        poster = Poster(cfg, queue, stats)
        log("starting %s with %d filter(s), posting to %s" % (source, len(filters), poster.url))
        posting = asyncio.ensure_future(poster.run())
        reporting = asyncio.ensure_future(report(queue, stats))
        sourcing = asyncio.ensure_future(run_source(main, queue, args))
        try:
            await asyncio.wait([sourcing, posting], return_when=asyncio.FIRST_COMPLETED)
            rc = 0
            if sourcing.done():
                if sourcing.exception() is None:
                    log("%s ended" % source)
                else:
                    e = sourcing.exception()
                    log("%s failed:\n%s" % (source, "".join(traceback.format_exception(type(e), e, e.__traceback__))))
                    rc = EXIT_PLUGIN
                if not queue.empty():
                    log("posting the %d buffered event(s)" % queue.qsize())
                drain = asyncio.ensure_future(queue.join())
                await asyncio.wait([drain, posting], return_when=asyncio.FIRST_COMPLETED)
            if posting.done():
                sourcing.cancel()
                e = posting.exception()
                if isinstance(e, Refused):
                    log(str(e))
                    return EXIT_REFUSED
                raise e
            print(status_line(queue, stats, "ended"), flush=True)
            return rc
        finally:
            reporting.cancel()
    finally:
        if tmp:
            shutil.rmtree(tmp, ignore_errors=True)


def run(cfg):
    try:
        return asyncio.run(amain(cfg))
    except ConfigError as e:
        log(str(e))
        return EXIT_CONFIG


if __name__ == "__main__":
    sys.exit(run(CONFIG))  # noqa: F821 - set by the bootstrap
