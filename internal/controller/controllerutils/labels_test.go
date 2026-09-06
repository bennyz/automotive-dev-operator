package controllerutils

import (
	"strings"
	"testing"
)

func TestSanitizeLabelValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"normal", "autosd", "autosd"},
		{"with-dashes", "aarch64-linux", "aarch64-linux"},
		{"special-chars", "foo@bar:baz", "foo-bar-baz"},
		{"leading-trailing-punct", "---value---", "value"},
		{"over-63-chars", strings.Repeat("a", 70), strings.Repeat("a", 63)},
		{"dots-and-underscores", "a.b_c", "a.b_c"},
		{"only-invalid", "@@@", ""},
		{"spaces", "hello world", "hello-world"},
		{"mixed", "--My.Build_123--", "My.Build_123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeLabelValue(tt.in)
			if got != tt.want {
				t.Errorf("SanitizeLabelValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
