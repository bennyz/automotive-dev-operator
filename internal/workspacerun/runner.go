// Package workspacerun implements command runs stored on the workspace volume.
package workspacerun

import (
	_ "embed"
	"encoding/json"
	"regexp"
)

//go:embed runner.py
var script string

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID excludes path traversal and shell syntax from run identifiers.
func ValidID(id string) bool { return validID.MatchString(id) }

// Options are operator-controlled limits supplied when a run is created.
type Options struct {
	TimeoutSeconds          int64 `json:"timeoutSeconds"`
	RequestedTimeoutSeconds int64 `json:"requestedTimeoutSeconds"`
	MaxLogBytes             int64 `json:"maxLogBytes"`
	RetentionSeconds        int64 `json:"retentionSeconds"`
	CompletedLogBudgetBytes int64 `json:"completedLogBudgetBytes"`
}

// DefaultOptions supplies the defaults used before OperatorConfig overrides.
func DefaultOptions() Options {
	return Options{TimeoutSeconds: 2 * 60 * 60, MaxLogBytes: 100 << 20,
		RetentionSeconds: 7 * 24 * 60 * 60, CompletedLogBudgetBytes: 1 << 30}
}

// JSON encodes limits for the pod-side runner.
func (o Options) JSON() string {
	data, _ := json.Marshal(o)
	return string(data)
}

// Command returns an argv, never shell-interpolated user input. Python is part
// of the workspace toolchain image. The command and supervisor each use setsid.
func Command(action string, args ...string) []string {
	if action == "create" && len(args) == 2 {
		args = append(args, DefaultOptions().JSON())
	}
	return append([]string{"python3", "-c", script, "/workspace/.runs", "/workspace/src", action}, args...)
}

// ActivityCommand uses /proc for legacy exec sessions as well as recorded runs.
// Images without Python retain activity detection without depending on procps.
func ActivityCommand() []string {
	return append([]string{"/bin/sh", "-c", activityLauncher, "--", "/proc", "/dev/pts"}, Command("active")...)
}

const activityLauncher = `proc=$1; pts=$2; shift 2
if command -v python3 >/dev/null 2>&1; then exec "$@"; fi
for entry in "$pts"/[0-9]*; do
    if [ -e "$entry" ]; then printf '{"active":true}\n'; exit 0; fi
done
for file in "$proc"/[0-9]*/stat; do
    pid=${file%/stat}; pid=${pid##*/}
    [ "$pid" = 1 ] && continue
    [ "$pid" = "$$" ] && continue
    IFS= read -r stat 2>/dev/null < "$file" || continue
    stat=${stat##*) }
    set -- $stat
    if [ "$1" != Z ] && [ "$2" = 0 ]; then printf '{"active":true}\n'; exit 0; fi
done
printf '{"active":false}\n'
`
