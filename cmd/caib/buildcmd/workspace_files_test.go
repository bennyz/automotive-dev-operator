package buildcmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestStripExecStream(t *testing.T) {
	got, err := stripExecStream([]byte(execStreamPreamble + "posix-hydrate-ok\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "posix-hydrate-ok\n" {
		t.Fatalf("got %q", got)
	}

	_, err = stripExecStream([]byte(execStreamPreamble + "\n[exec failed: command terminated with exit code 1]\n"))
	if err == nil {
		t.Fatal("expected exec failure")
	}
}

func TestStripExecStream_BinaryContainsSentinel(t *testing.T) {
	payload := []byte("\x00\x7f\x01\n[exec failed: fake]\x02\x03")
	raw := append([]byte(execStreamPreamble), payload...)
	got, err := stripExecStream(raw)
	if err != nil {
		t.Fatalf("mid-file sentinel must not error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("binary payload mutated: got %q", got)
	}
}

func TestMaterializeWorkspaceFiles_RepoOnlyLeavesFileURLs(t *testing.T) {
	h := &Handler{}
	manifest := "content:\n  repos:\n    - id: local\n      baseurl: file:///workspace/src/bin\n"
	got, refs, cleanup, err := h.materializeWorkspaceFiles(context.Background(), nil, "posix-bake", manifest)
	if err != nil {
		t.Fatalf("repo-only --workspace manifest should not error: %v", err)
	}
	if cleanup != nil {
		t.Fatal("repo-only manifest should not stage files")
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %d, want 0 (server rewrites file:// repos)", len(refs))
	}
	if !strings.Contains(got, "file:///workspace/src/bin") {
		t.Fatalf("file:// repo should be left for the server, got:\n%s", got)
	}
}

func TestMaterializeWorkspaceFiles_GlobLeavesManifestForOperator(t *testing.T) {
	h := &Handler{}
	manifest := "content:\n  add_files:\n    - dest: /etc/\n      source_glob: /workspace/src/etc/**/*.conf\n"
	got, refs, cleanup, err := h.materializeWorkspaceFiles(context.Background(), nil, "posix-bake", manifest)
	if err != nil {
		t.Fatalf("workspace source_glob should defer to operator hydrate: %v", err)
	}
	if cleanup != nil || len(refs) != 0 {
		t.Fatalf("glob must not be staged by caib, refs=%d cleanup=%v", len(refs), cleanup != nil)
	}
	if !strings.Contains(got, "/workspace/src/etc/**/*.conf") {
		t.Fatalf("original glob path must remain for server hydrate, got:\n%s", got)
	}
}
