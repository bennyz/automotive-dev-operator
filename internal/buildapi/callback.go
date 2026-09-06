package buildapi

import (
	"context"
	"encoding/base64"
	"fmt"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/labels"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/notifications"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func createCallbackSecret(
	ctx context.Context,
	k8sClient client.Client,
	namespace, subjectKind, subjectName string,
	subjectUID types.UID,
	callback *BuildCallback,
) (*corev1.Secret, error) {
	if callback == nil {
		return nil, nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(callback.Secret)
	if err != nil {
		return nil, fmt.Errorf("decode callback secret: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      notifications.CallbackSecretName(subjectName),
			Namespace: namespace,
			Labels: map[string]string{
				labels.ManagedBy:                  labels.ValueBuildAPI,
				labels.PartOf:                     labels.ValueAutomotiveDev,
				notifications.LabelCallbackSecret: labels.ValueTrue,
			},
			Annotations: map[string]string{
				notifications.AnnotationCallbackSubjectKind: subjectKind,
				notifications.AnnotationCallbackSubjectName: subjectName,
				notifications.AnnotationCallbackSubjectUID:  string(subjectUID),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			notifications.CallbackURLKey:  []byte(callback.URL),
			notifications.CallbackHMACKey: key,
		},
	}
	if subjectUID != "" {
		secret.OwnerReferences = []metav1.OwnerReference{callbackOwnerReference(subjectKind, subjectName, subjectUID)}
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("callback secret %q already exists", secret.Name)
		}
		return nil, fmt.Errorf("create callback secret: %w", err)
	}
	return secret, nil
}

func adoptCallbackSecret(
	ctx context.Context,
	k8sClient client.Client,
	secret *corev1.Secret,
	subjectKind, subjectName string,
	subjectUID types.UID,
) error {
	patch := client.MergeFromWithOptions(secret.DeepCopy(), client.MergeFromWithOptimisticLock{})
	secret.OwnerReferences = []metav1.OwnerReference{callbackOwnerReference(subjectKind, subjectName, subjectUID)}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[notifications.AnnotationCallbackSubjectUID] = string(subjectUID)
	return k8sClient.Patch(ctx, secret, patch)
}

func callbackOwnerReference(subjectKind, subjectName string, subjectUID types.UID) metav1.OwnerReference {
	controller := true
	blockDeletion := true
	apiVersion := automotivev1alpha1.GroupVersion.String()
	if subjectKind == notifications.SubjectTaskRun {
		apiVersion = "tekton.dev/v1"
	}
	return metav1.OwnerReference{
		APIVersion:         apiVersion,
		Kind:               subjectKind,
		Name:               subjectName,
		UID:                subjectUID,
		Controller:         &controller,
		BlockOwnerDeletion: &blockDeletion,
	}
}

func deleteCallbackSecret(ctx context.Context, k8sClient client.Client, secret *corev1.Secret) {
	if secret == nil {
		return
	}
	_ = client.IgnoreNotFound(k8sClient.Delete(ctx, secret))
}

func completeBuildCallbackInitialization(
	ctx context.Context,
	k8sClient client.Client,
	build *automotivev1alpha1.ImageBuild,
	callback *BuildCallback,
) error {
	if callback == nil {
		return nil
	}
	if _, err := createCallbackSecret(
		ctx,
		k8sClient,
		build.Namespace,
		notifications.SubjectImageBuild,
		build.Name,
		build.UID,
		callback,
	); err != nil {
		return err
	}
	patch := client.MergeFromWithOptions(build.DeepCopy(), client.MergeFromWithOptimisticLock{})
	delete(build.Annotations, notifications.AnnotationCallbackInitializing)
	if err := k8sClient.Patch(ctx, build, patch); err != nil {
		if k8serrors.IsConflict(err) {
			fresh := &automotivev1alpha1.ImageBuild{}
			if getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(build), fresh); getErr == nil &&
				fresh.Annotations[notifications.AnnotationCallbackInitializing] == "" {
				fresh.DeepCopyInto(build)
				return nil
			}
		}
		return fmt.Errorf("remove callback initialization marker: %w", err)
	}
	return nil
}
