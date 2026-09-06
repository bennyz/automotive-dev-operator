package buildapi

import (
	"testing"
)

func TestManifestNeedsUpload(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     bool
	}{
		{
			name:     "no add_files",
			manifest: "name: simple\n",
			want:     false,
		},
		{
			name: "add_files with source_path",
			manifest: `content:
  add_files:
    - source_path: my-app
      dest: /usr/bin/my-app
`,
			want: true,
		},
		{
			name: "add_files with source only",
			manifest: `content:
  add_files:
    - source: config.toml
      dest: /etc/my-app/config.toml
`,
			want: false,
		},
		{
			name: "qm section with source_path",
			manifest: `qm:
  content:
    add_files:
      - source_path: qm-app
        dest: /usr/bin/qm-app
`,
			want: true,
		},
		{
			name:     "invalid yaml returns false",
			manifest: "{{invalid yaml",
			want:     false,
		},
		{
			name: "add_files with source_glob only",
			manifest: `content:
  add_files:
    - source_glob: "*.rpm"
      dest: /tmp/rpms/
`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := manifestNeedsUpload(tt.manifest)
			if got != tt.want {
				t.Errorf("manifestNeedsUpload() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractManifestSourceFiles(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     []string
	}{
		{
			name:     "no add_files",
			manifest: "name: simple\n",
			want:     nil,
		},
		{
			name: "extracts relative source references",
			manifest: `content:
  add_files:
    - source: config.toml
      dest: /etc/config.toml
    - source_path: my-binary
      dest: /usr/bin/my-binary
    - source_glob: "*.conf"
      dest: /etc/conf.d/
`,
			want: []string{"config.toml", "my-binary", "*.conf"},
		},
		{
			name: "skips absolute paths",
			manifest: `content:
  add_files:
    - source: /absolute/path
      dest: /etc/foo
`,
			want: nil,
		},
		{
			name: "skips http URLs",
			manifest: `content:
  add_files:
    - source: https://example.com/file.tar
      dest: /tmp/file.tar
`,
			want: nil,
		},
		{
			name: "skips plain http URLs",
			manifest: `content:
  add_files:
    - source: http://example.com/file.tar
      dest: /tmp/file.tar
`,
			want: nil,
		},
		{
			name: "keeps filenames starting with http",
			manifest: `content:
  add_files:
    - source: httpd.conf
      dest: /etc/httpd.conf
`,
			want: []string{"httpd.conf"},
		},
		{
			name: "combines content and qm sections",
			manifest: `content:
  add_files:
    - source: app.conf
      dest: /etc/app.conf
qm:
  content:
    add_files:
      - source_path: qm-service
        dest: /usr/bin/qm-service
`,
			want: []string{"app.conf", "qm-service"},
		},
		{
			name:     "invalid yaml returns nil",
			manifest: "{{broken",
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractManifestSourceFiles(tt.manifest)
			if len(got) != len(tt.want) {
				t.Fatalf("extractManifestSourceFiles() returned %d files, want %d: got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("file[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
