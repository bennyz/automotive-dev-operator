package container

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centos-automotive-suite/automotive-dev-operator/cmd/caib/clilog"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
)

func TestDisplayContainerBuildResultCredentialsRequireRecordedImage(t *testing.T) {
	for _, tc := range []struct {
		name, output, digest string
		wantCredentials      bool
	}{
		{name: "no output", digest: "sha256:recorded"},
		{name: "no digest", output: "registry.example/requested:latest"},
		{name: "recorded image", output: "registry.example/image:latest", digest: "sha256:recorded", wantCredentials: true},
	} {
		for _, quiet := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/quiet=%t", tc.name, quiet), func(t *testing.T) {
				tmp := t.TempDir()
				t.Setenv("TMPDIR", tmp)
				t.Setenv("NO_COLOR", "1")
				clilog.SetQuiet(quiet)
				t.Cleanup(func() { clilog.SetQuiet(false) })
				capturePath := filepath.Join(tmp, "stdout")
				capture, err := os.Create(capturePath)
				if err != nil {
					t.Fatal(err)
				}
				previous := os.Stdout
				os.Stdout = capture
				t.Cleanup(func() { os.Stdout = previous; _ = capture.Close() })
				status := &buildcontract.ContainerBuildResponse{
					Name: "build", Phase: phaseCompleted, OutputImage: tc.output,
					ImageDigest: tc.digest, RegistryToken: "test-registry-token",
				}
				displayContainerBuildResult(status)
				os.Stdout = previous
				if err := capture.Close(); err != nil {
					t.Fatal(err)
				}
				files, err := filepath.Glob(filepath.Join(tmp, "caib-registry-creds-*.json"))
				if err != nil {
					t.Fatal(err)
				}
				wantFiles := 0
				if tc.wantCredentials {
					wantFiles = 1
				}
				if len(files) != wantFiles || (status.RegistryToken != "") != tc.wantCredentials {
					t.Fatalf("credential files=%d, token returned=%t", len(files), status.RegistryToken != "")
				}
				if !tc.wantCredentials {
					return
				}
				output, err := os.ReadFile(capturePath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(output), files[0]) {
					t.Fatal("credential file path was not printed")
				}
				data, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				var credentials map[string]string
				if err := json.Unmarshal(data, &credentials); err != nil {
					t.Fatal(err)
				}
				if credentials["username"] != "serviceaccount" || credentials["token"] != "test-registry-token" {
					t.Fatal("credential file does not contain the expected credentials")
				}
			})
		}
	}
}

func TestParseContainerBuildArgs(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]string
		wantLen int
	}{
		{
			name:    "single arg",
			input:   []string{"VERSION=1.0"},
			want:    map[string]string{"VERSION": "1.0"},
			wantLen: 1,
		},
		{
			name:    "multiple args",
			input:   []string{"VERSION=1.0", "ENV=prod"},
			want:    map[string]string{"VERSION": "1.0", "ENV": "prod"},
			wantLen: 2,
		},
		{
			name:    "value with equals sign",
			input:   []string{"CMD=echo foo=bar"},
			want:    map[string]string{"CMD": "echo foo=bar"},
			wantLen: 1,
		},
		{
			name:    "empty input",
			input:   []string{},
			want:    map[string]string{},
			wantLen: 0,
		},
		{
			name:    "value with spaces",
			input:   []string{"MSG=hello world"},
			want:    map[string]string{"MSG": "hello world"},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseContainerBuildArgs(tt.input)
			if len(got) != tt.wantLen {
				t.Errorf("parseContainerBuildArgs() returned %d args, want %d", len(got), tt.wantLen)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseContainerBuildArgs()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestContainerBuildPhaseStep(t *testing.T) {
	tests := []struct {
		phase string
		want  int
	}{
		{phasePending, 0},
		{phaseUploading, 1},
		{"Building", 2},
		{phaseCompleted, containerBuildTotalSteps},
		{phaseFailed, containerBuildTotalSteps},
		{"Unknown", 1}, // default
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			got := containerBuildPhaseStep(tt.phase)
			if got != tt.want {
				t.Errorf("containerBuildPhaseStep(%q) = %d, want %d", tt.phase, got, tt.want)
			}
		})
	}
}

func TestIsContainerBuildTerminal(t *testing.T) {
	tests := []struct {
		phase string
		want  bool
	}{
		{phaseCompleted, true},
		{phaseFailed, true},
		{phasePending, false},
		{phaseUploading, false},
		{"Building", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			got := isContainerBuildTerminal(tt.phase)
			if got != tt.want {
				t.Errorf("isContainerBuildTerminal(%q) = %v, want %v", tt.phase, got, tt.want)
			}
		})
	}
}
