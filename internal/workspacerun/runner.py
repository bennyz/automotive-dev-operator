"""Durable workspace runs. Invoked through pod exec; no daemon is required."""

import base64
import contextlib
import datetime
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

ROOT = Path(sys.argv[1])
WORKDIR = sys.argv[2]
LIVE = ("Starting", "Running", "Cancelling")


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
    # All writers hold the run lock. Readers never observe a partial result.
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
    if state["status"] in LIVE and identity(state["pid"]) != state["identity"]:
        state.update(status="Interrupted", finishedAt=now(),
                     message="The workspace container or run supervisor stopped before recording an exit code")
        save(directory, state)
    return state


def public(directory, state):
    result = {key: value for key, value in state.items() if key not in ("pid", "identity")}
    result["logBytes"] = (directory / "output.log").stat().st_size
    return result


def kill_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass


def supervise(directory, command):
    process = None
    try:
        with locked(directory):
            state = read_state(directory)
            if state["status"] == "Cancelling":
                state.update(status="Cancelled", exitCode=130, finishedAt=now())
                save(directory, state)
                return
            # start_new_session calls setsid before exec. All ordinary descendants
            # share this group, so cancellation also reaches make's compiler jobs.
            process = subprocess.Popen(["/bin/sh", "-c", command], cwd=WORKDIR,
                                       start_new_session=True, close_fds=True)
        cancelling = False
        deadline = None
        while process.poll() is None:
            with locked(directory):
                state = read_state(directory)
            if state["status"] == "Cancelling" and not cancelling:
                cancelling = True
                deadline = time.monotonic() + 5
                kill_group(process, signal.SIGTERM)
            if deadline is not None and time.monotonic() >= deadline:
                kill_group(process, signal.SIGKILL)
            time.sleep(0.1)
        # Do not leave background children writing into an already completed log.
        kill_group(process, signal.SIGKILL)
        code = process.returncode
        if code < 0:
            code = 128 - code
        with locked(directory):
            state = read_state(directory)
            cancelled = cancelling or state["status"] == "Cancelling"
            state.update(status="Cancelled" if cancelled else ("Succeeded" if code == 0 else "Failed"),
                         exitCode=code, finishedAt=now())
            save(directory, state)
    except BaseException as error:
        if process is not None:
            kill_group(process, signal.SIGKILL)
            process.wait()
        with locked(directory):
            state = read_state(directory)
            state.update(status="Failed", exitCode=127, finishedAt=now(), message=str(error))
            save(directory, state)


def launch(directory, run_id, command):
    directory.mkdir(parents=True, exist_ok=True)
    with locked(directory) as lock:
        if (directory / "state.json").exists():
            state = read_state(directory)
            if state["command"] != command:
                raise RunError(409, "run ID already belongs to a different command")
            return public(directory, state)
        (directory / "output.log").touch()
        state = dict(id=run_id, command=command, status="Starting", startedAt=now(),
                     pid=os.getpid(), identity=identity(os.getpid()))
        save(directory, state)
        read_fd, write_fd = os.pipe()
        pid = os.fork()
        if pid == 0:
            try:
                os.close(read_fd)
                os.setsid()
                # Detach every standard stream from pod exec before acknowledging
                # launch. Neither the HTTP client nor API lifetime owns this run.
                with open(os.devnull, "rb") as stdin, (directory / "output.log").open("ab", buffering=0) as output:
                    os.dup2(stdin.fileno(), 0)
                    os.dup2(output.fileno(), 1)
                    os.dup2(output.fileno(), 2)
                signal.signal(signal.SIGHUP, signal.SIG_IGN)
                state.update(status="Running", pid=os.getpid(), identity=identity(os.getpid()))
                save(directory, state)
                os.write(write_fd, b"1")
                os.close(write_fd)
                # Drop the inherited lock descriptor; the parent unlocks after
                # receiving the acknowledgement, before we acquire our own lock.
                lock.close()
                supervise(directory, command)
            finally:
                os._exit(0)
        os.close(write_fd)
        try:
            if os.read(read_fd, 1) != b"1":
                raise RunError(500, "run supervisor failed to start")
        finally:
            os.close(read_fd)
        return public(directory, read_state(directory))


def main():
    action = sys.argv[3]
    if action == "active":
        for directory in ROOT.glob("*/"):
            if not (directory / "state.json").is_file():
                continue
            with locked(directory):
                if read_state(directory)["status"] in LIVE:
                    return {"active": True}
        return {"active": False}
    run_id = sys.argv[4]
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", run_id):
        raise RunError(400, "invalid run ID")
    directory = ROOT / run_id
    if action == "create":
        return {"run": launch(directory, run_id, sys.argv[5])}
    if not directory.is_dir():
        raise RunError(404, "run not found")
    with locked(directory):
        state = read_state(directory)
        if action == "cancel" and state["status"] in LIVE:
            state["status"] = "Cancelling"
            save(directory, state)
        result = {"run": public(directory, state)}
        if action == "logs":
            offset = int(sys.argv[5])
            size = result["run"]["logBytes"]
            if offset < 0 or offset > size:
                raise RunError(416, "offset exceeds available log bytes")
            with (directory / "output.log").open("rb") as stream:
                stream.seek(offset)
                data = stream.read(min(65536, size - offset))
            result["data"] = base64.b64encode(data).decode("ascii")
        return result


try:
    os.umask(0o077)
    print(json.dumps(main()), flush=True)
except RunError as error:
    print(json.dumps({"code": error.code, "error": str(error)}), flush=True)
