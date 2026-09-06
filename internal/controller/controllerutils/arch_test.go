package controllerutils

import "testing"

func TestNormalizeArchToK8s(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"amd64", "amd64"},
		{"x86_64", "amd64"},
		{"X86_64", "amd64"},
		{" amd64 ", "amd64"},
		{"arm64", "arm64"},
		{"aarch64", "arm64"},
		{"AARCH64", "arm64"},
		{" arm64 ", "arm64"},
		{"s390x", "s390x"},
		{"ppc64le", "ppc64le"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := NormalizeArchToK8s(tt.input); got != tt.want {
				t.Errorf("NormalizeArchToK8s(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
