//go:build linux

package workspacerun

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
)

type runnerResult struct {
	Run    buildcontract.WorkspaceRun `json:"run"`
	Data   []byte                     `json:"data"`
	Active bool                       `json:"active"`
	Code   int                        `json:"code"`
}

func localRunner(t *testing.T) (string, func(string, ...string) runnerResult) {
	t.Helper()
	root := t.TempDir()
	return root, func(action string, args ...string) runnerResult {
		t.Helper()
		argv := Command(action, args...)
		argv[3], argv[4] = filepath.Join(root, ".runs"), root
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("runner %s: %v: %s", action, err, stderr.String())
		}
		var result runnerResult
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("runner %s: %v: %s", action, err, out)
		}
		return result
	}
}

func awaitRun(t *testing.T, call func(string, ...string) runnerResult, id string) buildcontract.WorkspaceRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run := call("get", id).Run
		if run.Terminal() {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return buildcontract.WorkspaceRun{}
}

func TestRunnerRetainsOutputAndResultAfterLauncherExits(t *testing.T) {
	root, call := localRunner(t)
	command := `printf 'one\000'; while [ ! -f release ]; do sleep 0.05; done; printf '\377two' >&2; exit 23`
	run := call("create", "durable", command).Run
	if run.ID != "durable" || run.Status != "Running" {
		t.Fatalf("create: %+v", run)
	}
	t.Cleanup(func() { call("cancel", "durable"); awaitRun(t, call, "durable") })
	if !call("active").Active {
		t.Fatal("detached command did not count as activity")
	}
	// Repeating a request after the launch connection was lost must not execute
	// another command, and a different payload must not overwrite its output.
	if got := call("create", "durable", command).Run; got.StartedAt != run.StartedAt {
		t.Fatalf("duplicate creation: %+v", got)
	}
	if got := call("create", "durable", "echo duplicate").Code; got != 409 {
		t.Fatalf("conflicting command: %d", got)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run = awaitRun(t, call, "durable")
	if run.Status != "Failed" || run.ExitCode == nil || *run.ExitCode != 23 || run.FinishedAt == "" {
		t.Fatalf("result: %+v", run)
	}
	if got := call("logs", "durable", "2").Data; !bytes.Equal(got, []byte{'e', 0, 255, 't', 'w', 'o'}) {
		t.Fatalf("resumed bytes: %q", got)
	}
	if got := call("logs", "durable", "8"); len(got.Data) != 0 || got.Code != 0 {
		t.Fatalf("EOF: %+v", got)
	}
	if got := call("logs", "durable", "9").Code; got != 416 {
		t.Fatalf("offset beyond EOF: %d", got)
	}
	if call("active").Active {
		t.Fatal("finished run counted as activity")
	}
	if got := call("cancel", "durable").Run; got.Status != "Failed" || *got.ExitCode != 23 {
		t.Fatalf("cancellation changed completed result: %+v", got)
	}
}

func TestRunnerConcurrentCreationExecutesOnce(t *testing.T) {
	root, call := localRunner(t)
	var group sync.WaitGroup
	for range 6 {
		group.Go(func() { call("create", "once", "echo execution >> count; sleep 0.2") })
	}
	group.Wait()
	awaitRun(t, call, "once")
	data, err := os.ReadFile(filepath.Join(root, "count"))
	if err != nil || string(data) != "execution\n" {
		t.Fatalf("duplicate execution: %q, %v", data, err)
	}
}

func TestRunnerCancellationKillsTERMResistantDescendants(t *testing.T) {
	root, call := localRunner(t)
	call("create", "cancel", `trap '' TERM; sh -c 'trap "" TERM; echo ready > ready; while :; do echo tick >> ticks; sleep 0.05; done' & wait`)
	t.Cleanup(func() { call("cancel", "cancel"); awaitRun(t, call, "cancel") })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command failed to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := call("cancel", "cancel").Run.Status; got != "Cancelling" {
		t.Fatalf("cancel: %s", got)
	}
	call("cancel", "cancel")
	run := awaitRun(t, call, "cancel")
	if run.Status != "Cancelled" || run.ExitCode == nil || *run.ExitCode != 137 {
		t.Fatalf("cancellation result: %+v", run)
	}
	before, err := os.ReadFile(filepath.Join(root, "ticks"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(root, "ticks"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("descendant survived cancellation")
	}
}

func TestRunnerRecoversStaleIdentityWithoutInventingExitCode(t *testing.T) {
	root, call := localRunner(t)
	call("create", "stale", "printf retained")
	awaitRun(t, call, "stale")
	filename := filepath.Join(root, ".runs", "stale", "state.json")
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["status"], state["pid"] = "Running", os.Getpid()
	state["identity"] = []string{"old-boot", "old-container", "old-process"}
	delete(state, "exitCode")
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if call("active").Active {
		t.Fatal("stale run prevents auto-pause")
	}
	run := call("cancel", "stale").Run
	if run.Status != "Interrupted" || run.ExitCode != nil || !strings.Contains(run.Message, "exit code") {
		t.Fatalf("stale result: %+v", run)
	}
	if got := string(call("logs", "stale", "0").Data); got != "retained" {
		t.Fatalf("lost persisted log: %q", got)
	}
	if got := call("get", "../stale").Code; got != 400 {
		t.Fatalf("invalid ID accepted: %d", got)
	}
	if got := call("get", "missing").Code; got != 404 {
		t.Fatalf("missing run: %d", got)
	}
}
