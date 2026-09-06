package buildapi

import (
	"context"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Dot import is standard for Ginkgo
	. "github.com/onsi/gomega"    //nolint:revive // Dot import is standard for Gomega
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/labels"
)

var _ = Describe("Secrets", func() {

	Describe("setSecretOwnerRef", func() {
		It("sets owner reference on secret", func() {
			scheme := newRegistryTestScheme()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "ns",
				},
			}
			build := &automotivev1alpha1.ImageBuild{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-build",
					Namespace: "ns",
					UID:       "abc-123",
				},
			}
			k8sClient := newRegistryTestClient(scheme, secret, build)

			err := setSecretOwnerRef(context.Background(), k8sClient, "ns", "test-secret", build)
			Expect(err).NotTo(HaveOccurred())

			// Verify owner ref was set
			updated := &corev1.Secret{}
			Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "ns"}, updated)).To(Succeed())
			Expect(updated.OwnerReferences).To(HaveLen(1))
			Expect(updated.OwnerReferences[0].Name).To(Equal("my-build"))
		})

		It("returns error when secret does not exist", func() {
			scheme := newRegistryTestScheme()
			k8sClient := newRegistryTestClient(scheme)
			build := &automotivev1alpha1.ImageBuild{
				ObjectMeta: metav1.ObjectMeta{Name: "my-build", Namespace: "ns"},
			}

			err := setSecretOwnerRef(context.Background(), k8sClient, "ns", "missing-secret", build)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("createFlashClientSecret", func() {
		It("creates secret with decoded base64 config", func() {
			scheme := newRegistryTestScheme()
			k8sClient := newRegistryTestClient(scheme)

			// "hello world" in base64
			err := createFlashClientSecret(context.Background(), k8sClient, "ns", "flash-secret", "aGVsbG8gd29ybGQ=")
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: "flash-secret", Namespace: "ns"}, secret)).To(Succeed())
			Expect(secret.Data["client.yaml"]).To(Equal([]byte("hello world")))
			Expect(secret.Labels).To(HaveKeyWithValue(labels.Component, "jumpstarter-client"))
		})

		It("returns error for invalid base64", func() {
			scheme := newRegistryTestScheme()
			k8sClient := newRegistryTestClient(scheme)

			err := createFlashClientSecret(context.Background(), k8sClient, "ns", "bad-secret", "not-valid-base64!!!")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("decode"))
		})
	})
})
