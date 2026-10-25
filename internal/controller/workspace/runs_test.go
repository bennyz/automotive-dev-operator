package workspace

import (
	"context"
	"testing"
	"time"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestActiveRunPreventsAutoPause(t *testing.T) {
	ws, _, _ := runningWorkspace("run-ws", "developer-project")
	ws.Spec.AutoPauseTimeoutMinutes = new(int32(1))
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	ws.Status.LastActivityTime = &old
	r, kube := newTestReconciler(ws)
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(ws), ws); err != nil {
		t.Fatal(err)
	}
	r.execActivity = func(_ context.Context, target *automotivev1alpha1.Workspace, argv []string) (string, error) {
		if target.Namespace != "developer-project" {
			t.Fatalf("wrong activity target namespace: %s", target.Namespace)
		}
		if argv[0] != "/bin/sh" || argv[len(argv)-1] != "active" {
			t.Fatal("activity check did not use the combined runner")
		}
		return `{"active":true}`, nil
	}
	result, err := r.checkAutoPause(context.Background(), ws, logr.Discard())
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("check: %+v, %v", result, err)
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(ws), ws); err != nil {
		t.Fatal(err)
	}
	if ws.Spec.Stopped || !ws.Status.LastActivityTime.After(old.Time) {
		t.Fatalf("live run was considered idle: %+v", ws)
	}
}

func TestRunActivityCheckFailsClosed(t *testing.T) {
	for _, response := range []string{"invalid JSON", `{"error":"volume unavailable"}`} {
		ws, _, _ := runningWorkspace("run-ws", "default")
		ws.Spec.AutoPauseTimeoutMinutes = new(int32(1))
		old := metav1.NewTime(time.Now().Add(-time.Hour))
		ws.Status.LastActivityTime = &old
		r, _ := newTestReconciler(ws)
		r.execActivity = func(context.Context, *automotivev1alpha1.Workspace, []string) (string, error) { return response, nil }
		result, err := r.checkAutoPause(context.Background(), ws, logr.Discard())
		if err != nil || result.RequeueAfter == 0 || ws.Spec.Stopped {
			t.Fatalf("failed activity check paused workspace: %+v, %v", result, err)
		}
	}
}

func TestAutoPauseConflictsWithNewRunActivity(t *testing.T) {
	ws, _, _ := runningWorkspace("run-ws", "default")
	ws.Spec.AutoPauseTimeoutMinutes = new(int32(1))
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	ws.Status.LastActivityTime = &old
	r, kube := newTestReconciler(ws)
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(ws), ws); err != nil {
		t.Fatal(err)
	}
	r.execActivity = func(_ context.Context, _ *automotivev1alpha1.Workspace, argv []string) (string, error) {
		// Simulate POST /runs updating activity after the controller read idle.
		current := &automotivev1alpha1.Workspace{}
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(ws), current); err != nil {
			return "", err
		}
		patch := client.MergeFrom(current.DeepCopy())
		now := metav1.Now()
		current.Status.LastActivityTime = &now
		if err := kube.Status().Patch(context.Background(), current, patch); err != nil {
			return "", err
		}
		return `{"active":false}`, nil
	}
	_, err := r.checkAutoPause(context.Background(), ws, logr.Discard())
	if !k8serrors.IsConflict(err) {
		t.Fatalf("expected optimistic-lock conflict, got %v", err)
	}
	current := &automotivev1alpha1.Workspace{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(ws), current); err != nil {
		t.Fatal(err)
	}
	if current.Spec.Stopped {
		t.Fatal("stale idle decision paused a newly active workspace")
	}
}
