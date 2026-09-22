package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockfileBuildCommands(t *testing.T) {
	data, err := os.ReadFile("scripts/build_image.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	_, rest, ok := strings.Cut(script, "declare -a LOCKFILE_ARGS=()")
	if !ok {
		t.Fatal("lockfile argument setup missing")
	}
	setup, _, ok := strings.Cut(rest, "declare -a ROOT_PASSWORD_ARGS=()")
	if !ok {
		t.Fatal("root password setup missing")
	}
	_, rest, ok = strings.Cut(script, "run_bootc() {")
	if !ok {
		t.Fatal("bootc function missing")
	}
	functions, _, ok := strings.Cut(rest, "\ncase ")
	if !ok {
		t.Fatal("build dispatch missing")
	}
	for _, locked := range []bool{false, true} {
		name := "unlocked"
		if locked {
			name = "locked"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config with spaces")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if locked {
				if err := os.WriteFile(filepath.Join(dir, "aib.lock"), []byte(`{"version":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []string{"bootc", "traditional", "disk"} {
				t.Run(mode, func(t *testing.T) {
					body := `
set -e
NEEDS_DISK=true
SPLIT_BUILD=true
DISTRO=autosd
TARGET=qemu
ARCH=aarch64
BOOTC_CONTAINER_NAME=test-image
MANIFEST_FILE=manifest.aib.yml
EXPORT_FILE=output.qcow2
CONTAINER_REF=quay.io/test/image
LOCAL_BUILDER_IMAGE=builder
run_aib_command() { shift; "$@"; }
aib() { printf 'CALL'; printf ' <%s>' "$@"; printf '\n'; }
aib-dev() { aib "$@"; }
start_container_push() { :; }
pull_registry_image() { :; }
log_elapsed() { :; }
`
					cmd := exec.Command("bash", "-c", body+"\ndeclare -a LOCKFILE_ARGS=()"+setup+"\nrun_bootc() {"+functions+"\nrun_"+mode)
					cmd.Env = append(os.Environ(), "MANIFEST_CONFIG_PATH="+dir)
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("script failed: %v\n%s", err, out)
					}
					calls := 0
					for line := range strings.SplitSeq(string(out), "\n") {
						if !strings.HasPrefix(line, "CALL") {
							continue
						}
						calls++
						want := locked && !strings.Contains(line, "<to-disk-image>")
						has := strings.Contains(line, "<--lockfile> <"+filepath.Join(dir, "aib.lock")+">")
						if has != want {
							t.Fatalf("lockfile argument mismatch: %s", line)
						}
					}
					wantCalls := 1
					if mode == "bootc" {
						wantCalls = 2
					}
					if calls != wantCalls {
						t.Fatalf("got %d AIB calls, want %d: %s", calls, wantCalls, out)
					}
				})
			}
		})
	}
}

func TestLockfileReproducibilityArtifacts(t *testing.T) {
	buildScript, err := os.ReadFile("scripts/build_image.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`rm -f "$WORKSPACE_PATH/aib.lock"`,
		`cp "$MANIFEST_CONFIG_PATH/aib.lock" "$WORKSPACE_PATH/aib.lock"`,
	} {
		if !strings.Contains(string(buildScript), want) {
			t.Fatalf("build script missing %q", want)
		}
	}

	pushScript, err := os.ReadFile("scripts/push_artifact.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if [ -f "./aib.lock" ]; then`,
		`"$OCI_REFERRER_TYPE_AIB_LOCKFILE" "AIB lockfile"`,
	} {
		if !strings.Contains(string(pushScript), want) {
			t.Fatalf("push script missing %q", want)
		}
	}

	findManifestScript, err := os.ReadFile("scripts/find_manifest.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(findManifestScript), "|aib.lock|") {
		t.Fatal("manifest discovery does not exclude a lockfile left in the shared workspace")
	}
}

func TestPackageReproducibleInputsReplacesStaleLockfile(t *testing.T) {
	data, err := os.ReadFile("scripts/build_image.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "package_reproducible_inputs() {")
	if !ok {
		t.Fatal("reproducible inputs function missing")
	}
	functionBody, _, ok := strings.Cut(rest, "\npackage_reproducible_inputs")
	if !ok {
		t.Fatal("reproducible inputs function call missing")
	}
	script := "package_reproducible_inputs() {" + functionBody + "\npackage_reproducible_inputs\n"

	for _, tt := range []struct {
		name     string
		lockfile string
	}{
		{name: "current lockfile", lockfile: `{"version":1}`},
		{name: "no current lockfile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			buildDir := filepath.Join(root, "build")
			workspaceDir := filepath.Join(root, "workspace")
			configDir := filepath.Join(root, "config")
			for _, dir := range []string{filepath.Join(buildDir, "osbuild_store", "sources"), workspaceDir, configDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(
				filepath.Join(buildDir, "osbuild_store", "hermeto-rpm-bom.json"),
				[]byte(`{"bomFormat":"CycloneDX"}`),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(root, "manifest.aib.yml")
			if err := os.WriteFile(manifestPath, []byte("name: test\n"), 0600); err != nil {
				t.Fatal(err)
			}
			lockfilePath := filepath.Join(workspaceDir, "aib.lock")
			if err := os.WriteFile(lockfilePath, []byte("stale"), 0600); err != nil {
				t.Fatal(err)
			}
			if tt.lockfile != "" {
				if err := os.WriteFile(filepath.Join(configDir, "aib.lock"), []byte(tt.lockfile), 0600); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(),
				"REPRODUCIBLE=true",
				"BUILD_DIR="+buildDir,
				"WORKSPACE_PATH="+workspaceDir,
				"MANIFEST_FILE="+manifestPath,
				"MANIFEST_CONFIG_PATH="+configDir,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}
			archive := filepath.Join(workspaceDir, "build-sources.tar.gz")
			list := exec.Command("tar", "-tzf", archive)
			contents, err := list.CombinedOutput()
			if err != nil {
				t.Fatalf("listing sources archive: %v\n%s", err, contents)
			}
			if !strings.Contains(string(contents), "hermeto-rpm-bom.json") {
				t.Fatalf("Hermeto SBOM missing from sources archive:\n%s", contents)
			}

			got, err := os.ReadFile(lockfilePath)
			if tt.lockfile == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("stale lockfile remains: contents=%q err=%v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.lockfile {
				t.Fatalf("lockfile = %q, want %q", got, tt.lockfile)
			}
		})
	}
}
