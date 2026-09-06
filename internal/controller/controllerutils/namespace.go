package controllerutils

import (
	"os"
	"strings"
	"sync"
)

const fallbackNamespace = "automotive-dev-operator-system"

var (
	resolvedNS  string
	resolveOnce sync.Once
)

// OperatorNamespace returns the namespace where the operator is deployed.
// Resolution order: WATCH_NAMESPACE env var (set by the manager deployment),
// pod serviceaccount namespace file, hardcoded fallback.
func OperatorNamespace() string {
	resolveOnce.Do(func() {
		if ns := strings.TrimSpace(os.Getenv("WATCH_NAMESPACE")); ns != "" {
			resolvedNS = ns
			return
		}
		if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			if ns := strings.TrimSpace(string(data)); ns != "" {
				resolvedNS = ns
				return
			}
		}
		resolvedNS = fallbackNamespace
	})
	return resolvedNS
}
