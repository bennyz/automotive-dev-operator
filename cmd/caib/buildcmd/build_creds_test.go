package buildcmd

import (
	"os"
	"path/filepath"
	"testing"

	buildapitypes "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi"
)

func TestApplyRegistryCredentials_InternalRegistryWithEnvCreds(t *testing.T) {
	t.Setenv("REGISTRY_URL", "quay.io")
	t.Setenv("REGISTRY_USERNAME", "myuser")
	t.Setenv("REGISTRY_PASSWORD", "mypass")

	opts := newTestDiskOpts()
	*opts.UseInternalRegistry = true
	h := NewHandler(opts)

	req := &buildapitypes.BuildRequest{}
	if err := h.applyRegistryCredentialsToRequest(req); err != nil {
		t.Fatalf("applyRegistryCredentialsToRequest() error = %v", err)
	}

	if !req.UseInternalRegistry {
		t.Fatal("expected UseInternalRegistry = true")
	}
	if req.RegistryCredentials == nil {
		t.Fatal("expected RegistryCredentials to be set when env vars are provided")
	}
	if req.RegistryCredentials.AuthType != "username-password" {
		t.Fatalf("authType = %q, want username-password", req.RegistryCredentials.AuthType)
	}
	if req.RegistryCredentials.RegistryURL != "quay.io" {
		t.Fatalf("registryURL = %q, want quay.io", req.RegistryCredentials.RegistryURL)
	}
}

func TestApplyRegistryCredentials_InternalRegistryWithAuthFile(t *testing.T) {
	t.Setenv("REGISTRY_URL", "")
	t.Setenv("REGISTRY_USERNAME", "")
	t.Setenv("REGISTRY_PASSWORD", "")
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "auth.json")
	writeTestAuthFile(t, authFile)

	opts := newTestDiskOpts()
	*opts.UseInternalRegistry = true
	*opts.RegistryAuthFile = authFile
	h := NewHandler(opts)

	req := &buildapitypes.BuildRequest{}
	if err := h.applyRegistryCredentialsToRequest(req); err != nil {
		t.Fatalf("applyRegistryCredentialsToRequest() error = %v", err)
	}

	if !req.UseInternalRegistry {
		t.Fatal("expected UseInternalRegistry = true")
	}
	if req.RegistryCredentials == nil {
		t.Fatal("expected RegistryCredentials to be set when --registry-auth-file is provided")
	}
	if req.RegistryCredentials.AuthType != "docker-config" {
		t.Fatalf("authType = %q, want docker-config", req.RegistryCredentials.AuthType)
	}
}

func TestApplyRegistryCredentials_InternalRegistryNoCredsReturnsNil(t *testing.T) {
	t.Setenv("REGISTRY_URL", "")
	t.Setenv("REGISTRY_USERNAME", "")
	t.Setenv("REGISTRY_PASSWORD", "")

	opts := newTestDiskOpts()
	*opts.UseInternalRegistry = true
	h := NewHandler(opts)

	req := &buildapitypes.BuildRequest{}
	if err := h.applyRegistryCredentialsToRequest(req); err != nil {
		t.Fatalf("applyRegistryCredentialsToRequest() error = %v", err)
	}

	if !req.UseInternalRegistry {
		t.Fatal("expected UseInternalRegistry = true")
	}
	if req.RegistryCredentials != nil {
		t.Fatalf("expected nil RegistryCredentials when no creds provided, got %+v", req.RegistryCredentials)
	}
}

func writeTestAuthFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}
	content := []byte(`{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}
}
