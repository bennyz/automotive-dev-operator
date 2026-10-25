package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
)

func (c *Client) workspaceRunRequest(ctx context.Context, method, name, id, action string, body io.Reader) (*http.Request, error) {
	endpoint := c.resolve(path.Join("/v1/workspaces", url.PathEscape(name), "runs", url.PathEscape(id), action))
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	return req, nil
}

func (c *Client) workspaceRunResult(req *http.Request) (*buildcontract.WorkspaceRun, error) {
	resp, err := c.doRequest(req, "workspace run", statusIn(http.StatusOK, http.StatusAccepted))
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	var run buildcontract.WorkspaceRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return nil, err
	}
	return &run, nil
}

// CreateWorkspaceRun starts a command independently of this request's context.
// Supply an ID to safely retry creation after a lost response.
func (c *Client) CreateWorkspaceRun(ctx context.Context, name string, run buildcontract.WorkspaceRunRequest) (*buildcontract.WorkspaceRun, error) {
	body, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	req, err := c.workspaceRunRequest(ctx, http.MethodPost, name, "", "", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return c.workspaceRunResult(req)
}

// GetWorkspaceRun retrieves the retained status, exit code and log byte count.
func (c *Client) GetWorkspaceRun(ctx context.Context, name, id string) (*buildcontract.WorkspaceRun, error) {
	req, err := c.workspaceRunRequest(ctx, http.MethodGet, name, id, "", nil)
	if err != nil {
		return nil, err
	}
	return c.workspaceRunResult(req)
}

// CancelWorkspaceRun requests process-group cancellation; repeated calls are safe.
func (c *Client) CancelWorkspaceRun(ctx context.Context, name, id string) (*buildcontract.WorkspaceRun, error) {
	req, err := c.workspaceRunRequest(ctx, http.MethodPost, name, id, "cancel", nil)
	if err != nil {
		return nil, err
	}
	return c.workspaceRunResult(req)
}

// WorkspaceRunLogs reads unmodified log bytes from offset. Callers resume by
// adding the number of bytes consumed, including partial reads before an error.
func (c *Client) WorkspaceRunLogs(ctx context.Context, name, id string, follow bool, offset int64) (io.ReadCloser, error) {
	req, err := c.workspaceRunRequest(ctx, http.MethodGet, name, id, "logs", nil)
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	query.Set("follow", strconv.FormatBool(follow))
	query.Set("offset", strconv.FormatInt(offset, 10))
	req.URL.RawQuery = query.Encode()
	resp, err := c.doRequest(req, "workspace run logs", statusIn(http.StatusOK))
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
