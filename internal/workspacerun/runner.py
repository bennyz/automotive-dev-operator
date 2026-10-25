"""Durable workspace runs. Invoked through pod exec; no daemon is required."""

import base64
import collections
import contextlib
import datetime
import fcntl
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import subprocess
import sys
import time

ROOT = Path(sys.argv[1])
WORKDIR = sys.argv[2]
LIVE = ("Starting", "Running", "Cancelling")
ORIGINAL_UMASK = os.umask(0o077)


class RunError(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def identity(pid):
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        if fields[0] == "Z":
            return None
        return [Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
                os.readlink(f"/proc/{pid}/ns/pid"), fields[19]]
    except FileNotFoundError:
        return None


def save(directory, state):
    with (directory / "state.tmp").open("w") as stream:
        json.dump(state, stream)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(directory / "state.tmp", directory / "state.json")


@contextlib.contextmanager
def locked(directory):
    with (directory / "lock").open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        yield stream


def read_state(directory):
    try:
        state = json.loads((directory / "state.json").read_text())
    except FileNotFoundError:
        raise RunError(404, "run not found") from None
    if state.get("deleted"):
        raise RunError(410, "run was deleted or expired; use a new ID for a new command")
    if state["status"] in LIVE and identity(state["pid"]) != state["identity"]:
        state.update(status="Interrupted", finishedAt=now(),
                     message="The workspace container or run supervisor stopped before recording an exit code")
        save(directory, state)
    return state


def segments(directory):
    files = sorted((directory / "logs").glob("*.log"))
    return [(int(file.stem), file.stat().st_size, file) for file in files]


def log_window(directory, state):
    if (directory / "logs").is_dir():
        chunks = segments(directory)
        end = chunks[-1][0] + chunks[-1][1] if chunks else 0
        start = max(0, end - state["maxLogBytes"])
        return start, end, chunks
    # Read records created by earlier versions of the runner.
    file = directory / "output.log"
    end = file.stat().st_size if file.exists() else 0
    return 0, end, [(0, end, file)] if end else []


def public(directory, state):
    private = ("pid", "identity", "maxLogBytes", "requestedTimeoutSeconds")
    result = {key: value for key, value in state.items() if key not in private}
    start, end, _ = log_window(directory, state)
    result.update(logBytes=end, logStartOffset=start, logTruncated=start > 0)
    return result


def read_log(directory, state, offset, length=1024 * 1024):
    start, end, chunks = log_window(directory, state)
    if offset < 0 or offset > end:
        raise RunError(416, "offset exceeds available log bytes")
    offset = max(offset, start)
    data = bytearray()
    for first, size, file in chunks:
        if first + size <= offset:
            continue
        with file.open("rb") as stream:
            stream.seek(offset - first)
            block = stream.read(min(length - len(data), first + size - offset))
        data.extend(block)
        offset += len(block)
        if len(data) == length:
            break
    return offset - len(data), bytes(data)


class LogWriter:
    """Append bounded segments without copying the entire retained tail.

    Offsets are encoded in segment names, so a supervisor crash cannot leave a
    stale byte counter. One leading segment may retain an inaccessible prefix;
    physical overhead is bounded by one segment (at most 1 MiB).
    """
    def __init__(self, directory, limit):
        self.directory = directory / "logs"
        self.directory.mkdir(exist_ok=True)
        self.limit = limit
        self.chunk_size = min(1024 * 1024, limit)
        self.chunks = collections.deque()
        self.position = 0

    def write(self, data):
        while data:
            first = self.position // self.chunk_size * self.chunk_size
            file = self.directory / f"{first:020d}.log"
            count = min(len(data), self.chunk_size - (self.position - first))
            if not self.chunks or self.chunks[-1][0] != first:
                self.chunks.append((first, file))
            with file.open("ab", buffering=0) as output:
                output.write(data[:count])
            self.position += count
            data = data[count:]
            while self.chunks and self.chunks[0][0] + self.chunk_size <= self.position - self.limit:
                self.chunks.popleft()[1].unlink()


def kill_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass


def pump(directory, process, options):
    writer = LogWriter(directory, options["maxLogBytes"])
    deadline = time.monotonic() + options["timeoutSeconds"]
    escalation = None
    reason = None
    pipe_open = True
    drain_deadline = None
    with selectors.DefaultSelector() as selector:
        selector.register(process.stdout, selectors.EVENT_READ)
        while pipe_open or process.poll() is None:
            for key, _ in selector.select(timeout=0.1):
                data = os.read(key.fd, 65536)
                if data:
                    with locked(directory):
                        writer.write(data)
                else:
                    selector.unregister(key.fileobj)
                    pipe_open = False
            with locked(directory):
                state = read_state(directory)
                if reason is None and state["status"] == "Cancelling":
                    reason = "cancel"
                if reason is None and time.monotonic() >= deadline:
                    reason = "timeout"
                    state.update(status="Cancelling", message=f"Run timed out after {options['timeoutSeconds']} seconds")
                    save(directory, state)
            if reason is not None and escalation is None:
                escalation = time.monotonic() + 5
                kill_group(process, signal.SIGTERM)
            if escalation is not None and time.monotonic() >= escalation:
                kill_group(process, signal.SIGKILL)
            if process.poll() is not None and drain_deadline is None:
                # Background descendants must stop producing output before the
                # final result is recorded. PID 1's init reaps orphaned children.
                kill_group(process, signal.SIGKILL)
                drain_deadline = time.monotonic() + 1
            if drain_deadline is not None and time.monotonic() >= drain_deadline:
                # A descendant that escaped the process group may still hold
                # the pipe open. It must not keep the supervisor alive forever.
                break
    process.stdout.close()
    return reason


def supervise(directory, command, options):
    process = None
    try:
        with locked(directory):
            state = read_state(directory)
            if state["status"] == "Cancelling":
                state.update(status="Cancelled", exitCode=130, finishedAt=now())
                save(directory, state)
                return
            process = subprocess.Popen(["/bin/sh", "-c", command], cwd=WORKDIR,
                                       start_new_session=True, close_fds=True,
                                       umask=ORIGINAL_UMASK, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT)
        reason = pump(directory, process, options)
        code = process.returncode
        if code < 0:
            code = 128 - code
        with locked(directory):
            state = read_state(directory)
            cancelled = reason == "cancel" or state["status"] == "Cancelling"
            status = "Cancelled" if cancelled else ("Succeeded" if code == 0 else "Failed")
            if reason == "timeout":
                status, code = "TimedOut", 124
            state.update(status=status, exitCode=code, finishedAt=now())
            save(directory, state)
    except BaseException as error:
        if process is not None:
            kill_group(process, signal.SIGKILL)
            process.wait()
        with locked(directory):
            state = read_state(directory)
            state.update(status="Failed", exitCode=127, finishedAt=now(), message=str(error))
            save(directory, state)
    finally:
        # No run lock is held while taking the collection lock: all collection
        # operations lock ROOT before individual runs.
        with locked(ROOT):
            prune(options)


def erase(directory):
    state = read_state(directory)
    if state["status"] in LIVE:
        raise RunError(409, "cancel the run and wait for it to finish before deleting it")
    if (directory / "logs").exists():
        shutil.rmtree(directory / "logs")
    (directory / "output.log").unlink(missing_ok=True)
    # Keep a small tombstone so a retried POST can never execute a deleted ID.
    save(directory, {"id": state["id"], "deleted": True, "deletedAt": now()})


def visible_runs():
    for directory in ROOT.glob("*/"):
        if not (directory / "state.json").is_file():
            continue
        try:
            with locked(directory):
                yield directory, read_state(directory)
        except RunError as error:
            if error.code not in (404, 410):
                raise


def prune(options):
    terminal = []
    cutoff = time.time() - options["retentionSeconds"]
    for directory, state in visible_runs():
        if state["status"] in LIVE:
            continue
        finished = datetime.datetime.fromisoformat(state["finishedAt"]).timestamp()
        terminal.append((finished, directory))
    retained = []
    total = 0
    for finished, directory in sorted(terminal):
        with locked(directory):
            if finished < cutoff:
                erase(directory)
            else:
                _, _, chunks = log_window(directory, read_state(directory))
                size = sum(chunk[1] for chunk in chunks)
                retained.append((directory, size))
                total += size
    for directory, size in retained:
        if total <= options["completedLogBudgetBytes"]:
            break
        with locked(directory):
            erase(directory)
        total -= size


def launch(directory, run_id, command, options, collection_lock):
    directory.mkdir(parents=True, exist_ok=True)
    with locked(directory) as lock:
        if (directory / "state.json").exists():
            state = read_state(directory)
            if state["command"] != command or state.get("requestedTimeoutSeconds", 0) != options["requestedTimeoutSeconds"]:
                raise RunError(409, "run ID already belongs to a different command or timeout")
            return public(directory, state)
        (directory / "logs").mkdir()
        state = dict(id=run_id, command=command, status="Starting", startedAt=now(),
                     pid=os.getpid(), identity=identity(os.getpid()),
                     timeoutSeconds=options["timeoutSeconds"], maxLogBytes=options["maxLogBytes"],
                     requestedTimeoutSeconds=options["requestedTimeoutSeconds"])
        save(directory, state)
        read_fd, write_fd = os.pipe()
        pid = os.fork()
        if pid == 0:
            try:
                os.close(read_fd)
                os.setsid()
                # The supervisor owns no exec connection descriptors. Command
                # output is pumped separately into bounded persistent segments.
                with open(os.devnull, "r+b", buffering=0) as null:
                    for fd in (0, 1, 2):
                        os.dup2(null.fileno(), fd)
                state.update(status="Running", pid=os.getpid(), identity=identity(os.getpid()))
                save(directory, state)
                os.write(write_fd, b"1")
                os.close(write_fd)
                lock.close()
                collection_lock.close()
                supervise(directory, command, options)
            finally:
                os._exit(0)
        os.close(write_fd)
        try:
            if os.read(read_fd, 1) != b"1":
                raise RunError(500, "run supervisor failed to start")
        finally:
            os.close(read_fd)
        return public(directory, read_state(directory))


def active_runs():
    return any(state["status"] in LIVE for _, state in visible_runs())


def other_activity():
    if any(file.name.isdigit() for file in Path("/dev/pts").iterdir()):
        return True
    for file in Path("/proc").glob("[0-9]*/stat"):
        pid = int(file.parent.name)
        if pid in (1, os.getpid()):
            continue
        try:
            fields = file.read_text().rsplit(")", 1)[1].split()
        except FileNotFoundError:
            continue
        if fields[0] != "Z" and fields[1] == "0":
            return True
    return False


def stream_log(directory, offset, end, follow, duration):
    deadline = time.monotonic() + duration
    result = {}
    while True:
        with locked(directory):
            state = read_state(directory)
            run = public(directory, state)
            # Never silently skip bytes mid-response. Reconnect with a new
            # X-Log-Offset header when rotation overtakes a slow reader.
            if offset < run["logStartOffset"]:
                result = {"run": run, "error": "log tail advanced; reconnect to resume", "code": 409}
                break
            actual, data = read_log(directory, state, offset)
            if not follow:
                data = data[:max(0, end - actual)]
        if data:
            sys.stdout.buffer.write(data)
            sys.stdout.buffer.flush()
            offset += len(data)
        result = {"run": run}
        if (not follow and offset >= end) or (state["status"] not in LIVE and offset >= run["logBytes"]):
            break
        if time.monotonic() >= deadline:
            break
        if not data:
            time.sleep(0.2)
    result["nextOffset"] = offset
    print(json.dumps(result), file=sys.stderr, flush=True)


def main():
    action = sys.argv[3]
    if action in ("active", "active-runs"):
        return {"active": active_runs() or (action == "active" and other_activity())}
    if action == "list":
        runs = [public(directory, state) for directory, state in visible_runs()]
        runs.sort(key=lambda run: (datetime.datetime.fromisoformat(run["startedAt"]).timestamp(), run["id"]), reverse=True)
        offset, limit = int(sys.argv[4]), int(sys.argv[5])
        return {"runs": runs[offset:offset + limit]}
    run_id = sys.argv[4]
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", run_id):
        raise RunError(400, "invalid run ID")
    directory = ROOT / run_id
    if action == "create":
        options = json.loads(sys.argv[6])
        ROOT.mkdir(parents=True, exist_ok=True)
        with locked(ROOT) as collection_lock:
            # Check for an existing/tombstoned ID before GC, so even a retry of
            # an expired run cannot accidentally become a fresh execution.
            if not (directory / "state.json").exists():
                prune(options)
            return {"run": launch(directory, run_id, sys.argv[5], options, collection_lock)}
    if not directory.is_dir():
        raise RunError(404, "run not found")
    if action == "delete":
        with locked(ROOT), locked(directory):
            try:
                erase(directory)
            except RunError as error:
                if error.code != 410:
                    raise
        return {}
    if action == "stream":
        stream_log(directory, int(sys.argv[5]), int(sys.argv[6]), sys.argv[7] == "true", float(sys.argv[8]))
        return None
    with locked(directory):
        state = read_state(directory)
        if action == "cancel" and state["status"] in LIVE:
            state["status"] = "Cancelling"
            save(directory, state)
        result = {"run": public(directory, state)}
        if action == "logs":
            offset, data = read_log(directory, state, int(sys.argv[5]))
            result.update(offset=offset, data=base64.b64encode(data).decode("ascii"))
        return result


try:
    result = main()
    if result is not None:
        print(json.dumps(result), flush=True)
except RunError as error:
    output = sys.stderr if sys.argv[3] == "stream" else sys.stdout
    print(json.dumps({"code": error.code, "error": str(error)}), file=output, flush=True)
