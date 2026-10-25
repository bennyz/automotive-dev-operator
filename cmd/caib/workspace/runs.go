package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/centos-automotive-suite/automotive-dev-operator/cmd/caib/clilog"
	caibcommon "github.com/centos-automotive-suite/automotive-dev-operator/cmd/caib/common"
	buildapiclient "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi/client"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type runClientCall func(func(*buildapiclient.Client) error) error

func withRunClient(fn func(*buildapiclient.Client) error) error {
	return caibcommon.ExecuteWithReauth(serverURL, &authToken, insecureSkipTLS, fn)
}

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name> -- <command...>",
		Short: "Execute a durable command in a workspace",
		Long: `Run a command in /workspace/src and follow its retained output.
Network failures reconnect automatically. The CLI returns the command's exit code.
Ctrl-C detaches without stopping the command; use workspace run cancel to stop it.

Everything after -- is joined as a shell command, as with the legacy exec API.

Examples:
  caib workspace exec my-app -- make -j4
  caib workspace exec my-app --detach -- cargo build --release
  caib workspace exec my-app --run <run-id>
  caib workspace exec my-app --run <run-id> --offset 4096`,
		Args: cobra.MinimumNArgs(1),
		Run:  runExec,
	}
	cmd.Flags().String("run", "", "reattach to an existing run instead of starting a command")
	cmd.Flags().Int64("offset", 0, "resume at this log byte offset (with --run)")
	cmd.Flags().Bool("detach", false, "start a run and print its ID without following output")
	return cmd
}

func runExec(cmd *cobra.Command, args []string) {
	requireServer()
	id, _ := cmd.Flags().GetString("run")
	offset, _ := cmd.Flags().GetInt64("offset")
	detach, _ := cmd.Flags().GetBool("detach")
	if offset < 0 || (id == "" && offset != 0) {
		handleError(fmt.Errorf("--offset must be non-negative and requires --run"))
	}
	if (id == "" && len(args) < 2) || (id != "" && (len(args) != 1 || detach)) {
		handleError(fmt.Errorf("specify a command, or use --run <id> to reattach"))
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	if id == "" {
		id = uuid.NewString()
		clilog.Statusf("Run: %s\n", id)
		req := buildcontract.WorkspaceRunRequest{ID: id, Command: strings.Join(args[1:], " ")}
		err := retryRunRequest(ctx, time.Second, func() error {
			requestCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			return withRunClient(func(client *buildapiclient.Client) error {
				_, err := client.CreateWorkspaceRun(requestCtx, args[0], req)
				return err
			})
		})
		if err != nil {
			stop()
			handleError(fmt.Errorf("create run %s: %w; inspect with caib workspace exec %s --run %s", id, err, args[0], id))
		}
	}
	if detach {
		stop()
		fmt.Println(id)
		return
	}
	code, err := followWorkspaceRun(ctx, withRunClient, args[0], id, &offset, os.Stdout, time.Second)
	stop()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Detached. Resume: caib workspace exec %s --run %s --offset %d\n", args[0], id, offset)
			os.Exit(130)
		}
		handleError(fmt.Errorf("run %s: %w; resume with caib workspace exec %s --run %s --offset %d", id, err, args[0], id, offset))
	}
	if code != 0 {
		os.Exit(code)
	}
}

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "run", Short: "Inspect or cancel a durable workspace command"}
	for _, action := range []string{"show", "cancel"} {
		cmd.AddCommand(&cobra.Command{
			Use: action + " <workspace> <run-id>", Short: action + " a workspace run", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				requireServer()
				ctx, cancel := context.WithTimeout(cmd.Context(), 40*time.Second)
				defer cancel()
				var run *buildcontract.WorkspaceRun
				err := withRunClient(func(client *buildapiclient.Client) (err error) {
					if action == "cancel" {
						run, err = client.CancelWorkspaceRun(ctx, args[0], args[1])
					} else {
						run, err = client.GetWorkspaceRun(ctx, args[0], args[1])
					}
					return err
				})
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(run)
			},
		})
	}
	return cmd
}

func transientRunError(err error) bool {
	var response buildapiclient.HTTPError
	if errors.As(err, &response) {
		code := response.HTTPStatusCode()
		return code >= 500 || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func waitRunRetry(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func retryRunRequest(ctx context.Context, delay time.Duration, call func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil || !transientRunError(err) {
			return err
		}
		if err := waitRunRetry(ctx, delay); err != nil {
			return err
		}
	}
}

type runOutput struct {
	io.Writer
	err error
}

func (w *runOutput) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func followWorkspaceRun(ctx context.Context, api runClientCall, name, id string, offset *int64, out io.Writer, retryDelay time.Duration) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		// Periodically rotate streams so a silent VPN black hole cannot leave the
		// client stuck in Read forever. Rotation uses the same byte offset.
		streamCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		var body io.ReadCloser
		err := api(func(client *buildapiclient.Client) (err error) {
			body, err = client.WorkspaceRunLogs(streamCtx, name, id, true, *offset)
			return err
		})
		if err == nil {
			output := &runOutput{Writer: out}
			n, readErr := io.Copy(output, body)
			*offset += n
			_ = body.Close()
			err = readErr
			if output.err != nil {
				cancel()
				return 0, output.err
			}
		}
		cancel()
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil && !transientRunError(err) {
			return 0, err
		}
		var run *buildcontract.WorkspaceRun
		err = retryRunRequest(ctx, retryDelay, func() error {
			requestCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			return api(func(client *buildapiclient.Client) (err error) {
				run, err = client.GetWorkspaceRun(requestCtx, name, id)
				return err
			})
		})
		if err != nil {
			return 0, err
		}
		if run.Terminal() && *offset >= run.LogBytes {
			if run.ExitCode == nil {
				return 0, fmt.Errorf("%s: %s (exit code unavailable)", run.Status, run.Message)
			}
			if run.Status == "Cancelled" && *run.ExitCode == 0 {
				return 130, nil
			}
			return *run.ExitCode, nil
		}
		if err := waitRunRetry(ctx, retryDelay); err != nil {
			return 0, err
		}
	}
}
