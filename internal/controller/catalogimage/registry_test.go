package catalogimage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/oci"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
)

type helperSource struct {
	types.ImageSource
	manifests map[digest.Digest][]byte
	blobs     map[digest.Digest][]byte
}

func (s *helperSource) Reference() types.ImageReference {
	ref, _ := docker.ParseReference("//registry.example/image:test")
	return ref
}
func (s *helperSource) GetManifest(_ context.Context, instance *digest.Digest) ([]byte, string, error) {
	key := digest.Digest("")
	if instance != nil {
		key = *instance
	}
	b, ok := s.manifests[key]
	if !ok {
		return nil, "", fmt.Errorf("missing manifest %s", key)
	}
	return b, manifest.GuessMIMEType(b), nil
}
func (s *helperSource) GetBlob(_ context.Context, info types.BlobInfo, _ types.BlobInfoCache) (io.ReadCloser, int64, error) {
	b, ok := s.blobs[info.Digest]
	if !ok {
		return nil, 0, fmt.Errorf("missing blob %s", info.Digest)
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}
func TestReadBuilderImages(t *testing.T) {
	key := oci.Get().AnnotationKey("builder-image")
	encode := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	config := encode(map[string]any{"architecture": "arm64", "os": "linux", "config": map[string]any{"Labels": map[string]string{key: "registry/helper:legacy"}}, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{}}})
	cfgDigest := digest.FromBytes(config)
	schema2 := encode(map[string]any{"schemaVersion": 2, "mediaType": manifest.DockerV2Schema2MediaType, "config": map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "size": len(config), "digest": cfgDigest}, "layers": []any{}})
	annotated := encode(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "annotations": map[string]string{key: "registry/helper:pinned"}})
	noHelper := encode(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "annotations": map[string]string{key: ""}})
	diskManifest := map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": "application/vnd.automotive.disk.simg",
		"config":       map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "size": 2, "digest": digest.FromString("{}"), "data": "e30="},
	}
	diskWithoutHelper := encode(diskManifest)
	diskManifest["annotations"] = map[string]string{key: "registry/helper:disk"}
	diskWithHelper := encode(diskManifest)
	schemaDigest, annotatedDigest := digest.FromBytes(schema2), digest.FromBytes(annotated)
	index := encode(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{map[string]any{"digest": schemaDigest}, map[string]any{"digest": annotatedDigest}}})
	for _, tc := range []struct {
		name    string
		raw     []byte
		want    []string
		missing bool
	}{
		{name: "config label", raw: schema2, want: []string{"registry/helper:legacy"}},
		{name: "manifest annotation", raw: annotated, want: []string{"registry/helper:pinned"}},
		{name: "explicit empty annotation", raw: noHelper},
		{name: "legacy disk artifact without helper", raw: diskWithoutHelper},
		{name: "disk artifact with helper", raw: diskWithHelper, want: []string{"registry/helper:disk"}},
		{name: "every platform", raw: index, want: []string{"registry/helper:legacy", "registry/helper:pinned"}},
		{name: "inaccessible config is unresolved", raw: schema2, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &helperSource{manifests: map[digest.Digest][]byte{"": tc.raw, schemaDigest: schema2, annotatedDigest: annotated}, blobs: map[digest.Digest][]byte{cfgDigest: config}}
			if tc.missing {
				src.blobs = nil
			}
			rootDigest := digest.FromBytes(tc.raw)
			src.manifests[rootDigest] = tc.raw
			got, err := readBuilderImages(context.Background(), src, &types.SystemContext{}, &rootDigest, 0)
			if tc.missing {
				if err == nil {
					t.Fatal("silently accepted inaccessible config")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
