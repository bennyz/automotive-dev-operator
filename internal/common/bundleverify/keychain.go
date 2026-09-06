package bundleverify

import (
	"bytes"
	"context"
	"fmt"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	dockertypes "github.com/docker/cli/cli/config/types"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KeychainFromPullSecrets builds an authn.Keychain from Kubernetes image pull
// secrets. The returned keychain tries each secret in order, falling back to
// authn.DefaultKeychain if none match. Returns authn.DefaultKeychain when
// secrets is empty.
func KeychainFromPullSecrets(ctx context.Context, k8sClient client.Reader, namespace string, secrets []corev1.LocalObjectReference) (authn.Keychain, error) {
	if len(secrets) == 0 {
		return authn.DefaultKeychain, nil
	}

	keychains := make([]authn.Keychain, 0, len(secrets)+1)
	for _, ref := range secrets {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: namespace}, secret); err != nil {
			return nil, fmt.Errorf("reading pull secret %q: %w", ref.Name, err)
		}

		data, ok := secret.Data[corev1.DockerConfigJsonKey]
		if !ok {
			data, ok = secret.Data[corev1.DockerConfigKey]
			if !ok {
				continue
			}
		}

		cf, err := config.LoadFromReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing pull secret %q: %w", ref.Name, err)
		}

		keychains = append(keychains, &configFileKeychain{cf: cf})
	}

	keychains = append(keychains, authn.DefaultKeychain)
	return authn.NewMultiKeychain(keychains...), nil
}

type configFileKeychain struct {
	cf *configfile.ConfigFile
}

func (k *configFileKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	var cfg, empty dockertypes.AuthConfig
	var err error
	for _, key := range []string{target.String(), target.RegistryStr()} {
		if key == name.DefaultRegistry {
			key = "https://" + name.DefaultRegistry + "/v1/"
		}
		cfg, err = k.cf.GetAuthConfig(key)
		if err != nil {
			return nil, err
		}
		cfg.ServerAddress = ""
		if cfg != empty {
			break
		}
	}
	if cfg == empty {
		return authn.Anonymous, nil
	}
	return authn.FromConfig(authn.AuthConfig{
		Username:      cfg.Username,
		Password:      cfg.Password,
		Auth:          cfg.Auth,
		IdentityToken: cfg.IdentityToken,
		RegistryToken: cfg.RegistryToken,
	}), nil
}
