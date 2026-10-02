package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuilderCache(t *testing.T) {
	for _, entrypoint := range []string{"build", "prepare"} {
		for _, tc := range []struct {
			name, scenario string
			force, fails   bool
			pulls, pushes  int
		}{
			{name: "unchanged", scenario: "unchanged", pulls: 1},
			{name: "changed inputs", scenario: "changed", pulls: 2, pushes: 1},
			{name: "cache miss", scenario: "missing", pulls: 1, pushes: 1},
			{name: "forced identical rebuild", scenario: "unchanged", force: true, pulls: 1, pushes: 1},
			{name: "cached pull fails", scenario: "pull-fail", fails: true},
			{name: "freshness check fails", scenario: "aib-fail", fails: true},
			{name: "local inspect fails", scenario: "inspect-fail", fails: true},
			{name: "local digest empty", scenario: "inspect-empty", fails: true},
			{name: "push fails", scenario: "push-fail", fails: true},
			{name: "push digest missing", scenario: "digest-missing", fails: true},
			{name: "push digest invalid", scenario: "digest-invalid", fails: true},
			{name: "cache digest invalid", scenario: "cache-invalid", fails: true},
			{name: "pushed digest pull fails", scenario: "final-pull-fail", fails: true},
		} {
			if entrypoint == "prepare" && tc.scenario == "final-pull-fail" {
				continue // The prepare task has no local consumer after the push.
			}
			t.Run(entrypoint+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				body := builderCacheHarness + builderCacheScript
				if entrypoint == "build" {
					body += shellFunctions(t, buildImageScript, "cleanup() {", "fail() {")
					body += shellFunctions(t, buildImageScript, "prepare_builder_if_needed() {", "\nprepare_builder_if_needed\n") + "\nprepare_builder_if_needed\n"
				} else {
					body += strings.ReplaceAll(buildBuilderScript, "$(workspaces.manifest-config-workspace.path)", dir)
				}
				body += "\nprintf 'SUCCESS <%s>\\n' \"$(cat \"$RESULT_PATH\")\"\n"
				force := "false"
				if tc.force {
					force = "true"
				}
				cmd := exec.Command("bash", "-c", body)
				cmd.Env = append(os.Environ(), "TEST_DIR="+dir, "RESULT_PATH="+filepath.Join(dir, "result"),
					"SCENARIO="+tc.scenario, "REBUILD_BUILDER="+force)
				output, err := cmd.CombinedOutput()
				out := string(output)
				if (err != nil) != tc.fails {
					t.Fatalf("error=%v, want failure=%t\n%s", err, tc.fails, out)
				}
				leftovers, err := filepath.Glob(filepath.Join(dir, "builder-*-*.*"))
				if err != nil || len(leftovers) != 0 {
					t.Fatalf("temporary builder files leaked: %v, %v", leftovers, err)
				}
				if tc.fails {
					if !strings.Contains(out, "ERROR:") {
						t.Errorf("failure has no diagnostic\n%s", out)
					}
					if _, err := os.Stat(filepath.Join(dir, "result")); !os.IsNotExist(err) || strings.Contains(out, "SUCCESS") {
						t.Fatalf("published a builder result after failure\n%s", out)
					}
					return
				}
				pulls := tc.pulls
				if entrypoint == "prepare" && tc.pushes > 0 {
					pulls--
				}
				for event, want := range map[string]int{"PULL <": pulls, "PUSH <": tc.pushes, "AIB <": 1} {
					if got := strings.Count(out, event); got != want {
						t.Errorf("%s count=%d, want %d\n%s", event, got, want, out)
					}
				}
				if strings.Contains(out, "<--if-needed>") == tc.force {
					t.Errorf("incorrect freshness flag for force=%t\n%s", tc.force, out)
				}
				if entrypoint == "build" && !strings.Contains(out, "PROGRESS <Builder image ready> <3> <6>") {
					t.Errorf("incorrect prepared-builder progress\n%s", out)
				}
				if entrypoint == "prepare" && !strings.Contains(out, "PROGRESS <Builder ready> <2> <2>") {
					t.Errorf("incorrect prepare-task progress\n%s", out)
				}
				if !strings.Contains(out, "<--distro> <autosd> <--define> <repo=two words>") {
					t.Errorf("builder lost custom definitions\n%s", out)
				}
				digestChar := "a"
				if tc.pushes > 0 {
					digestChar = "b"
				}
				want := "registry.example:5000/test/aib-build@sha256:" + strings.Repeat(digestChar, 64)
				if !strings.Contains(out, "SUCCESS <"+want+">") {
					t.Errorf("result does not pin the consumed builder\n%s", out)
				}
			})
		}
	}
}

func TestBuilderDigestRef(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for ref, repo := range map[string]string{
		"builder": "builder", "builder:latest": "builder",
		"registry:5000/team/builder:latest": "registry:5000/team/builder",
		"registry/team/builder@" + digest:   "registry/team/builder",
	} {
		t.Run(ref, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", builderCacheScript+`builder_digest_ref "$REF" "$DIGEST"`)
			cmd.Env = append(os.Environ(), "REF="+ref, "DIGEST="+digest)
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != repo+"@"+digest {
				t.Fatalf("unexpected reference: %s, %v", out, err)
			}
		})
	}
}

func TestBuilderCacheEmbeddedInBothTasks(t *testing.T) {
	for name, script := range map[string]string{"build": BuildImageScript, "prepare": BuildBuilderScript} {
		if !strings.Contains(script, builderCacheScript) {
			t.Errorf("%s task does not embed builder cache helpers", name)
		}
	}
}

func TestProvidedBuilderSkipsFreshnessCheck(t *testing.T) {
	for _, entrypoint := range []string{"build", "prepare"} {
		t.Run(entrypoint, func(t *testing.T) {
			dir := t.TempDir()
			body := builderCacheHarness + builderCacheScript + `
PREPARES_BUILDER=false
BUILDER_IMAGE=registry.example/custom:chosen
REBUILD_BUILDER=true
aib() { echo 'unexpected rebuild' >&2; exit 1; }
skopeo() { echo 'unexpected cache access' >&2; exit 1; }
pull_registry_image() {
  [ "$1" = "$BUILDER_IMAGE" ] && [ "$2" = "containers-storage:$LOCAL_BUILDER_IMAGE" ]
}
`
			if entrypoint == "build" {
				body += shellFunctions(t, buildImageScript, "prepare_builder_if_needed() {", "\nprepare_builder_if_needed\n") + "\nprepare_builder_if_needed\n"
			} else {
				body += buildBuilderScript
			}
			result := filepath.Join(dir, "result")
			cmd := exec.Command("bash", "-c", body)
			cmd.Env = append(os.Environ(), "TEST_DIR="+dir, "RESULT_PATH="+result)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("provided builder failed: %v\n%s", err, output)
			}
			ref, err := os.ReadFile(result)
			if err != nil || string(ref) != "registry.example/custom:chosen" {
				t.Fatalf("provided builder changed: %s, %v", ref, err)
			}
		})
	}
}

const builderCacheHarness = `
set -e
PREPARES_BUILDER=true
PULLS_BUILDER=true
CLUSTER_REGISTRY_ROUTE=registry.example:5000
REGISTRY="$CLUSTER_REGISTRY_ROUTE"
REGISTRY_AUTH_FILE="$TEST_DIR/auth"
NAMESPACE=test
DISTRO=autosd
TARGET_ARCH=amd64
AIB_HASH=hash
AIB_IMAGE=quay.io/example/aib:1.3.5
LOCAL_BUILDER_IMAGE=localhost/builder
BUILDER_IMAGE=
BUILD_DIR="$TEST_DIR/build"
SECURE_BUILD=true
STEP_BUILD=4
PROGRESS_TOTAL=6
CUSTOM_DEFS_ARGS=(--define 'repo=two words')
SKOPEO_INSPECT_TLS_ARGS=(--tls-verify=false)
SKOPEO_COPY_TLS_ARGS=(--src-tls-verify=false --dest-tls-verify=false)
OLD_DIGEST=sha256:$(printf 'a%.0s' {1..64})
PUSH_DIGEST=sha256:$(printf 'b%.0s' {1..64})
create_service_account_auth() { printf auth > "$2"; }
mktemp() { command mktemp "$TEST_DIR/${1##*/}"; }
emit_progress() { printf 'PROGRESS <%s> <%s> <%s>\n' "$@"; }
setup_cluster_auth() { printf auth > "$REGISTRY_AUTH_FILE"; }
setup_container_config() { :; }
setup_var_tmp() { :; }
install_custom_ca_certs() { :; }
setup_osbuild() { :; }
load_custom_definitions() { CUSTOM_DEFS_ARGS=(--define 'repo=two words'); }
write_result() { printf '%s' "$2" > "$RESULT_PATH"; }
pull_registry_image() { echo 'Unexpected second pull' >&2; return 1; }
aib() {
  printf 'AIB'; printf ' <%s>' "$@"; printf '\n'
  [ "$SCENARIO" != aib-fail ] || return 1
  if [ "$SCENARIO" = unchanged ]; then
    printf local-old > "$TEST_DIR/local"
  else
    printf local-new > "$TEST_DIR/local"
  fi
}
skopeo() {
  local op="$1" digest_file= auth=false
  shift
  if [ "$op" = inspect ]; then
    case "${@: -1}" in
      docker:*)
        [ "$SCENARIO" != missing ] || return 1
        if [ "$SCENARIO" = cache-invalid ]; then echo invalid; else echo "$OLD_DIGEST"; fi
        ;;
      containers-storage:*)
        [ "$SCENARIO" != inspect-fail ] || return 1
        [ "$SCENARIO" != inspect-empty ] || return 0
        cat "$TEST_DIR/local"
        ;;
      *) return 1 ;;
    esac
    return
  fi
  [ "$op" = copy ] || return 1
  while [[ "$1" == --* ]]; do
    case "$1" in
      --digestfile) digest_file="$2"; shift ;;
      --authfile=*) auth=true; [ -f "${1#*=}" ] || return 1 ;;
      --src-tls-verify=false|--dest-tls-verify=false) ;;
      *) echo "Unexpected copy option: $1" >&2; return 1 ;;
    esac
    shift
  done
  [ "$auth" = true ] || return 1
  case "$1" in
    docker:*)
      printf 'PULL <%s>\n' "$1"
      # Reject mutable-tag pulls, including after a competing push moves the tag.
      case "$1" in
        *"@$OLD_DIGEST")
          [ "$SCENARIO" != pull-fail ] || return 1
          printf local-old > "$TEST_DIR/local"
          ;;
        *"@$PUSH_DIGEST") [ "$SCENARIO" != final-pull-fail ] || return 1 ;;
        *) echo 'Unexpected registry digest' >&2; return 1 ;;
      esac
      ;;
    containers-storage:*)
      printf 'PUSH <%s>\n' "$2"
      [ "$SCENARIO" != push-fail ] || return 1
      [ -n "$digest_file" ] || return 1
      case "$SCENARIO" in
        digest-missing) : ;;
        digest-invalid) echo invalid > "$digest_file" ;;
        *) echo "$PUSH_DIGEST" > "$digest_file" ;;
      esac
      ;;
    *) return 1 ;;
  esac
}
`
