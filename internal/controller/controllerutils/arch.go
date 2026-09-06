package controllerutils

import "strings"

// NormalizeArchToK8s maps architecture names to Kubernetes kubernetes.io/arch values.
// Handles both Linux (x86_64, aarch64) and OCI/Go (amd64, arm64) conventions.
func NormalizeArchToK8s(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return arch
	}
}
