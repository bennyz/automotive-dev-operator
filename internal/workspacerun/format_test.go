//go:build linux

package workspacerun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunnerRequestUsesStdinAndPreservesLiteralContents(t *testing.T) {
	root, call := localRunner(t)
	const output = "quoted ' and \"; \\ backslash\n🙂\u2028end\n"
	const command = "cat <<'DATA'\n" + output + "DATA\n"
	argv, input := Command("create", "stdin", command, DefaultOptions().JSON())
	if !slices.Equal(argv, []string{"python3", "-I", "-"}) {
		t.Fatalf("runner or request payload included in exec argv: %d arguments", len(argv))
	}
	if !strings.Contains(input, script) {
		t.Fatal("runner source missing from stdin")
	}
	if got := call("create", "stdin", command, DefaultOptions().JSON()); got.Code != 0 {
		t.Fatalf("stdin launch: %+v", got)
	}
	if run := awaitRun(t, call, "stdin"); run.Status != "Succeeded" || run.Command != command {
		t.Fatalf("request contents changed in transit: %+v", run)
	}
	if got := streamRunLog(t, root, "stdin", "0"); string(got.Bytes) != output {
		t.Fatalf("stdin command output: %q", got.Bytes)
	}
	if got := call("get", "\"]; open('injected', 'w').close(); #\n"); got.Code != 400 {
		t.Fatalf("request data escaped into Python: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
		t.Fatalf("request data executed as Python: %v", err)
	}
}

func TestRunRecordsPersistVersionOne(t *testing.T) {
	root, call := localRunner(t)
	call("create", "versioned", "sleep 30", DefaultOptions().JSON())
	t.Cleanup(func() { call("cancel", "versioned"); awaitRun(t, call, "versioned") })
	for _, phase := range []string{"running", "cancelled"} {
		if phase == "cancelled" {
			call("cancel", "versioned")
			awaitRun(t, call, "versioned")
		}
		data, err := os.ReadFile(filepath.Join(root, ".runs", "versioned", "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil || state["version"] != float64(1) {
			t.Fatalf("%s record lacks format version 1: %s, %v", phase, data, err)
		}
	}
}

func TestUnversionedRecordsRemainReadable(t *testing.T) {
	root, call := localRunner(t)
	filename := writeTerminalRun(t, root, "unversioned", time.Hour, []byte("old output"))
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	delete(state, "version")
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := call("get", "unversioned"); got.Code != 0 || got.Run.ExitCode == nil || *got.Run.ExitCode != 3 {
		t.Fatalf("unversioned result lost during upgrade: %+v", got)
	}
	if got := streamRunLog(t, root, "unversioned", "0"); string(got.Bytes) != "old output" {
		t.Fatalf("unversioned output lost during upgrade: %q", got.Bytes)
	}
}

func TestUnknownRecordVersionFailsClosedWithoutMutation(t *testing.T) {
	root, call := localRunner(t)
	filename := writeTerminalRun(t, root, "future", time.Hour, nil)
	// A newer format may have entirely different required fields and statuses.
	const record = `{"version":2,"status":"NewLiveStatus"}`
	if err := os.WriteFile(filename, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"get", "future"}, {"active"}, {"list", "0", "100"}, {"cancel", "future"}, {"delete", "future"},
		{"create", "new", "true", DefaultOptions().JSON()},
	} {
		got := call(args[0], args[1:]...)
		if got.Code != 409 || !strings.Contains(got.Error, "unsupported run record version 2") {
			t.Fatalf("%s did not preserve unknown format: %+v", args[0], got)
		}
	}
	if data, err := os.ReadFile(filename); err != nil || string(data) != record {
		t.Fatalf("unknown format overwritten or removed: %s, %v", data, err)
	}
}
