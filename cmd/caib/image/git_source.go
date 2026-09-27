package image

import "github.com/spf13/cobra"

func addGitSourceFlags(cmd *cobra.Command, opts Options) {
	cmd.Flags().StringVar(opts.GitURL, "git-url", "", "HTTPS repository containing the manifest and optional adjacent aib.lock")
	cmd.Flags().StringVar(opts.GitRef, "git-ref", "", "Git branch, tag, or commit (default: remote HEAD)")
	cmd.Flags().StringVar(opts.GitSecret, "git-secret", "", "Namespace-local basic-auth Secret for Git checkout")
	cmd.MarkFlagsMutuallyExclusive("git-url", "lockfile")
}
