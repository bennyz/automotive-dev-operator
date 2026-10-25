package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableScriptRetainsStrictModeAndCleansUp(t *testing.T) {
	for _, script := range []string{
		"exit 37\necho must-not-run\n",
		"false\necho must-not-run\n",
		"false | true\necho must-not-run\n",
		"cat >/dev/null\nfalse\necho must-not-run\n",
		"unset CAIB_TEST_UNSET_VARIABLE\necho \"${CAIB_TEST_UNSET_VARIABLE}\"\necho must-not-run\n",
		"printf 'script-ok\\n'\n",
	} {
		t.Run(strings.SplitN(script, "\n", 2)[0], func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "build script.sh")
			if err := os.WriteFile(filename, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := newExecCmd()
			if err := cmd.Flags().Set("script", filename); err != nil {
				t.Fatal(err)
			}
			command, err := workspaceRunCommand(cmd, nil)
			if err != nil {
				t.Fatal(err)
			}
			scriptDir := t.TempDir()
			process := exec.Command("/bin/sh", "-c", command)
			process.Env = append(os.Environ(), "TMPDIR="+scriptDir)
			output, err := process.CombinedOutput()
			success := strings.HasPrefix(script, "printf")
			if (err == nil) != success || strings.Contains(string(output), "must-not-run") || (success && string(output) != "script-ok\n") {
				t.Fatalf("script strict mode: err=%v output=%q", err, output)
			}
			if strings.HasPrefix(script, "exit 37") && process.ProcessState.ExitCode() != 37 {
				t.Fatalf("script exit status: %d", process.ProcessState.ExitCode())
			}
			files, err := os.ReadDir(scriptDir)
			if err != nil || len(files) != 0 {
				t.Fatalf("script was not cleaned up: %v, %v", files, err)
			}
		})
	}
}
