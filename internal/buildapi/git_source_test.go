package buildapi

import (
	"context"
	"testing"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/labels"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGitCredentialsOwnership(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-auth", Namespace: "test", Annotations: map[string]string{labels.RequestedBy: "alice", api.GitCredentialsHostAnnotation: "https://git.example.com"}}, Type: corev1.SecretTypeBasicAuth, Data: map[string][]byte{corev1.BasicAuthUsernameKey: []byte("alice"), corev1.BasicAuthPasswordKey: []byte("token")}}
	c := newFakeClient(secret)
	source := &api.GitSource{URL: "https://git.example.com/os.git", CredentialsSecretRef: secret.Name}
	for _, user := range []string{"alice", "bob", ""} {
		err := validateGitCredentials(context.Background(), c, "test", user, source)
		if (err == nil) != (user == "alice") {
			t.Fatalf("user=%q error=%v", user, err)
		}
	}
}

func TestGitSourceRequest(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mutate  func(*BuildRequest)
		invalid bool
	}{
		{"git", func(r *BuildRequest) {}, false},
		{"inline manifest", func(r *BuildRequest) { r.Manifest = "name: demo" }, true},
		{"local lockfile", func(r *BuildRequest) { r.Lockfile = `{"version":1}` }, true},
		{"upload", func(r *BuildRequest) { r.HasLocalFiles = true }, true},
		{"workspace", func(r *BuildRequest) { r.Workspace = "dev" }, true},
		{"disk", func(r *BuildRequest) { r.Mode = ModeDisk }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := BuildRequest{Name: "git-build", GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "images/demo.aib.yml"}}
			tt.mutate(&r)
			if err := validateBuildRequest(&r); (err != nil) != tt.invalid {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGitSourceDefaultsDeferred(t *testing.T) {
	r := BuildRequest{GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "demo.aib.yml"}}
	if err := applyBuildDefaults(&r); err != nil {
		t.Fatal(err)
	}
	if r.Target != "" || r.Architecture != "" || r.ExportFormat != "" {
		t.Fatalf("premature defaults: %+v", r)
	}
	r.Target = " "
	if err := applyBuildDefaults(&r); err == nil {
		t.Fatal("explicit whitespace target accepted")
	}
}
