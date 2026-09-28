package imagebuild

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/labels"
	controllerutils "github.com/centos-automotive-suite/automotive-dev-operator/internal/controller/controllerutils"
	tekton "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestGitSourceSecretReadErrorIsRetryable(t *testing.T) {
	ctx := context.Background()
	ib := api.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns"},
		Spec: api.ImageBuildSpec{AIB: &api.AIBSpec{GitSource: &api.GitSource{
			URL: "https://git.example.com/os.git", ManifestPath: "demo.aib.yml", CredentialsSecretRef: "git-auth",
		}}},
		Status: api.ImageBuildStatus{Phase: api.ImageBuildPhasePending},
	}
	r := newExpiryReconciler(ib)
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return fmt.Errorf("temporary API failure")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if _, err := r.startGitSource(ctx, &ib, "git-build-source", ""); err == nil {
		t.Fatal("transient Secret read error was swallowed")
	}
	stored := &api.ImageBuild{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != api.ImageBuildPhasePending {
		t.Fatalf("transient read marked build %q", stored.Status.Phase)
	}
}

func TestGitSourcePreparation(t *testing.T) { //nolint:gocyclo // covers both discovery and checkout lifecycle transitions
	ctx := context.Background()
	ib := api.ImageBuild{ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns", UID: "build-uid"}, Spec: api.ImageBuildSpec{AIB: &api.AIBSpec{Distro: "autosd", Mode: "bootc", Image: "quay.io/example/arm64-only:latest", GitSource: &api.GitSource{URL: "https://git.example.com/os.git", Revision: "main", ManifestPath: "images/demo.aib.yml", LockfilePath: "locks/release.json"}, AIBExtraArgs: []string{"--verbose"}}}}
	r := newExpiryReconciler(ib)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "aib-target-defaults", Namespace: controllerutils.OperatorNamespace()}, Data: map[string]string{"target-defaults.yaml": "targets:\n  board:\n    architecture: amd64\n    defaultFormat: raw\n    extraArgs: [--board-default]\n"}}
	if err := r.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	result, err := r.handleInitialState(ctx, &ib)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("prepare: %+v %v", result, err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Status.Phase != "Pending" || ib.Status.SourceTaskRunName == "" || ib.Status.PVCName != "" {
		t.Fatalf("status: %+v", ib.Status)
	}
	tr := &tekton.TaskRun{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: ib.Status.SourceTaskRunName}, tr); err != nil {
		t.Fatal(err)
	}
	if tr.Spec.Workspaces[0].EmptyDir == nil {
		t.Fatal("discovery must not bind the build PVC")
	}
	if tr.Spec.PodTemplate == nil {
		t.Fatal("discovery TaskRun is missing its scheduling template")
	}
	for _, param := range tr.Spec.Params {
		if param.Name == "aib-image" {
			t.Fatal("discovery must not use the build's architecture-specific AIB image")
		}
	}
	tr.Status.Conditions = []apis.Condition{{Type: "Succeeded", Status: corev1.ConditionTrue}}
	tr.Status.Results = []tekton.TaskRunResult{{Name: "commit", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: strings.Repeat("a", 40)}}, {Name: "target", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: "board"}}}
	if err := r.Update(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareGitSource(ctx, &ib); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Status.DiscoveredCommit != strings.Repeat("a", 40) || ib.Status.DiscoveredTarget != "board" || ib.Status.PVCName != "" {
		t.Fatalf("discovery result was not saved before PVC creation: %+v", ib.Status)
	}
	if err := r.Delete(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareGitSource(ctx, &ib); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Spec.GetTarget() != "board" || ib.Spec.Architecture != "amd64" || ib.Status.PVCName != "" {
		t.Fatalf("discovery defaults after TaskRun deletion: %+v %+v", ib.Spec, ib.Status)
	}
	if _, err := r.prepareGitSource(ctx, &ib); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Status.PVCName == "" || ib.Status.SourceTaskRunName == tr.Name {
		t.Fatalf("checkout status: %+v", ib.Status)
	}
	tr = &tekton.TaskRun{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: ib.Status.SourceTaskRunName}, tr); err != nil {
		t.Fatal(err)
	}
	if tr.Spec.Workspaces[0].PersistentVolumeClaim.ClaimName != ib.Status.PVCName {
		t.Fatal("source PVC mismatch")
	}
	if tr.Spec.PodTemplate == nil || tr.Spec.PodTemplate.Affinity == nil {
		t.Fatal("checkout must have architecture affinity")
	}
	foundCommit, foundAIBImage, foundLockfile := false, false, false
	for _, param := range tr.Spec.Params {
		if param.Name == "commit" && param.Value.StringVal == strings.Repeat("a", 40) {
			foundCommit = true
		}
		if param.Name == "aib-image" && param.Value.StringVal == "quay.io/example/arm64-only:latest" {
			foundAIBImage = true
		}
		if param.Name == "lockfile" && param.Value.StringVal == "locks/release.json" {
			foundLockfile = true
		}
	}
	if !foundCommit || !foundAIBImage || !foundLockfile {
		t.Fatal("checkout must use the discovered commit, lockfile, and custom AIB image")
	}
	tr.Status.Conditions = []apis.Condition{{Type: "Succeeded", Status: corev1.ConditionTrue}}
	tr.Status.Results = []tekton.TaskRunResult{{Name: "commit", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: strings.Repeat("a", 40)}}, {Name: "target", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: "board"}}}
	if err := r.Update(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.prepareGitSource(ctx, &ib); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Status.Phase != "Building" || ib.Status.SourceCommit != strings.Repeat("a", 40) {
		t.Fatalf("status: %+v", ib.Status)
	}
	if ib.Spec.GetTarget() != "board" || ib.Spec.Architecture != "amd64" || ib.Spec.Export != nil || r.resolveExportFormat(ctx, &ib) != "raw" {
		t.Fatalf("defaults: %+v", ib.Spec)
	}
	if err := r.applyGitTargetDefaults(ctx, &ib, "different"); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ib.Spec.GetAIBExtraArgs(), ",") != "--verbose" {
		t.Fatalf("duplicated defaults: %v", ib.Spec.GetAIBExtraArgs())
	}
	cmName, err := r.createOrUpdateManifestConfigMap(ctx, &ib)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ib.Namespace}, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["git-manifest-path"] != "images/demo.aib.yml" {
		t.Fatalf("manifest config: %v", cm.Data)
	}
	if _, exists := cm.Data["aib.lock"]; exists {
		t.Fatal("inline lockfile created for Git source")
	}
}

func TestGitSourceKnownArchitectureSkipsDiscovery(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, target, arch string
	}{
		{name: "explicit architecture", arch: "amd64"},
		{name: "explicit target", target: "board"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ib := api.ImageBuild{ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns", UID: "build-uid"}, Spec: api.ImageBuildSpec{Architecture: tt.arch, AIB: &api.AIBSpec{Target: tt.target, GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "demo.aib.yml"}}}}
			r := newExpiryReconciler(ib)
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "aib-target-defaults", Namespace: controllerutils.OperatorNamespace()}, Data: map[string]string{"target-defaults.yaml": "targets:\n  board:\n    architecture: amd64\n"}}
			if err := r.Create(ctx, cm); err != nil {
				t.Fatal(err)
			}
			if _, err := r.prepareGitSource(ctx, &ib); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
				t.Fatal(err)
			}
			if tt.target != "" {
				if ib.Spec.Architecture != "amd64" || ib.Status.PVCName != "" {
					t.Fatalf("target defaults were not applied before PVC creation: %+v", ib)
				}
				if _, err := r.prepareGitSource(ctx, &ib); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
					t.Fatal(err)
				}
			}
			if ib.Status.PVCName == "" {
				t.Fatal("known architecture did not start checkout")
			}
			tr := &tekton.TaskRun{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: safeDerivedName(ib.Name, "-source-discovery")}, tr); err == nil {
				t.Fatal("unnecessary discovery TaskRun was created")
			}
		})
	}
}

func TestGitArchitectureSelection(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, target, explicit, fallback, defaultArch, wantArch, wantSource string
	}{
		{name: "explicit", target: "board", explicit: "arm64", fallback: "amd64", defaultArch: "amd64", wantArch: "arm64", wantSource: "explicit"},
		{name: "target default", target: "board", fallback: "arm64", defaultArch: "amd64", wantArch: "amd64", wantSource: "target-default"},
		{name: "client fallback", target: "board", fallback: "amd64", wantArch: "amd64", wantSource: "client-fallback"},
		{name: "manifest target fallback", fallback: "x86_64", wantArch: "amd64", wantSource: "client-fallback"},
		{name: "operator fallback", target: "board", wantArch: "arm64", wantSource: "operator-fallback"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ib := api.ImageBuild{
				ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns", Annotations: map[string]string{labels.DefaultArchitecture: tt.fallback}},
				Spec:       api.ImageBuildSpec{Architecture: tt.explicit, AIB: &api.AIBSpec{Target: tt.target, GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "demo.aib.yml"}}},
			}
			r := newExpiryReconciler(ib)
			if tt.defaultArch != "" {
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "aib-target-defaults", Namespace: controllerutils.OperatorNamespace()}, Data: map[string]string{"target-defaults.yaml": "targets:\n  board:\n    architecture: " + tt.defaultArch + "\n"}}
				if err := r.Create(ctx, cm); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.applyGitTargetDefaults(ctx, &ib, "board"); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
				t.Fatal(err)
			}
			if ib.Spec.Architecture != tt.wantArch || ib.Annotations[labels.ArchitectureSource] != tt.wantSource || ib.Spec.GetTarget() != "board" {
				t.Fatalf("selected architecture: spec=%+v annotations=%v", ib.Spec, ib.Annotations)
			}
		})
	}
}

func TestGitArchitectureFallbackRejectsInvalidDirectCR(t *testing.T) {
	ctx := context.Background()
	ib := api.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns", Annotations: map[string]string{labels.DefaultArchitecture: "ppc64le"}},
		Spec:       api.ImageBuildSpec{AIB: &api.AIBSpec{Target: "board", GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "demo.aib.yml"}}},
	}
	r := newExpiryReconciler(ib)
	if _, err := r.prepareGitSource(ctx, &ib); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Status.Phase != "Failed" || ib.Status.PVCName != "" {
		t.Fatalf("invalid fallback did not fail before scheduling: %+v", ib.Status)
	}
}

func TestGitSourceMissingDiscoveryRestartsWithoutUnpinnedCheckout(t *testing.T) {
	ctx := context.Background()
	ib := api.ImageBuild{ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns", UID: "build-uid"}, Spec: api.ImageBuildSpec{Architecture: "arm64", AIB: &api.AIBSpec{GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "demo.aib.yml"}}}}
	ib.Status.SourceTaskRunName = safeDerivedName(ib.Name, "-source-discovery")
	r := newExpiryReconciler(ib)
	if _, err := r.prepareGitSource(ctx, &ib); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(&ib), &ib); err != nil {
		t.Fatal(err)
	}
	if ib.Status.Phase != api.ImageBuildPhasePending || ib.Status.PVCName != "" {
		t.Fatalf("missing discovery resumed an unpinned checkout: %+v", ib.Status)
	}
	tr := &tekton.TaskRun{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: ib.Status.SourceTaskRunName}, tr); err != nil {
		t.Fatal(err)
	}
	if tr.Spec.Workspaces[0].EmptyDir == nil {
		t.Fatal("missing discovery must restart without binding the build PVC")
	}
}
