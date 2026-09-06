package buildapi

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/notifications"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func callbackTestBuild() *automotivev1alpha1.ImageBuild {
	return &automotivev1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "callback-build", Namespace: "test-ns", UID: types.UID("build-uid"),
			Annotations: map[string]string{notifications.AnnotationCallbackInitializing: "true"},
		},
		Spec: automotivev1alpha1.ImageBuildSpec{
			ExternalID:        "external-id",
			CallbackSecretRef: notifications.CallbackSecretName("callback-build"),
		},
	}
}

func callbackTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := automotivev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func callbackTestValue() *BuildCallback {
	return &BuildCallback{
		URL:    "https://receiver.example/hook",
		Secret: base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
	}
}

func TestCompleteBuildCallbackInitialization(t *testing.T) {
	ctx := context.Background()
	t.Run("persists credentials and removes marker", func(t *testing.T) {
		build := callbackTestBuild()
		k8sClient := fake.NewClientBuilder().WithScheme(callbackTestScheme(t)).WithObjects(build).Build()
		if err := completeBuildCallbackInitialization(ctx, k8sClient, build, callbackTestValue()); err != nil {
			t.Fatal(err)
		}
		storedBuild := &automotivev1alpha1.ImageBuild{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(build), storedBuild); err != nil {
			t.Fatal(err)
		}
		if storedBuild.Annotations[notifications.AnnotationCallbackInitializing] != "" {
			t.Fatal("initialization marker remains")
		}
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: build.Namespace,
			Name:      build.Spec.CallbackSecretRef,
		}, secret); err != nil {
			t.Fatal(err)
		}
		if string(secret.Data[notifications.CallbackURLKey]) != callbackTestValue().URL ||
			string(secret.Data[notifications.CallbackHMACKey]) != "01234567890123456789012345678901" ||
			!metav1.IsControlledBy(secret, build) {
			t.Fatalf("unexpected callback secret: %+v", secret)
		}
	})
	t.Run("secret write failure leaves initialization recoverable", func(t *testing.T) {
		build := callbackTestBuild()
		k8sClient := fake.NewClientBuilder().
			WithScheme(callbackTestScheme(t)).
			WithObjects(build).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, underlying client.WithWatch, object client.Object, options ...client.CreateOption) error {
					if _, ok := object.(*corev1.Secret); ok {
						return errors.New("injected secret failure")
					}
					return underlying.Create(ctx, object, options...)
				},
			}).Build()
		if err := completeBuildCallbackInitialization(ctx, k8sClient, build, callbackTestValue()); err == nil {
			t.Fatal("expected secret failure")
		}
		storedBuild := &automotivev1alpha1.ImageBuild{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(build), storedBuild); err != nil {
			t.Fatal(err)
		}
		if storedBuild.Annotations[notifications.AnnotationCallbackInitializing] != "true" {
			t.Fatal("marker was removed after secret failure")
		}
	})
	t.Run("marker write failure leaves owned credentials recoverable", func(t *testing.T) {
		build := callbackTestBuild()
		k8sClient := fake.NewClientBuilder().
			WithScheme(callbackTestScheme(t)).
			WithObjects(build).
			WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
					if _, ok := object.(*automotivev1alpha1.ImageBuild); ok {
						return errors.New("injected marker failure")
					}
					return underlying.Patch(ctx, object, patch, options...)
				},
			}).Build()
		if err := completeBuildCallbackInitialization(ctx, k8sClient, build, callbackTestValue()); err == nil {
			t.Fatal("expected marker failure")
		}
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: build.Namespace,
			Name:      build.Spec.CallbackSecretRef,
		}, secret); err != nil {
			t.Fatal(err)
		}
		if !metav1.IsControlledBy(secret, build) {
			t.Fatal("callback secret cannot be recovered")
		}
		storedBuild := &automotivev1alpha1.ImageBuild{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(build), storedBuild); err != nil {
			t.Fatal(err)
		}
		if storedBuild.Annotations[notifications.AnnotationCallbackInitializing] != "true" {
			t.Fatal("stored marker was removed")
		}
	})
}
