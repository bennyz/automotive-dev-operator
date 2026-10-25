package workspace

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// workspaceRunCommand preserves exec's literal argument and strict script
// behavior while submitting a durable shell command instead of a live stream.
func workspaceRunCommand(cmd *cobra.Command, args []string) (string, error) {
	if cmd.Flags().Changed("script") {
		if len(args) != 0 {
			return "", fmt.Errorf("--script cannot be combined with command arguments")
		}
		filename, _ := cmd.Flags().GetString("script")
		script, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("read script: %w", err)
		}
		if strings.TrimSpace(string(script)) == "" {
			return "", fmt.Errorf("script is empty")
		}
		// Pass contents as a literal argument. A temporary file keeps the script's
		// stdin at EOF, as in /exec, and is removed on success or failure.
		args = []string{"/bin/bash", "-c", `set -euo pipefail
script=$(mktemp)
trap 'rm -f -- "$script"' EXIT
printf '%s' "$1" > "$script"
/bin/bash -euo pipefail "$script"`, "caib-workspace-script", string(script)}
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " "), nil
}
