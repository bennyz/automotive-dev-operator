package buildapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/buildcontract"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/workspacerun"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type workspaceRunResult struct {
	Run   buildcontract.WorkspaceRun `json:"run"`
	Data  []byte                     `json:"data"`
	Code  int                        `json:"code"`
	Error string                     `json:"error"`
}

// runWorkspaceOperation always separates protocol output from exec diagnostics.
// Each operation is short; the detached supervisor owns the command lifetime.
func (a *APIServer) runWorkspaceOperation(ctx context.Context, cfg *rest.Config, ws *automotivev1alpha1.Workspace, action string, args ...string) (workspaceRunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err := a.exec.stream(ctx, cfg, ws.Namespace, ws.Status.PodName, workspaceContainerName,
		workspacerun.Command(action, args...), strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		return workspaceRunResult{}, wrapPodStreamError("workspace run "+action, err, &stderr)
	}
	var result workspaceRunResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return result, fmt.Errorf("invalid workspace run response: %w", err)
	}
	return result, nil
}

func (a *APIServer) workspaceRunTarget(c *gin.Context, name string) (*automotivev1alpha1.Workspace, *rest.Config, bool) {
	ws, err := a.getOwnedWorkspace(c, name)
	if err != nil {
		return nil, nil, false
	}
	if ws.Spec.Stopped || ws.Status.Phase != phaseRunning || ws.Status.PodName == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "start the workspace to access its runs and retained logs"})
		return nil, nil, false
	}
	cfg, err := a.deps.getRESTConfigFromRequest(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get kubernetes config"})
		return nil, nil, false
	}
	return ws, cfg, true
}

func (a *APIServer) workspaceRunError(c *gin.Context, result workspaceRunResult, err error) bool {
	if err != nil {
		a.log.Error(err, "Workspace run operation failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "workspace run operation failed; retry using the same run ID"})
		return true
	}
	if result.Error != "" {
		code := result.Code
		if code < 400 || code > 599 {
			code = http.StatusInternalServerError
		}
		c.JSON(code, gin.H{"error": result.Error})
		return true
	}
	return false
}

func (a *APIServer) createWorkspaceRun(c *gin.Context, name string) {
	var req buildcontract.WorkspaceRunRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON request"})
		return
	}
	if strings.TrimSpace(req.Command) == "" || len(req.Command) > 64<<10 || strings.ContainsRune(req.Command, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "command must contain 1 to 65536 bytes and no NUL"})
		return
	}
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if !workspacerun.ValidID(req.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run ID"})
		return
	}
	ws, cfg, ok := a.workspaceRunTarget(c, name)
	if !ok {
		return
	}
	if !a.recordWorkspaceRunActivity(c, ws) {
		return
	}
	// Finish the bounded launch handshake even if the initiating HTTP connection
	// disappears. Retrying this ID returns the existing run, never another command.
	result, err := a.runWorkspaceOperation(context.WithoutCancel(c.Request.Context()), cfg, ws, "create", req.ID, req.Command)
	if a.workspaceRunError(c, result, err) {
		return
	}
	c.Header("Location", "/v1/workspaces/"+name+"/runs/"+req.ID)
	c.JSON(http.StatusAccepted, result.Run)
}

// Reserve an inactivity window before launching. This must succeed, and must
// conflict with a simultaneous stop decision; a best-effort activity update
// could allow the pod to disappear between the readiness check and launch.
func (a *APIServer) recordWorkspaceRunActivity(c *gin.Context, ws *automotivev1alpha1.Workspace) bool {
	kube, err := a.deps.getClientFromRequest(c)
	if err == nil {
		patch := client.MergeFromWithOptions(ws.DeepCopy(), client.MergeFromWithOptimisticLock{})
		now := metav1.Now()
		ws.Status.LastActivityTime = &now
		err = kube.Status().Patch(c.Request.Context(), ws, patch)
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "could not record workspace activity; retry using the same run ID"})
		return false
	}
	return true
}

func (a *APIServer) workspaceRunAction(c *gin.Context, name, action string) {
	if !validWorkspaceRunParam(c) {
		return
	}
	ws, cfg, ok := a.workspaceRunTarget(c, name)
	if !ok {
		return
	}
	result, err := a.runWorkspaceOperation(c.Request.Context(), cfg, ws, action, c.Param("run"))
	if a.workspaceRunError(c, result, err) {
		return
	}
	c.JSON(http.StatusOK, result.Run)
}

func validWorkspaceRunParam(c *gin.Context) bool {
	if !workspacerun.ValidID(c.Param("run")) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run ID"})
		return false
	}
	return true
}

func (a *APIServer) getWorkspaceRun(c *gin.Context, name string) {
	a.workspaceRunAction(c, name, "get")
}

func (a *APIServer) cancelWorkspaceRun(c *gin.Context, name string) {
	a.workspaceRunAction(c, name, "cancel")
}

func (a *APIServer) workspaceRunLogs(c *gin.Context, name string) {
	if !validWorkspaceRunParam(c) {
		return
	}
	offset, err := strconv.ParseInt(c.DefaultQuery("offset", "0"), 10, 64)
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative byte offset"})
		return
	}
	followValue, follow := c.GetQuery("follow")
	// Gin's GetQuery treats an empty query value as absent; ?follow is valid.
	if c.Request.URL.Query().Has("follow") {
		follow = true
		if followValue != "" {
			follow, err = strconv.ParseBool(followValue)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "follow must be a boolean"})
				return
			}
		}
	}
	ws, cfg, ok := a.workspaceRunTarget(c, name)
	if !ok {
		return
	}
	duration := time.Duration(a.limits.MaxLogStreamDurationMinutes) * time.Minute
	if duration <= 0 {
		duration = 120 * time.Minute
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
	defer cancel()
	read := func() (workspaceRunResult, error) {
		return a.runWorkspaceOperation(ctx, cfg, ws, "logs", c.Param("run"), strconv.FormatInt(offset, 10))
	}
	result, err := read()
	if a.workspaceRunError(c, result, err) {
		return
	}
	// A non-following read is a snapshot, even if a command keeps appending.
	end := result.Run.LogBytes
	// The build log helper writes a banner. Run logs must contain only stored
	// command bytes, otherwise every reconnect would corrupt the resume offset.
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "no-store, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Log-Offset", strconv.FormatInt(offset, 10))
	c.Header("Trailer", "X-Log-Error")
	c.Writer.Flush()
	for {
		data := result.Data
		if !follow && int64(len(data)) > end-offset {
			data = data[:end-offset]
		}
		if len(data) > 0 {
			n, writeErr := c.Writer.Write(data)
			offset += int64(n)
			if writeErr != nil {
				return
			}
			c.Writer.Flush()
		}
		if (!follow && offset >= end) || (result.Run.Terminal() && offset >= result.Run.LogBytes) {
			return
		}
		if offset >= result.Run.LogBytes {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		result, err = read()
		if err != nil || result.Error != "" {
			// Never splice diagnostics into the byte-addressed command output.
			c.Header("X-Log-Error", "workspace log read failed; resume from the last received byte offset")
			return
		}
	}
}
