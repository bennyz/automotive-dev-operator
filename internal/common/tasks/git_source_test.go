package tasks

import (
	"context"
	"encoding/pem"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitSourceTaskValidation(t *testing.T) {
	if err := GenerateGitSourceTask("test", nil).Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGitManifestStepCanRewritePVCCheckout(t *testing.T) {
	task := GenerateBuildAutomotiveImageTask("test", nil, "")
	for _, step := range task.Spec.Steps {
		if step.Name != "find-manifest-file" {
			continue
		}
		if step.SecurityContext == nil || step.SecurityContext.RunAsUser == nil || *step.SecurityContext.RunAsUser != 0 {
			t.Fatal("find-manifest-file must run as root to rewrite the Git checkout")
		}
		return
	}
	t.Fatal("find-manifest-file step not found")
}

func TestGitSourceStaging(t *testing.T) {
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq unavailable")
	}
	dir := t.TempDir()
	repository := filepath.Join(dir, "shared", ".caib-source", "repository")
	config := filepath.Join(dir, "config")
	work := filepath.Join(dir, "manifest-work")
	for _, p := range []string{filepath.Join(repository, "images"), filepath.Join(repository, "shared"), config, work} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for p, content := range map[string]string{
		filepath.Join(config, "git-manifest-path"):          "images/demo.aib.yml",
		filepath.Join(repository, "images", "demo.aib.yml"): "content:\n  add_files:\n    - path: /etc/config\n      source_path: ../shared/config\n",
		filepath.Join(repository, "images", "aib.lock"):     `{"version":1}`,
		filepath.Join(repository, "shared", "config"):       "from Git",
		filepath.Join(repository, ".settings"):              "hidden",
	} {
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("$(workspaces.manifest-config-workspace.path)", config, "$(workspaces.shared-workspace.path)", filepath.Join(dir, "shared"), "/manifest-work", work, "/tekton/results", filepath.Join(dir, "results")).Replace(FindManifestScript)
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("staging: %v %s", err, out)
	}
	manifest := filepath.Join(work, "source", "images", "demo.aib.yml")
	file, err := exec.Command("yq", "eval", ".content.add_files[0].source_path", manifest).Output()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(strings.TrimSpace(string(file)))
	if err != nil || string(data) != "from Git" {
		t.Fatalf("staged reference: %s %v", data, err)
	}
	for _, p := range []string{filepath.Join(work, "source", "images", "aib.lock"), filepath.Join(work, "source", ".settings")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloneGitSourceSnapshot(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--initial-branch=main")
	manifest := filepath.Join(repo, "demo.aib.yml")
	if err := os.WriteFile(manifest, []byte("name: first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "first")
	first := runGit("rev-parse", "HEAD")
	runGit("tag", "release")
	server := httptest.NewTLSServer(&cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	clone := func(ref string) {
		t.Helper()
		workspace := t.TempDir()
		cmd := exec.Command("sh", "-c", CloneGitSourceScript)
		cmd.Env = append(os.Environ(), "SOURCE_URL="+server.URL+"/repo/.git", "SOURCE_REVISION="+ref, "SOURCE_WORKSPACE="+workspace, "GIT_SSL_CAINFO="+caFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("checkout %q: %v %s", ref, err, out)
		}
		commit, err := os.ReadFile(filepath.Join(workspace, ".caib-source", "commit"))
		if err != nil || strings.TrimSpace(string(commit)) != first {
			t.Fatalf("commit=%s err=%v", commit, err)
		}
		data, err := os.ReadFile(filepath.Join(workspace, ".caib-source", "repository", "demo.aib.yml"))
		if err != nil || string(data) != "name: first\n" {
			t.Fatalf("manifest=%s err=%v", data, err)
		}
		if _, err := os.Stat(filepath.Join(workspace, ".caib-source", "repository", ".git")); !os.IsNotExist(err) {
			t.Fatal("Git metadata retained in build context")
		}
	}
	clone("")
	clone("main")
	clone("release")
	if err := os.WriteFile(manifest, []byte("name: second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "second")
	clone(first)
}

func TestPrepareGitSourceInputs(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	if err := exec.Command(python, "-c", "import yaml").Run(); err != nil {
		t.Skip("PyYAML unavailable")
	}
	for _, tt := range []struct {
		name, lock, source, payload, manifest string
		invalid, danglingSymlink              bool
	}{
		{name: "nested locked", lock: `{"version":1}`, source: "../shared/config"},
		{name: "unlocked", source: "../shared/config"},
		{name: "invalid lock", lock: `{"version":2}`, source: "../shared/config", invalid: true},
		{name: "non JSON lock", lock: `version: 1`, source: "../shared/config", invalid: true},
		{name: "escape", source: "../../../outside", invalid: true},
		{name: "missing file", source: "missing", invalid: true},
		{name: "absolute", source: "/etc/passwd", invalid: true},
		{name: "null add_files", manifest: "target: board\ncontent:\n  add_files: null\n"},
		{name: "LFS input", source: "../shared/config", payload: "version https://git-lfs.github.com/spec/v1\n", invalid: true},
		{name: "dangling symlink", source: "../shared/config", invalid: true, danglingSymlink: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, ".caib-source", "repository")
			for _, p := range []string{"images", "shared"} {
				if err := os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(p, content string) {
				t.Helper()
				if err := os.WriteFile(p, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manifest := tt.manifest
			if manifest == "" {
				manifest = "target: board\ncontent:\n  add_files:\n    - path: /etc/config\n      source_path: " + tt.source + "\n"
			}
			write(filepath.Join(root, "images", "demo.aib.yml"), manifest)
			payload := tt.payload
			if payload == "" {
				payload = "config"
			}
			write(filepath.Join(root, "shared", "config"), payload)
			if tt.danglingSymlink {
				if err := os.Symlink("missing", filepath.Join(root, "shared", "broken")); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(dir, ".caib-source", "commit"), strings.Repeat("a", 40))
			if tt.lock != "" {
				write(filepath.Join(root, "images", "aib.lock"), tt.lock)
			}
			results := filepath.Join(dir, "results")
			if err := os.Mkdir(results, 0700); err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(PrepareGitSourceScript, `Path("/tekton/results")`, `Path(os.environ["TEST_RESULTS"])`, 1)
			cmd := exec.Command(python, "-c", body)
			cmd.Env = append(os.Environ(), "SOURCE_WORKSPACE="+dir, "SOURCE_MANIFEST=images/demo.aib.yml", "TEST_RESULTS="+results)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tt.invalid {
				t.Fatalf("error=%v output=%s", err, out)
			}
			if !tt.invalid {
				target, err := os.ReadFile(filepath.Join(results, "target"))
				if err != nil || string(target) != "board" {
					t.Fatalf("target=%q error=%v", target, err)
				}
			}
		})
	}
}
