package controllerutils

import "strings"

// SanitizeLabelValue ensures a string is valid as a Kubernetes label value:
// max 63 chars, only [a-zA-Z0-9._-], must start/end with alphanumeric.
func SanitizeLabelValue(v string) string {
	if v == "" {
		return ""
	}

	cleaned := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, v)

	if len(cleaned) > 63 {
		cleaned = cleaned[:63]
	}

	cleaned = strings.Trim(cleaned, "-_.")
	return cleaned
}
