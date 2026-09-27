package buildapi

import (
	"context"
	"fmt"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/labels"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateGitCredentials(ctx context.Context, c client.Client, namespace, requester string, source *api.GitSource) error {
	if source == nil || source.CredentialsSecretRef == "" {
		return nil
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: source.CredentialsSecretRef}, secret); err != nil {
		return fmt.Errorf("git credentials Secret is unavailable")
	}
	if requester == "" || secret.Annotations[labels.RequestedBy] != requester {
		return fmt.Errorf("git credentials Secret must have annotation %s matching the requesting user", labels.RequestedBy)
	}
	return api.ValidateGitCredentialsSecret(source, secret)
}
