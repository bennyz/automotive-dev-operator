package caibcommon

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/centos-automotive-suite/automotive-dev-operator/cmd/caib/clilog"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/oci"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func captureStderr(fn func()) string {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func TestPullOCIArtifactQuietSuppressesStdout(t *testing.T) {
	clilog.SetQuiet(true)
	defer clilog.SetQuiet(false)

	out := captureStdout(func() {
		// Call will fail (invalid ref) but the point is that no fmt.Printf
		// output should appear on stdout before the error.
		_ = PullOCIArtifact("invalid://ref", t.TempDir(), "", "", false)
	})

	if out != "" {
		t.Errorf("quiet mode should suppress all stdout output, got: %q", out)
	}
}

func TestPullOCIArtifactNonQuietPrintsToStdout(t *testing.T) {
	clilog.SetQuiet(false)

	out := captureStdout(func() {
		_ = PullOCIArtifact("invalid://ref", t.TempDir(), "", "", false)
	})

	if out == "" {
		t.Error("non-quiet mode should produce stdout output")
	}
}

func TestPullOCIArtifactNonQuietNoProgressOnStderr(t *testing.T) {
	clilog.SetQuiet(false)

	stderr := captureStderr(func() {
		_ = PullOCIArtifact("invalid://ref", t.TempDir(), "", "", false)
	})

	// Progress/info output must not bleed to stderr; only genuine warnings may appear.
	if strings.Contains(stderr, "Downloading") || strings.Contains(stderr, "Pulling") || strings.Contains(stderr, "Extracting") {
		t.Errorf("non-quiet mode should not send informational output to stderr, got: %q", stderr)
	}
}

func TestExtractOCIArtifactBlobQuietSuppressesStdout(t *testing.T) {
	clilog.SetQuiet(true)
	defer clilog.SetQuiet(false)

	out := captureStdout(func() {
		// Will fail (no index.json) but should produce no stdout output.
		_ = extractOCIArtifactBlob(t.TempDir(), t.TempDir())
	})

	if out != "" {
		t.Errorf("quiet mode should suppress extractOCIArtifactBlob stdout output, got: %q", out)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		layerIndex  int
		want        string
		wantWarning string
	}{
		{"empty string uses fallback", "", 0, "layer-0.bin", ""},
		{"valid filename unchanged", "image.qcow2", 2, "image.qcow2", ""},
		{"null byte uses fallback", "bad\x00name", 1, "layer-1.bin", "null bytes"},
		{"absolute path uses fallback", "/etc/passwd", 3, "layer-3.bin", "absolute path"},
		{"dotdot uses fallback", "../escape", 4, "layer-4.bin", "contains '..'"},
		{"path separator uses basename", "sub/dir/file.img", 5, "file.img", "path separators"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			stderr := captureStderr(func() {
				got = sanitizeFilename(tt.filename, tt.layerIndex)
			})

			if got != tt.want {
				t.Errorf("sanitizeFilename(%q, %d) = %q, want %q", tt.filename, tt.layerIndex, got, tt.want)
			}
			if tt.wantWarning != "" {
				if !strings.Contains(stderr, tt.wantWarning) {
					t.Errorf("expected stderr to contain %q, got: %q", tt.wantWarning, stderr)
				}
			} else if stderr != "" {
				t.Errorf("expected no stderr output, got: %q", stderr)
			}
		})
	}
}

type testArtifactLayer struct {
	title   string
	content []byte
}

func writeTestOCIArtifact(t *testing.T, annotations map[string]string, layers ...testArtifactLayer) string {
	t.Helper()
	layout := t.TempDir()
	blobDir := filepath.Join(layout, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeBlob := func(content []byte, mediaType string) ocispec.Descriptor {
		t.Helper()
		d := digest.FromBytes(content)
		if err := os.WriteFile(filepath.Join(blobDir, d.Encoded()), content, 0644); err != nil {
			t.Fatal(err)
		}
		return ocispec.Descriptor{Digest: d, Size: int64(len(content)), MediaType: mediaType}
	}
	manifest := ocispec.Manifest{
		Versioned:   specs.Versioned{SchemaVersion: 2},
		Annotations: annotations,
	}
	for _, layer := range layers {
		descriptor := writeBlob(layer.content, "application/octet-stream")
		descriptor.Annotations = map[string]string{ocispec.AnnotationTitle: layer.title}
		manifest.Layers = append(manifest.Layers, descriptor)
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	indexData, err := json.Marshal(ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Manifests: []ocispec.Descriptor{writeBlob(manifestData, ocispec.MediaTypeImageManifest)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), indexData, 0644); err != nil {
		t.Fatal(err)
	}
	return layout
}

func assertExtractedArtifactFiles(t *testing.T, dest string, layers []testArtifactLayer) {
	t.Helper()
	for _, layer := range layers {
		path := filepath.Join(dest, layer.title)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Errorf("%s is not a regular file", layer.title)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, layer.content) {
			t.Errorf("downloaded bytes differ for %s", layer.title)
		}
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(layers) {
		t.Fatal("unexpected files in the output directory")
	}
}

func TestExtractOCIArtifactBlobSingleFile(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("disk image contents")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name     string
		title    string
		dest     string
		existing string
		want     string
	}{
		{"existing directory", "disk.qcow2.gz", "output", "dir", "output/disk.qcow2.gz"},
		{"existing directory with trailing slash", "disk.qcow2.gz", "output/", "dir", "output/disk.qcow2.gz"},
		{"new directory with trailing slash", "disk.qcow2.gz", "output/", "", "output/disk.qcow2.gz"},
		{"new nested directory", "disk.qcow2.gz", "parent/output/", "", "parent/output/disk.qcow2.gz"},
		{"explicit filename", "disk.qcow2.gz", "renamed.gz", "", "renamed.gz"},
		{"overwrite explicit file", "disk.qcow2.gz", "renamed.gz", "file", "renamed.gz"},
		{"missing title", "", "output/", "", "output/layer-0.bin"},
		{"unsafe title", "../escape.gz", "output/", "", "output/layer-0.bin"},
		{"title with directory", "images/disk.qcow2.gz", "output/", "", "output/disk.qcow2.gz"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layout := writeTestOCIArtifact(t, nil, testArtifactLayer{tt.title, compressed.Bytes()})
			root := t.TempDir()
			dest := filepath.Join(root, tt.dest)
			if strings.HasSuffix(tt.dest, "/") {
				dest += string(os.PathSeparator)
			}
			switch tt.existing {
			case "dir":
				if err := os.MkdirAll(dest, 0755); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(dest, []byte("old contents"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := extractOCIArtifactBlob(layout, dest); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, tt.want))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, compressed.Bytes()) {
				t.Fatal("downloaded bytes differ from compressed artifact")
			}
		})
	}
}

func TestExtractOCIArtifactBlobMultiFile(t *testing.T) {
	layers := []testArtifactLayer{
		{"boot.img.gz", []byte("compressed boot partition")},
		{"root.img.gz", []byte("compressed root partition")},
	}
	for _, tt := range []struct {
		name        string
		annotations map[string]string
		layers      []testArtifactLayer
	}{
		{"multiple layers", nil, layers},
		{"annotated multiple layers", map[string]string{oci.Get().AnnotationKey("multi-layer"): "true"}, layers},
		{"annotated single layer", map[string]string{oci.Get().AnnotationKey("multi-layer"): "true"}, layers[:1]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layout := writeTestOCIArtifact(t, tt.annotations, tt.layers...)
			dest := filepath.Join(t.TempDir(), "output")
			if err := extractOCIArtifactBlob(layout, dest); err != nil {
				t.Fatal(err)
			}
			assertExtractedArtifactFiles(t, dest, tt.layers)
		})
	}
}

func TestExtractOCIArtifactBlobDestinationSymlink(t *testing.T) {
	layers := []testArtifactLayer{
		{"disk.qcow2.gz", []byte("compressed disk image")},
		{"boot.img.gz", []byte("compressed boot partition")},
	}
	for _, tt := range []struct {
		name   string
		layers []testArtifactLayer
	}{
		{"single file", layers[:1]},
		{"multiple files", layers},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "output")
			if err := os.Mkdir(dest, 0755); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside")
			original := []byte("unrelated file contents")
			if err := os.WriteFile(outside, original, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dest, layers[0].title)); err != nil {
				t.Fatal(err)
			}
			layout := writeTestOCIArtifact(t, nil, tt.layers...)
			if err := extractOCIArtifactBlob(layout, dest); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(outside)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Fatal("extraction overwrote the symlink target outside the output directory")
			}
			assertExtractedArtifactFiles(t, dest, tt.layers)
		})
	}
}

func TestCopyFileFailurePreservesDestination(t *testing.T) {
	for _, tt := range []struct {
		name           string
		sourceDir      bool
		destinationDir bool
		wantError      string
	}{
		{"copy failure", true, false, "copy data"},
		{"rename failure", false, true, "rename destination"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if tt.sourceDir {
				if err := os.Mkdir(source, 0755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(source, []byte("new contents"), 0644); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(root, "destination")
			marker := dest
			if tt.destinationDir {
				if err := os.Mkdir(dest, 0755); err != nil {
					t.Fatal(err)
				}
				marker = filepath.Join(dest, "original")
			}
			original := []byte("original contents")
			if err := os.WriteFile(marker, original, 0644); err != nil {
				t.Fatal(err)
			}
			if err := copyFile(source, dest); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("copyFile() error = %v, want %q", err, tt.wantError)
			}
			got, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Error("failed copy changed the original destination")
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Error("failed copy left temporary files in the destination directory")
			}
		})
	}
}
