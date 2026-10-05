package buildcmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/centos-automotive-suite/automotive-dev-operator/cmd/caib/clilog"
	buildapiclient "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi/client"
	buildcontract "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
)

func newTestOpts() Options {
	opts := newTestDiskOpts()
	var (
		workspace        string
		extraRepos       []string
		flashCmd         string
		exporterSelector string
	)
	opts.Build.Workspace = workspace
	opts.Build.ExtraRepos = extraRepos
	opts.Flash.Cmd = flashCmd
	opts.Flash.ExporterSelector = exporterSelector
	return opts
}

// fakeBuildServer creates an httptest.Server that responds to /v1/builds/<name>
// with the given BuildResponse sequence. Each call to GetBuild returns the next
// response; once exhausted it repeats the last one. Progress endpoint returns 404.
func fakeBuildServer(t *testing.T, responses []buildcontract.BuildResponse) *httptest.Server {
	t.Helper()
	callIdx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/progress") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		idx := callIdx
		if idx < len(responses)-1 {
			callIdx++
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(responses[idx]); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
}

func TestWaitForBuildCompletion_FlashFeedback(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		flash       *buildcontract.FlashOutcomeStatus
		wantLease   string
		wantFlashed bool
		wantError   string
	}{
		{
			name: "successful flash", message: "Build completed",
			flash:     &buildcontract.FlashOutcomeStatus{Enabled: true, State: "Succeeded", LeaseID: "flash-lease"},
			wantLease: "flash-lease", wantFlashed: true,
		},
		{
			name: "missing lease", message: "Build and flash completed successfully",
			flash:     &buildcontract.FlashOutcomeStatus{Enabled: true, State: "Succeeded"},
			wantError: "successful flash but no lease ID",
		},
		{
			name: "flash not started overrides message wording", message: "Build completed without flashing",
			flash: &buildcontract.FlashOutcomeStatus{State: "NotStarted"},
		},
		{
			name: "no flash outcome", message: "Build and flash completed successfully",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := buildcontract.BuildResponse{Name: "test-build", Phase: "Completed", Message: tt.message, Flash: tt.flash}
			srv := fakeBuildServer(t, []buildcontract.BuildResponse{st})
			defer srv.Close()
			api, err := buildapiclient.New(srv.URL)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(newTestOpts())
			var waitErr error
			output := captureStdout(t, func() {
				waitErr = h.waitForBuildCompletion(t.Context(), api, st.Name)
			})
			if tt.wantError != "" {
				if waitErr == nil || !strings.Contains(waitErr.Error(), tt.wantError) {
					t.Fatalf("waitForBuildCompletion error = %v, want %q", waitErr, tt.wantError)
				}
				if strings.Contains(output, "Build and flash completed successfully!") || strings.Contains(output, "caib image logs") || strings.Contains(output, "jmp ") {
					t.Errorf("incomplete flash status should not produce success or recovery guidance: %s", output)
				}
				return
			}
			if waitErr != nil {
				t.Fatalf("waitForBuildCompletion failed: %v", waitErr)
			}
			if got := strings.Contains(output, "Build and flash completed successfully!"); got != tt.wantFlashed {
				t.Fatalf("flash banner present = %v, want %v; output: %s", got, tt.wantFlashed, output)
			}
			if !tt.wantFlashed {
				return
			}
			for _, want := range []string{"Lease ID: " + tt.wantLease, "jmp shell --lease " + tt.wantLease, "jmp delete leases " + tt.wantLease} {
				if !strings.Contains(output, want) {
					t.Errorf("missing %q in completion output: %s", want, output)
				}
			}
		})
	}
}

func TestFinishBuild_StructuredOutputUsesStatusLease(t *testing.T) {
	wasQuiet := clilog.IsQuiet()
	clilog.SetQuiet(true)
	t.Cleanup(func() { clilog.SetQuiet(wasQuiet) })
	for _, tt := range []struct {
		name  string
		phase string
		state string
		wait  bool
	}{
		{name: "without waiting", phase: "Completed", state: "Succeeded"},
		{name: "failed flash", phase: "Failed", state: "Failed", wait: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := buildcontract.BuildResponse{
				Name: "test-build", Phase: tt.phase, Message: "terminal build result",
				Flash: &buildcontract.FlashOutcomeStatus{Enabled: true, State: tt.state, LeaseID: "status-lease"},
			}
			srv := fakeBuildServer(t, []buildcontract.BuildResponse{st})
			defer srv.Close()
			api, err := buildapiclient.New(srv.URL)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			opts := newTestOpts()
			opts.Output.Format = "json"
			var buildErr error
			opts.HandleError = func(err error) { buildErr = err }
			h := NewHandler(opts)
			output := captureStdout(t, func() {
				h.finishBuild(t.Context(), api, st.Name, tt.wait)
			})
			var result BuildResult
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("invalid structured output: %v; output: %s", err, output)
			}
			if result.LeaseID != "status-lease" {
				t.Errorf("lease ID = %q, want status-lease", result.LeaseID)
			}
			if got := buildErr != nil; got != (tt.phase == "Failed") {
				t.Errorf("build error present = %v, want %v", got, tt.phase == "Failed")
			}
		})
	}
}

func TestWaitForBuildCompletion_BuildFailedNoDiskImage(t *testing.T) {
	responses := []buildcontract.BuildResponse{
		{
			Name:    "test-build",
			Phase:   "Failed",
			Message: `"step-build-image" exited with code 1`,
		},
	}
	srv := fakeBuildServer(t, responses)
	defer srv.Close()

	opts := newTestOpts()
	opts.Connection.ServerURL = srv.URL
	timeout := 1
	opts.Output.Timeout = timeout

	var capturedErr error
	opts.HandleError = func(err error) { capturedErr = err }
	h := NewHandler(opts)

	api, err := buildapiclient.New(srv.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Capture stdout to verify no flash instructions printed
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	waitErr := h.waitForBuildCompletion(t.Context(), api, "test-build")

	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old

	if waitErr == nil {
		t.Fatal("expected error from waitForBuildCompletion")
	}

	output := string(out)
	if strings.Contains(output, "Flash failed") {
		t.Errorf("should not show flash instructions when build itself failed, got: %s", output)
	}
	if strings.Contains(output, "flash manually") {
		t.Errorf("should not show manual flash guidance when build failed, got: %s", output)
	}
	if capturedErr != nil {
		t.Fatalf("waiter must return the failure without handling it: %v", capturedErr)
	}
}

func TestWaitForBuildCompletion_BuildFailedWithDiskImage_NotFlashFailure(t *testing.T) {
	// Edge case: server returns DiskImage on a build failure (shouldn't happen
	// with server fix, but tests client-side defense-in-depth).
	responses := []buildcontract.BuildResponse{
		{
			Name:      "test-build",
			Phase:     "Failed",
			Message:   `"step-build-image" exited with code 1`,
			DiskImage: "registry.example.com/img:disk",
		},
	}
	srv := fakeBuildServer(t, responses)
	defer srv.Close()

	opts := newTestOpts()
	opts.Connection.ServerURL = srv.URL
	opts.Flash.AfterBuild = true
	timeout := 1
	opts.Output.Timeout = timeout

	var capturedErr error
	opts.HandleError = func(err error) { capturedErr = err }
	h := NewHandler(opts)

	api, err := buildapiclient.New(srv.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	waitErr := h.waitForBuildCompletion(t.Context(), api, "test-build")

	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old

	if waitErr == nil {
		t.Fatal("expected error from waitForBuildCompletion")
	}

	output := string(out)
	// Even though DiskImage is set and FlashAfterBuild is true, the message
	// does not indicate a flash failure, so no flash instructions should appear.
	if strings.Contains(output, "Flash failed") {
		t.Errorf("should not show flash instructions for build failure (not flash failure), got: %s", output)
	}
	if capturedErr != nil {
		t.Fatalf("waiter must return the failure without handling it: %v", capturedErr)
	}
}

func TestFinishBuild_FlashFailure_ShowsFlashInstructions(t *testing.T) {
	responses := []buildcontract.BuildResponse{
		{
			Name:      "test-build",
			Phase:     "Failed",
			Message:   "Flash to device failed: timeout waiting for device",
			DiskImage: "registry.example.com/ns/test-build:disk",
			Flash:     &buildcontract.FlashOutcomeStatus{Enabled: true, State: "Failed"},
			Jumpstarter: &buildcontract.JumpstarterInfo{
				Available:        true,
				ExporterSelector: "board-type=renesas-rcar-s4,enabled=true",
				FlashCmd:         "j storage flash oci://registry.example.com/ns/test-build:disk",
			},
		},
	}
	srv := fakeBuildServer(t, responses)
	defer srv.Close()

	opts := newTestOpts()
	opts.Connection.ServerURL = srv.URL
	opts.Flash.AfterBuild = true
	timeout := 1
	opts.Output.Timeout = timeout

	var capturedErr error
	opts.HandleError = func(err error) { capturedErr = err }
	h := NewHandler(opts)

	api, err := buildapiclient.New(srv.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	h.finishBuild(t.Context(), api, "test-build", true)

	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old

	output := string(out)
	if !strings.Contains(output, "Flash failed") {
		t.Errorf("expected flash failure message, got: %s", output)
	}
	if !strings.Contains(output, "flash manually") {
		t.Errorf("expected manual flash guidance, got: %s", output)
	}
	if !strings.Contains(output, "j storage flash") {
		t.Errorf("expected flash command in output, got: %s", output)
	}
	if capturedErr == nil {
		t.Fatal("expected HandleError to be called")
	}
	if !strings.Contains(capturedErr.Error(), "Flash to device failed") {
		t.Errorf("error should contain flash failure message, got: %v", capturedErr)
	}
}

func TestFinishBuild_FlashFailure_NoJumpstarter(t *testing.T) {
	// Flash failure detected but no Jumpstarter info — should still call
	// handleFlashError (which calls handleError) but not print flash instructions.
	responses := []buildcontract.BuildResponse{
		{
			Name:      "test-build",
			Phase:     "Failed",
			Message:   "Flash to device failed: connection refused",
			DiskImage: "registry.example.com/ns/test-build:disk",
			Flash:     &buildcontract.FlashOutcomeStatus{Enabled: true, State: "Failed"},
		},
	}
	srv := fakeBuildServer(t, responses)
	defer srv.Close()

	opts := newTestOpts()
	opts.Connection.ServerURL = srv.URL
	opts.Flash.AfterBuild = true
	timeout := 1
	opts.Output.Timeout = timeout

	var capturedErr error
	opts.HandleError = func(err error) { capturedErr = err }
	h := NewHandler(opts)

	api, err := buildapiclient.New(srv.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	h.finishBuild(t.Context(), api, "test-build", true)

	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old

	output := string(out)
	// No Jumpstarter info, so no "flash manually" instructions should appear
	if strings.Contains(output, "flash manually") {
		t.Errorf("should not show flash instructions without Jumpstarter info, got: %s", output)
	}
	if capturedErr == nil {
		t.Fatal("expected HandleError to be called")
	}
	if !strings.Contains(capturedErr.Error(), "Flash to device failed") {
		t.Errorf("error should contain flash failure message, got: %v", capturedErr)
	}
}
