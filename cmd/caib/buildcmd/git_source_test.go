package buildcmd

import (
	"os"
	"path/filepath"
	"testing"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	buildapi "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi"
	"github.com/spf13/cobra"
)

func TestReadGitBuildSource(t *testing.T) {
	url := "https://git.example.com/os.git"
	ref := "release"
	h := NewHandler(Options{GitURL: &url, GitRef: &ref})
	data, source, err := h.readBuildSource("does-not-exist/demo.aib.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 || source.Revision != ref || source.ManifestPath != "does-not-exist/demo.aib.yml" {
		t.Fatalf("unexpected source: %+v", source)
	}
	lock := "local.lock"
	h.opts.Lockfile = &lock
	if _, _, err := h.readBuildSource("demo.aib.yml"); err == nil {
		t.Fatal("accepted local lockfile override")
	}
}

func TestReadLocalBuildSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "demo.aib.yml")
	if err := os.WriteFile(p, []byte("name: demo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, source, err := NewHandler(Options{}).readBuildSource(p)
	if err != nil || source != nil || string(data) != "name: demo\n" {
		t.Fatalf("local source changed: %q, %+v, %v", data, source, err)
	}
}

func TestDeferGitDefaults(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("target", "qemu", "")
	cmd.Flags().String("arch", "arm64", "")
	cmd.Flags().String("format", "qcow2", "")
	if err := cmd.Flags().Set("arch", "amd64"); err != nil {
		t.Fatal(err)
	}
	req := buildapi.BuildRequest{GitSource: &api.GitSource{}, Target: "qemu", Architecture: "amd64", ExportFormat: "qcow2"}
	deferGitDefaults(cmd, &req)
	if req.Target != "" || req.Architecture != "amd64" || req.ExportFormat != "" {
		t.Fatalf("defaults: %+v", req)
	}
}
