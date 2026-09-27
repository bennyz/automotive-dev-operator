package imagebuild

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
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
	if _, err := r.startGitSource(ctx, &ib, "git-build-source"); err == nil {
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

func TestGitSourcePreparation(t *testing.T) {
	ctx := context.Background()
	ib := api.ImageBuild{ObjectMeta: metav1.ObjectMeta{Name: "git-build", Namespace: "test-ns", UID: "build-uid"}, Spec: api.ImageBuildSpec{AIB: &api.AIBSpec{Distro: "autosd", Mode: "bootc", GitSource: &api.GitSource{URL: "https://git.example.com/os.git", Revision: "main", ManifestPath: "images/demo.aib.yml"}, AIBExtraArgs: []string{"--verbose"}}}}
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
	if ib.Status.Phase != "Pending" || ib.Status.SourceTaskRunName == "" || ib.Status.PVCName == "" {
		t.Fatalf("status: %+v", ib.Status)
	}
	tr := &tekton.TaskRun{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: ib.Status.SourceTaskRunName}, tr); err != nil {
		t.Fatal(err)
	}
	if tr.Spec.Workspaces[0].PersistentVolumeClaim.ClaimName != ib.Status.PVCName {
		t.Fatal("source PVC mismatch")
	}
	if tr.Spec.PodTemplate == nil {
		t.Fatal("source TaskRun is missing its scheduling template")
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
