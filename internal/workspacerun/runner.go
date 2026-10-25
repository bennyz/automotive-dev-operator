// Package workspacerun implements command runs stored on the workspace volume.
package workspacerun

import (
	_ "embed"
	"regexp"
)

//go:embed runner.py
var script string

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID excludes path traversal and shell syntax from run identifiers.
func ValidID(id string) bool { return validID.MatchString(id) }

// Command returns an argv, never shell-interpolated user input. Python is part
// of the workspace toolchain image. The command and supervisor each use setsid.
func Command(action string, args ...string) []string {
	return append([]string{"python3", "-c", script, "/workspace/.runs", "/workspace/src", action}, args...)
}
