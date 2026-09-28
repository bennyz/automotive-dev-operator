package image

import "github.com/spf13/cobra"

func addGitSourceFlags(cmd *cobra.Command, opts Options) {
	cmd.Flags().StringVar(opts.GitURL, "git-url", "", "HTTPS repository containing the manifest and optional adjacent <manifest>.lock")
	cmd.Flags().StringVar(opts.GitRef, "git-ref", "", "Git branch, tag, or commit (default: remote HEAD)")
	cmd.Flags().StringVar(opts.GitSecret, "git-secret", "", "Namespace-local basic-auth Secret for Git checkout")
	cmd.Flags().StringVar(opts.GitLockfile, "git-lockfile", "", "Repository-relative lockfile path in the selected Git commit (default: adjacent <manifest>.lock)")
}
