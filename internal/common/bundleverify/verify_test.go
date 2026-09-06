package bundleverify

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestVerifyBundle_InvalidPEM(t *testing.T) {
	err := VerifyBundle(context.Background(), "quay.io/test/img@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []byte("not-a-pem"))
	if err == nil {
		t.Fatal("expected error for invalid PEM key")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestVerifyBundle_InvalidRef(t *testing.T) {
	// Valid ECDSA P-256 test key (not a real signing key)
	testPEM := []byte(`-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEY1WtPBgOWxlBCpCIuR7SXPJG1sXD
VmOYGDB0PCBPeJQyaK1FGKs06iDQL4DP6jMzqpNL3D5LkF8bOJCGhIFjQ==
-----END PUBLIC KEY-----`)

	err := VerifyBundle(context.Background(), ":::invalid-ref", testPEM)
	if err == nil {
		t.Fatal("expected error for invalid reference")
	}
}

func newFakeReader(objs ...client.Object) client.Reader {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestFetchCosignPublicKey_NilKeyRef(t *testing.T) {
	k := newFakeReader()
	_, err := FetchCosignPublicKey(context.Background(), k, nil, "default")
	if err == nil {
		t.Fatal("expected error for nil keyRef")
	}
	if got := err.Error(); got != "cosign key reference is not configured" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestFetchCosignPublicKey_EmptyName(t *testing.T) {
	k := newFakeReader()
	ref := &corev1.ConfigMapKeySelector{Key: "cosign.pub"}
	_, err := FetchCosignPublicKey(context.Background(), k, ref, "default")
	if err == nil {
		t.Fatal("expected error for empty ConfigMap name")
	}
}

func TestFetchCosignPublicKey_MissingConfigMap(t *testing.T) {
	k := newFakeReader()
	ref := &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "no-such-cm"},
		Key:                  "cosign.pub",
	}
	_, err := FetchCosignPublicKey(context.Background(), k, ref, "default")
	if err == nil {
		t.Fatal("expected error for missing ConfigMap")
	}
}

func TestFetchCosignPublicKey_MissingKey(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-key", Namespace: "default"},
		Data:       map[string]string{"wrong-key": "data"},
	}
	k := newFakeReader(cm)
	ref := &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "my-key"},
		Key:                  "cosign.pub",
	}
	_, err := FetchCosignPublicKey(context.Background(), k, ref, "default")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestFetchCosignPublicKey_EmptyPEM(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-key", Namespace: "default"},
		Data:       map[string]string{"cosign.pub": "   "},
	}
	k := newFakeReader(cm)
	ref := &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "my-key"},
		Key:                  "cosign.pub",
	}
	_, err := FetchCosignPublicKey(context.Background(), k, ref, "default")
	if err == nil {
		t.Fatal("expected error for empty PEM")
	}
	if got := err.Error(); got != `ConfigMap "my-key" key "cosign.pub" is empty` {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestFetchCosignPublicKey_Success(t *testing.T) {
	pem := "-----BEGIN PUBLIC KEY-----\ntest\n-----END PUBLIC KEY-----"
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-key", Namespace: "default"},
		Data:       map[string]string{"cosign.pub": pem},
	}
	k := newFakeReader(cm)
	ref := &corev1.ConfigMapKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "my-key"},
		Key:                  "cosign.pub",
	}
	got, err := FetchCosignPublicKey(context.Background(), k, ref, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != pem {
		t.Errorf("got %q, want %q", string(got), pem)
	}
}
