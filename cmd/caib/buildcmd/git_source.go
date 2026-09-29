package buildcmd

import (
	"fmt"
	"os"
	"path"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	buildapi "github.com/centos-automotive-suite/automotive-dev-operator/internal/buildapi"
	"github.com/spf13/cobra"
)

func (h *Handler) readBuildSource(manifestPath string) ([]byte, *api.GitSource, error) {
	url, ref, secret, gitLockfile := ptrStr(h.opts.GitURL), ptrStr(h.opts.GitRef), ptrStr(h.opts.GitSecret), ptrStr(h.opts.GitLockfile)
	if url == "" {
		if ref != "" || secret != "" || gitLockfile != "" {
			return nil, nil, fmt.Errorf("--git-ref, --git-secret, and --git-lockfile require --git-url")
		}
		data, err := os.ReadFile(manifestPath)
		return data, nil, err
	}
	if ptrStr(h.opts.Lockfile) != "" {
		return nil, nil, fmt.Errorf("--lockfile cannot be used with --git-url; commit %s beside the manifest or use --git-lockfile for another committed file", path.Base(defaultLockfilePath(manifestPath)))
	}
	if ptrStr(h.opts.Workspace) != "" || ptrStr(h.opts.LocalRepo) != "" || (h.opts.ExtraRepos != nil && len(*h.opts.ExtraRepos) != 0) {
		return nil, nil, fmt.Errorf("git builds do not support workspace or extra repository overlays")
	}
	if gitLockfile != "" {
		gitLockfile = path.Clean(gitLockfile)
	}
	source := &api.GitSource{URL: url, Revision: ref, ManifestPath: path.Clean(manifestPath), LockfilePath: gitLockfile, CredentialsSecretRef: secret}
	if err := api.ValidateGitSource(source); err != nil {
		return nil, nil, err
	}
	return nil, source, nil
}

// Leave target-dependent defaults unset until the source TaskRun reads the manifest.
func deferGitDefaults(cmd *cobra.Command, req *buildapi.BuildRequest) {
	if req.GitSource == nil {
		return
	}
	if !cmd.Flags().Changed("target") {
		req.Target = ""
	}
	if !cmd.Flags().Changed("arch") {
		req.ArchitectureFallback = req.Architecture
		req.Architecture = ""
	}
	if !cmd.Flags().Changed("format") && !cmd.Flags().Changed("disk-format") {
		req.ExportFormat = ""
	}
}
