package caibcommon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefineFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "string values",
			content: "routing: Engine\nmode: debug\n",
			want:    []string{`mode="debug"`, `routing="Engine"`},
		},
		{
			name:    "numeric values",
			content: "count: 42\nratio: 1.5\n",
			want:    []string{`count=42`, `ratio=1.5`},
		},
		{
			name:    "boolean values",
			content: "enabled: true\nverbose: false\n",
			want:    []string{`enabled=true`, `verbose=false`},
		},
		{
			name:    "list values",
			content: "extra_rpms:\n  - vim\n  - curl\n",
			want:    []string{`extra_rpms=["vim","curl"]`},
		},
		{
			name:    "quoted string preserves type",
			content: "version: \"1.0\"\n",
			want:    []string{`version="1.0"`},
		},
		{
			name:    "map values",
			content: "config:\n  key: val\n",
			want:    []string{`config={"key":"val"}`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "vars.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			got, err := LoadDefineFiles([]string{path})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d defines, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("define[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadDefineFiles_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	file1 := filepath.Join(dir, "base.yaml")
	if err := os.WriteFile(file1, []byte("key1: val1\nshared: from_base\n"), 0644); err != nil {
		t.Fatal(err)
	}

	file2 := filepath.Join(dir, "override.yaml")
	if err := os.WriteFile(file2, []byte("key2: val2\nshared: from_override\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadDefineFiles([]string{file1, file2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{`key1="val1"`, `key2="val2"`, `shared="from_override"`}
	if len(got) != len(want) {
		t.Fatalf("got %d defines, want %d: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("define[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadDefineFiles_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(":\n  - :\n  [invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadDefineFiles([]string{path})
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
