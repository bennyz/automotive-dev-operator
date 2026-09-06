package caibcommon

import (
	"testing"
	"time"
)

func TestFormatAge(t *testing.T) {
	ref := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"seconds ago", 30 * time.Second, "30s"},
		{"minutes ago", 5 * time.Minute, "5m"},
		{"hours ago", 3 * time.Hour, "3h"},
		{"days ago", 2 * 24 * time.Hour, "2d"},
		{"months ago", 60 * 24 * time.Hour, "2mo"},
		{"years ago", 400 * 24 * time.Hour, "1y"},
		{"future", -30 * time.Second, "future"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := ref.Add(-tt.offset).Format(time.RFC3339)
			got := formatAgeFrom(ts, ref)
			if got != tt.want {
				t.Errorf("formatAgeFrom(%q, ref) = %q, want %q", ts, got, tt.want)
			}
		})
	}
}

func TestFormatAge_InvalidTimestamp(t *testing.T) {
	got := FormatAge("not-a-timestamp")
	if got != "not-a-timestamp" {
		t.Errorf("expected raw string passthrough, got %q", got)
	}
}
