package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	buildapiclient "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi/client"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
)

func TestFollowWorkspaceRunResumesPartialReadsAndDrainsTerminalLogs(t *testing.T) {
	data := []byte{'a', 0, 0xf0, 0x9f, 0x98, 0x80, '\n', 'z'}
	var offsets []int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("run request lost authorization")
		}
		if strings.HasSuffix(r.URL.Path, "/logs") {
			offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
			offsets = append(offsets, offset)
			if len(offsets) == 1 {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				_, _ = w.Write(data[:3]) // Truncated response, including half a UTF-8 character.
				return
			}
			if offset != 3 {
				t.Errorf("wrong resume offset: %d", offset)
			}
			_, _ = w.Write(data[offset:])
			return
		}
		// The run is already finished when the first connection fails, but the
		// CLI must fetch the remaining bytes before returning the exit status.
		_, _ = fmt.Fprintf(w, `{"id":"run","status":"Failed","exitCode":37,"logBytes":%d}`, len(data))
	}))
	defer server.Close()
	api, err := buildapiclient.New(server.URL, buildapiclient.WithAuthToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	var offset int64
	code, err := followWorkspaceRun(ctx, func(fn func(*buildapiclient.Client) error) error { return fn(api) }, "dev", "run", &offset, &output, time.Millisecond)
	if err != nil || code != 37 || !bytes.Equal(output.Bytes(), data) || offset != int64(len(data)) || len(offsets) != 2 {
		t.Fatalf("follow: code=%d err=%v output=%q offset=%d attempts=%v", code, err, output.Bytes(), offset, offsets)
	}
}

func TestFollowWorkspaceRunReportsUnavailableExitCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs") {
			_, _ = io.WriteString(w, `{"id":"run","status":"Interrupted","message":"pod stopped","logBytes":0}`)
		}
	}))
	defer server.Close()
	api, err := buildapiclient.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	_, err = followWorkspaceRun(context.Background(), func(fn func(*buildapiclient.Client) error) error { return fn(api) }, "dev", "run", &offset, io.Discard, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "exit code unavailable") {
		t.Fatalf("interruption was treated as success: %v", err)
	}
}

func TestWorkspaceExecExitCode(t *testing.T) {
	if os.Getenv("CAIB_RUN_EXIT_HELPER") == "1" {
		cmd := NewWorkspaceCmd(nil)
		cmd.SetArgs([]string{"exec", "dev", "--server", os.Getenv("CAIB_RUN_TEST_SERVER"), "--token", "test-token", "--", "exit 42"})
		if err := cmd.Execute(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(99)
		}
		return
	}
	var id string
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			var request buildcontract.WorkspaceRunRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			creates++
			if request.ID == "" || (id != "" && id != request.ID) || request.Command != "exit 42" {
				t.Errorf("invalid or changed create request: %+v", request)
			}
			id = request.ID
			if creates == 1 {
				// The server accepted the command but its response was lost.
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"Running"}`, id)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = io.WriteString(w, "command output\n")
		default:
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"Failed","exitCode":42,"logBytes":15}`, id)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceExecExitCode$")
	cmd.Env = append(os.Environ(), "CAIB_RUN_EXIT_HELPER=1", "CAIB_RUN_TEST_SERVER="+server.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 42 || string(output) != "command output\n" || creates != 2 {
		t.Fatalf("CLI exit: %v, stdout=%q stderr=%q creates=%d", err, output, stderr.String(), creates)
	}
}
