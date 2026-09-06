// Package bundleverify provides cosign signature verification for OCI images.
package bundleverify

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	ociremote "github.com/sigstore/cosign/v3/pkg/oci/remote"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FetchCosignPublicKey reads a cosign public key (PEM-encoded) from a ConfigMap
// referenced by a ConfigMapKeySelector.
func FetchCosignPublicKey(ctx context.Context, k8sClient client.Reader, keyRef *corev1.ConfigMapKeySelector, namespace string) ([]byte, error) {
	if keyRef == nil || keyRef.Name == "" || keyRef.Key == "" {
		return nil, fmt.Errorf("cosign key reference is not configured")
	}
	cm := &corev1.ConfigMap{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: keyRef.Name, Namespace: namespace}, cm); err != nil {
		return nil, fmt.Errorf("failed to read cosign key ConfigMap %q: %w", keyRef.Name, err)
	}
	pubKeyPEM, ok := cm.Data[keyRef.Key]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %q does not contain key %q", keyRef.Name, keyRef.Key)
	}
	if strings.TrimSpace(pubKeyPEM) == "" {
		return nil, fmt.Errorf("ConfigMap %q key %q is empty", keyRef.Name, keyRef.Key)
	}
	return []byte(pubKeyPEM), nil
}

// VerifyBundle verifies the cosign signature of an OCI image reference using the
// given cosign public key (PEM-encoded). Optional ociremote.Option values are
// forwarded to cosign for registry authentication.
//
// Tries v3 bundle format (OCI referrers) first, falls back to legacy tag-based signatures.
func VerifyBundle(ctx context.Context, bundleRef string, cosignPubKeyPEM []byte, registryOpts ...ociremote.Option) error {
	pubKey, err := cryptoutils.UnmarshalPEMToPublicKey(cosignPubKeyPEM)
	if err != nil {
		return fmt.Errorf("parsing cosign public key: %w", err)
	}

	verifier, err := signature.LoadDefaultVerifier(pubKey)
	if err != nil {
		return fmt.Errorf("creating verifier: %w", err)
	}

	ref, err := name.ParseReference(bundleRef)
	if err != nil {
		return fmt.Errorf("parsing bundle reference %q: %w", bundleRef, err)
	}

	v3Err := verifyV3Bundles(ctx, ref, verifier, registryOpts)
	if v3Err == nil {
		return nil
	}

	if legacyErr := verifyLegacy(ctx, ref, verifier, registryOpts); legacyErr != nil {
		return fmt.Errorf("verification failed (v3: %v, legacy: %w)", v3Err, legacyErr)
	}
	return nil
}

// verifyV3Bundles fetches sigstore v3 bundles via OCI referrers and verifies with sigstore-go.
func verifyV3Bundles(ctx context.Context, ref name.Reference, verifier signature.Verifier, registryOpts []ociremote.Option) error {
	bundles, hash, err := cosign.GetBundles(ctx, ref, registryOpts)
	if err != nil {
		return fmt.Errorf("fetching v3 bundles: %w", err)
	}
	if len(bundles) == 0 {
		return fmt.Errorf("no v3 bundles found")
	}

	digestHex, err := hex.DecodeString(hash.Hex)
	if err != nil {
		return fmt.Errorf("decoding image digest hex: %w", err)
	}

	co := &cosign.CheckOpts{
		SigVerifier: verifier,
		IgnoreTlog:  true,
		IgnoreSCT:   true,
	}

	artifactDigest := verify.WithArtifactDigest(hash.Algorithm, digestHex)

	for _, bundle := range bundles {
		if _, err := cosign.VerifyNewBundle(ctx, co, artifactDigest, bundle); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no v3 bundle verified successfully for %q", ref.String())
}

// verifyLegacy uses the legacy tag-based cosign verification (v2 compat).
func verifyLegacy(ctx context.Context, ref name.Reference, verifier signature.Verifier, registryOpts []ociremote.Option) error {
	checkOpts := &cosign.CheckOpts{
		SigVerifier:        verifier,
		IgnoreTlog:         true,
		IgnoreSCT:          true,
		ExperimentalOCI11:  true,
		RegistryClientOpts: registryOpts,
	}

	_, _, err := cosign.VerifyImageSignatures(ctx, ref, checkOpts)
	if err != nil {
		return fmt.Errorf("cosign verification failed for %q: %w", ref.String(), err)
	}

	return nil
}
