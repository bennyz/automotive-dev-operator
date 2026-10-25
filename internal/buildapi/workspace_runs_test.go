//go:build linux

package buildapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	buildapiclient "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi/client"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func runTestServer(t *testing.T, root, owner string, stopped bool) (*APIServer, *gin.Engine, *atomic.Int32) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("NAMESPACE", "default")
	scheme := runtime.NewScheme()
	if err := automotivev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ws := &automotivev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "default"},
		Spec:       automotivev1alpha1.WorkspaceSpec{Owner: owner, Stopped: stopped},
		Status:     automotivev1alpha1.WorkspaceStatus{Phase: phaseRunning, PodName: "workspace-dev"},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).WithStatusSubresource(ws).Build()
	a := newTestServer(t, func(deps *apiDependencies) {
		deps.getClientFromRequest = func(*gin.Context) (client.Client, error) { return kube, nil }
		deps.getRESTConfigFromRequest = func(*gin.Context) (*rest.Config, error) { return &rest.Config{}, nil }
	})
	calls := &atomic.Int32{}
	a.exec.newExecutor = func(_ *rest.Config, namespace, pod, container string, argv []string) (remotecommand.Executor, error) {
		calls.Add(1)
		if namespace != "default" || pod != "workspace-dev" || container != workspaceContainerName {
			t.Errorf("wrong exec target: %s/%s/%s", namespace, pod, container)
		}
		argv[3], argv[4] = filepath.Join(root, ".runs"), root
		return &fakeRemoteExecutor{streamWithContextFn: func(ctx context.Context, opts remotecommand.StreamOptions) error {
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Stdout, cmd.Stderr = opts.Stdout, opts.Stderr
			return cmd.Run()
		}}, nil
	}
	// Authentication itself is covered through registerWorkspaceRoutes in the
	// Workspace API suite. Keep a fixed identity here to exercise owner checks.
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("requester", "alice"); c.Next() })
	router.POST("/v1/workspaces/:name/runs", a.wrapNamedHandler("create run", a.createWorkspaceRun))
	router.GET("/v1/workspaces/:name/runs/:run", a.wrapNamedHandler("get run", a.getWorkspaceRun))
	router.GET("/v1/workspaces/:name/runs/:run/logs", a.wrapNamedHandler("run logs", a.workspaceRunLogs))
	router.POST("/v1/workspaces/:name/runs/:run/cancel", a.wrapNamedHandler("cancel run", a.cancelWorkspaceRun))
	return a, router, calls
}

func TestWorkspaceRunHTTPDisconnectAndAPIRecreation(t *testing.T) {
	root := t.TempDir()
	_, router, _ := runTestServer(t, root, "alice", false)
	server := httptest.NewServer(router)
	defer server.Close()
	api, err := buildapiclient.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := api.CreateWorkspaceRun(ctx, "dev", buildcontract.WorkspaceRunRequest{
		ID: "reconnect", Command: `printf first; while [ ! -f release ]; do sleep 0.05; done; printf '\000last'; exit 17`,
	})
	if err != nil || run.ID != "reconnect" {
		t.Fatalf("create: %+v, %v", run, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = api.CancelWorkspaceRun(cleanupCtx, "dev", "reconnect")
	}()
	streamCtx, disconnect := context.WithCancel(ctx)
	stream, err := api.WorkspaceRunLogs(streamCtx, "dev", run.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(stream, first); err != nil || string(first) != "first" {
		t.Fatalf("first output: %q, %v", first, err)
	}
	disconnect()
	_ = stream.Close()
	server.Close()
	// A new API instance has no in-memory knowledge of the run.
	_, router, _ = runTestServer(t, root, "alice", false)
	server2 := httptest.NewServer(router)
	t.Cleanup(server2.Close)
	api, err = buildapiclient.New(server2.URL)
	if err != nil {
		t.Fatal(err)
	}
	run, err = api.GetWorkspaceRun(ctx, "dev", run.ID)
	if err != nil || run.Status != "Running" {
		t.Fatalf("run lost after disconnect: %+v, %v", run, err)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stream, err = api.WorkspaceRunLogs(ctx, "dev", run.ID, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil || !bytes.Equal(remaining, []byte{0, 'l', 'a', 's', 't'}) {
		t.Fatalf("reattach output: %q, %v", remaining, err)
	}
	run, err = api.GetWorkspaceRun(ctx, "dev", run.ID)
	if err != nil || run.ExitCode == nil || *run.ExitCode != 17 || run.Status != "Failed" {
		t.Fatalf("retained result: %+v, %v", run, err)
	}
}

func TestWorkspaceRunAuthorizationAndValidation(t *testing.T) {
	requests := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/workspaces/dev/runs", `{"id":"run","command":"echo hello"}`},
		{http.MethodGet, "/v1/workspaces/dev/runs/run", ""},
		{http.MethodGet, "/v1/workspaces/dev/runs/run/logs?follow&offset=0", ""},
		{http.MethodPost, "/v1/workspaces/dev/runs/run/cancel", ""},
	}
	for _, tc := range []struct {
		name, owner string
		stopped     bool
		status      int
	}{{"other owner", "bob", false, 404}, {"stopped", "alice", true, 409}} {
		t.Run(tc.name, func(t *testing.T) {
			_, router, calls := runTestServer(t, t.TempDir(), tc.owner, tc.stopped)
			for _, request := range requests {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
				if rec.Code != tc.status {
					t.Errorf("%s %s: %d %s", request.method, request.path, rec.Code, rec.Body.String())
				}
			}
			if calls.Load() != 0 {
				t.Fatal("unauthorized or stopped workspace reached pod exec")
			}
		})
	}
	_, router, calls := runTestServer(t, t.TempDir(), "alice", false)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/workspaces/dev/runs", `{"command":" "}`},
		{http.MethodPost, "/v1/workspaces/dev/runs", `{"id":"../escape","command":"true"}`},
		{http.MethodGet, "/v1/workspaces/dev/runs/run/logs?offset=-1", ""},
		{http.MethodGet, "/v1/workspaces/dev/runs/run/logs?offset=no", ""},
		{http.MethodGet, "/v1/workspaces/dev/runs/run/logs?follow=nope", ""},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		if rec.Code != 400 {
			t.Errorf("invalid request: %d %s", rec.Code, rec.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached pod exec")
	}
}

func TestWorkspaceRunLaunchIgnoresRequestCancellation(t *testing.T) {
	a, router, _ := runTestServer(t, t.TempDir(), "alice", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newExecutor := a.exec.newExecutor
	a.exec.newExecutor = func(cfg *rest.Config, ns, pod, container string, argv []string) (remotecommand.Executor, error) {
		cancel()
		return newExecutor(cfg, ns, pod, container, argv)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/dev/runs", strings.NewReader(`{"id":"detached","command":"true"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("launch tied to HTTP context: %d %s", rec.Code, rec.Body.String())
	}
	var run buildcontract.WorkspaceRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.ID != "detached" {
		t.Fatalf("launch result: %+v %v", run, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !run.Terminal() {
		if time.Now().After(deadline) {
			t.Fatal("detached run did not finish")
		}
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workspaces/dev/runs/detached", nil))
		if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkspaceRunRequiresActivityReservation(t *testing.T) {
	a, router, calls := runTestServer(t, t.TempDir(), "alice", false)
	getClient := a.deps.getClientFromRequest
	requests := 0
	a.deps.getClientFromRequest = func(c *gin.Context) (client.Client, error) {
		requests++
		if requests == 2 {
			return nil, errors.New("API unavailable while recording activity")
		}
		return getClient(c)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/workspaces/dev/runs", strings.NewReader(`{"id":"run","command":"true"}`)))
	if rec.Code != http.StatusServiceUnavailable || calls.Load() != 0 {
		t.Fatalf("launched without recording activity: HTTP %d, exec calls %d", rec.Code, calls.Load())
	}
}

func TestWorkspaceRunLogSnapshotAndOffsets(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".runs", "large")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{'a', 0, 255, '\n'}, 40000)
	for name, content := range map[string][]byte{
		"state.json": []byte(`{"id":"large","status":"Succeeded","exitCode":0}`),
		"output.log": data,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, router, _ := runTestServer(t, root, "alice", false)
	for _, suffix := range []string{"?offset=3", "?follow&offset=3"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workspaces/dev/runs/large/logs"+suffix, nil))
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data[3:]) || rec.Header().Get("X-Log-Offset") != "3" {
			t.Fatalf("log bytes or offset corrupted: %d, %d bytes", rec.Code, rec.Body.Len())
		}
	}
	for path, status := range map[string]int{
		"large/logs?offset=160001": 416,
		"missing/logs":             404,
		"missing":                  404,
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workspaces/dev/runs/"+path, nil))
		if rec.Code != status {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
