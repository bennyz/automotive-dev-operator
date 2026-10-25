//go:build linux

package workspacerun

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchRetryRecoversFromInitialStateWriteFailure(t *testing.T) {
	root, call := localRunner(t)
	directory := filepath.Join(root, ".runs", "retry")
	// Prevent the initial record from being written after logs/ is created.
	// Removing the obstruction must let the same request launch exactly once.
	if err := os.MkdirAll(filepath.Join(directory, "state.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	const command = `echo execution >> count; printf recovered; exit 3`
	if got := call("create", "retry", command, DefaultOptions().JSON()); got.Code != 500 || !strings.Contains(got.Error, "IsADirectoryError") {
		t.Fatalf("initial state write: %+v", got)
	}
	if info, err := os.Stat(filepath.Join(directory, "logs")); err != nil || !info.IsDir() {
		t.Fatalf("failed launch did not leave a log directory: %v", err)
	}
	for _, name := range []string{filepath.Join(directory, "state.json"), filepath.Join(root, "count")} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("failed launch created %s: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(directory, "state.tmp")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := call("create", "retry", command, DefaultOptions().JSON()); got.Code != 0 {
			t.Fatalf("retry after storage recovery: %+v", got)
		}
		run := awaitRun(t, call, "retry")
		if run.Status != "Failed" || run.ExitCode == nil || *run.ExitCode != 3 {
			t.Fatalf("recovered run: %+v", run)
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "count")); err != nil || string(data) != "execution\n" {
		t.Fatalf("retry did not execute exactly once: %q, %v", data, err)
	}
	if got := streamRunLog(t, root, "retry", "0"); got.Code != 0 || string(got.Bytes) != "recovered" {
		t.Fatalf("recovered log: %+v", got)
	}
}

func TestDirectorySyncFailurePreventsCommandLaunch(t *testing.T) {
	root, call := localRunner(t)
	// Inject an I/O error only when syncing a directory. File writes and the
	// rename still succeed, but the command must not start without a durable record.
	const failDirectorySync = `import errno, os, stat
original_fsync = os.fsync
def fail_directory_sync(fd):
    if stat.S_ISDIR(os.fstat(fd).st_mode):
        raise OSError(errno.EIO, "injected directory sync failure")
    original_fsync(fd)
os.fsync = fail_directory_sync
`
	argv := Command("create", "sync", "echo started > started", DefaultOptions().JSON())
	argv[3] = failDirectorySync + argv[3]
	argv[4], argv[5] = filepath.Join(root, ".runs"), root
	out, stderr := executeRunner(t, root, argv)
	var result runnerResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("directory sync response: %q, %v", out, err)
	}
	if result.Code == 0 {
		awaitRun(t, call, "sync")
	}
	if result.Code != 500 || !strings.Contains(result.Error, "injected directory sync failure") || len(stderr) != 0 {
		t.Fatalf("directory sync failure was not reported: %+v, stderr=%q", result, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".runs", "sync", "state.json")); err != nil {
		t.Fatalf("failure occurred before replacing the state file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "started")); !os.IsNotExist(err) {
		t.Fatalf("command started before its record was durable: %v", err)
	}
	if got := call("get", "sync"); got.Code != 0 || got.Run.Status != "Interrupted" || got.Run.ExitCode != nil {
		t.Fatalf("unsynced launch must not invent an exit code: %+v", got)
	}
}

func TestUnreadableRecordsDoNotBlockCollectionsOrLaunch(t *testing.T) {
	root, call := localRunner(t)
	for _, field := range []string{"truncated", "finishedAt", "maxLogBytes", "exitCode", "message"} {
		filename := writeTerminalRun(t, root, strings.ToLower(field), time.Hour, nil)
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		switch field {
		case "truncated":
			data = []byte(`{"id":`)
		case "finishedAt":
			delete(state, field)
		default:
			state[field] = []string{"wrong type"}
		}
		if field != "truncated" {
			data, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := call("get", strings.ToLower(field)); got.Code != 422 || !strings.Contains(got.Error, "unreadable run record") {
			t.Fatalf("bad %s record: %+v", field, got)
		}
	}
	if got := call("active"); got.Code != 0 || got.Active {
		t.Fatalf("bad records block auto-pause: %+v", got)
	}
	if got := call("list", "0", "100"); got.Code != 0 || len(got.Runs) != 0 {
		t.Fatalf("bad records block list: %+v", got)
	}
	if got := call("create", "healthy", "sleep 30", DefaultOptions().JSON()); got.Code != 0 || got.Run.Status != "Running" {
		t.Fatalf("bad records block launch: %+v", got)
	}
	t.Cleanup(func() { call("cancel", "healthy"); awaitRun(t, call, "healthy") })
	if got := call("active"); got.Code != 0 || !got.Active {
		t.Fatalf("healthy run lost among bad records: %+v", got)
	}
	if got := call("list", "0", "100"); got.Code != 0 || len(got.Runs) != 1 || got.Runs[0].ID != "healthy" {
		t.Fatalf("healthy run missing from list: %+v", got)
	}
}

func TestUnexpectedRunnerFailuresPreserveJSONProtocol(t *testing.T) {
	root, _ := localRunner(t)
	if err := os.WriteFile(filepath.Join(root, ".runs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	argv := Command("create", "run", "true", DefaultOptions().JSON())
	argv[4], argv[5] = filepath.Join(root, ".runs"), root
	out, stderr := executeRunner(t, root, argv)
	var result runnerResult
	if err := json.Unmarshal(out, &result); err != nil || result.Code != 500 || !strings.Contains(result.Error, "FileExistsError") || len(stderr) != 0 {
		t.Fatalf("unexpected create error: %q stderr=%q decode=%v", out, stderr, err)
	}
	if err := os.Remove(filepath.Join(root, ".runs")); err != nil {
		t.Fatal(err)
	}
	writeTerminalRun(t, root, "run", time.Hour, nil)
	argv = Command("stream", "run", "0", "0", "true", "invalid-duration")
	argv[4], argv[5] = filepath.Join(root, ".runs"), root
	out, stderr = executeRunner(t, root, argv)
	if err := json.Unmarshal(stderr, &result); err != nil || result.Code != 500 || !strings.Contains(result.Error, "ValueError") || len(out) != 0 {
		t.Fatalf("unexpected stream error: %q stderr=%q decode=%v", out, stderr, err)
	}
}

func TestRunnerIsolatedFromWorkspaceAndPythonEnvironment(t *testing.T) {
	root, call := localRunner(t)
	for _, name := range []string{"json.py", "subprocess.py"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(`raise RuntimeError("workspace module hijacked runner")`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PYTHONPATH", root)
	t.Setenv("PYTHONHOME", filepath.Join(root, "invalid-python-home"))
	if got := call("create", "isolated", "printf isolated", DefaultOptions().JSON()); got.Code != 0 {
		t.Fatalf("untrusted Python imports blocked launch: %+v", got)
	}
	if run := awaitRun(t, call, "isolated"); run.Status != "Succeeded" {
		t.Fatalf("isolated run: %+v", run)
	}
	if got := streamRunLog(t, root, "isolated", "0"); string(got.Bytes) != "isolated" || got.Code != 0 {
		t.Fatalf("isolated stream: %+v", got)
	}
	proc, pts := filepath.Join(root, "proc"), filepath.Join(root, "pts")
	for _, directory := range []string{filepath.Join(proc, "123"), pts} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proc, "123", "stat"), []byte("123 (background worker) S 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-matching /proc entry must preserve the Python argv, and activity
	// must use the same isolated interpreter as every other action.
	argv := ActivityCommand()
	argv[4], argv[5], argv[10], argv[11] = proc, pts, filepath.Join(root, ".runs"), root
	out, stderr := executeRunner(t, root, argv)
	if string(out) != "{\"active\": false}\n" || len(stderr) != 0 {
		t.Fatalf("isolated activity: %q, %q", out, stderr)
	}
}

func TestBudgetSmallerThanLogCapKeepsLatestTailAndExitCode(t *testing.T) {
	root, call := localRunner(t)
	options := DefaultOptions()
	options.CompletedLogBudgetBytes = 1 << 20
	// Cross segment boundaries and finish mid-segment to exercise trimming.
	command := `python3 -c 'import sys; sys.stdout.buffer.write(b"x" * (2*1024*1024+123))'; printf 'last error'; exit 3`
	call("create", "large", command, options.JSON())
	run := awaitRun(t, call, "large")
	if run.Status != "Failed" || run.ExitCode == nil || *run.ExitCode != 3 || run.LogsEvicted || run.LogBytes-run.LogStartOffset != 1<<20 {
		t.Fatalf("budget lost latest result or tail: %+v", run)
	}
	got := streamRunLog(t, root, "large", "0")
	if got.Code != 0 || len(got.Bytes) != 1<<20 || !bytes.HasSuffix(got.Bytes, []byte("last error")) || got.Offset != run.LogStartOffset {
		t.Fatalf("budget tail: offset=%d bytes=%d code=%d", got.Offset, len(got.Bytes), got.Code)
	}
	// Wait for completion's collection cleanup, then repeat an independent
	// cleanup to verify the result and log still survive the budget.
	call("create", "after", "true", options.JSON())
	awaitRun(t, call, "after")
	if got := call("get", "large").Run; got.LogsEvicted || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("post-completion pruning lost latest non-empty log: %+v", got)
	}
}

func TestTombstonesExpireAndStayOutsideRunCollections(t *testing.T) {
	root, call := localRunner(t)
	writeTerminalRun(t, root, "old", time.Hour, []byte("old output"))
	if got := call("delete", "old"); got.Code != 0 {
		t.Fatal(got)
	}
	tombstone := filepath.Join(root, ".runs", ".deleted", "old")
	if got := call("create", "old", "true", DefaultOptions().JSON()); got.Code != 410 {
		t.Fatalf("retry window not protected: %+v", got)
	}
	expired := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(tombstone, expired, expired); err != nil {
		t.Fatal(err)
	}
	if got := call("get", "old"); got.Code != 404 {
		t.Fatalf("expired tombstone still visible: %+v", got)
	}
	// Tombstones are separate from run directories and never parsed as records.
	if got := call("list", "0", "100"); got.Code != 0 || len(got.Runs) != 0 {
		t.Fatalf("tombstones listed as runs: %+v", got)
	}
	if got := call("active"); got.Code != 0 || got.Active {
		t.Fatalf("tombstones blocked auto-pause: %+v", got)
	}
	call("create", "trigger", "true", DefaultOptions().JSON())
	awaitRun(t, call, "trigger")
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("expired tombstone not collected: %v", err)
	}
	// IDs are documented as single-use. Once their retry window is gone,
	// expired tombstones must not indefinitely reject a new request.
	if got := call("create", "old", "true", DefaultOptions().JSON()); got.Code != 0 {
		t.Fatalf("expired tombstone blocked launch: %+v", got)
	}
	awaitRun(t, call, "old")
}
