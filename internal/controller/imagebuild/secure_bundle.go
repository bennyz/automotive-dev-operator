package imagebuild

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/bundleverify"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/tasks"
	controllerutils "github.com/centos-automotive-suite/automotive-dev-operator/internal/controller/controllerutils"
	corev1 "k8s.io/api/core/v1"
)

// configureSecureTaskBundle applies the same bundle checks to the source TaskRun
// and the build PipelineRun. Published bundles contain only default task images.
func (r *ImageBuildReconciler) configureSecureTaskBundle(ctx context.Context, ib *api.ImageBuild, op *api.OperatorConfig, config *tasks.BuildConfig, warn bool) error {
	if op.Spec.OSBuilds == nil {
		return fmt.Errorf("secureBuild requested but OperatorConfig.spec.osBuilds is unavailable: %w", errTerminalConfig)
	}
	ref := strings.TrimSpace(ib.Spec.TaskBundleRef)
	if ref == "" {
		return fmt.Errorf("secureBuild requested but taskBundleRef is not set on the ImageBuild: %w", errTerminalConfig)
	}
	if !digestPinnedRef.MatchString(ref) {
		return fmt.Errorf("secureBuild requires a digest-pinned taskBundleRef (must match image@sha256:<64 hex>), got %q: %w", ref, errTerminalConfig)
	}
	if op.Spec.OSBuilds.TaskBundleVerify {
		if _, ok := r.verifiedBundles.Load(ref); !ok {
			verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			key, err := bundleverify.FetchCosignPublicKey(verifyCtx, r.Client, op.Spec.OSBuilds.TaskBundleCosignKeyRef, controllerutils.OperatorNamespace())
			if err != nil {
				return fmt.Errorf("secureBuild: cosign key is unavailable: %w: %w", err, errTerminalConfig)
			}
			if err := bundleverify.VerifyBundle(verifyCtx, ref, key); err != nil {
				return fmt.Errorf("task bundle signature verification failed: %w: %w", err, errTerminalConfig)
			}
			r.verifiedBundles.Store(ref, struct{}{})
		}
	}
	if config.TrustedCABundleName != "" && config.TrustedCABundleName != tasks.DefaultTrustedCABundleConfigMap {
		return fmt.Errorf("secureBuild: custom CA bundle %q is not in the published task bundle: %w", config.TrustedCABundleName, errTerminalConfig)
	}
	if config.TrustedCABundleKind != "" && !strings.EqualFold(config.TrustedCABundleKind, "ConfigMap") {
		return fmt.Errorf("secureBuild: CA bundle kind %q is not in the published task bundle: %w", config.TrustedCABundleKind, errTerminalConfig)
	}
	if ib.Spec.GetGitSource() != nil && config.GitCloneImage != "" && config.GitCloneImage != api.DefaultGitCloneImage {
		return fmt.Errorf("secureBuild: custom Git clone image %q is not in the published task bundle: %w", config.GitCloneImage, errTerminalConfig)
	}
	config.TaskResolver = tasks.TaskResolverBundle
	config.TaskBundleRef = ref
	if warn {
		if config.UseMemoryVolumes {
			r.emitEventf(ib, corev1.EventTypeWarning, "SecureBuildConfigDrift", "OperatorConfig.useMemoryVolumes is enabled but bundle tasks use disk-backed emptyDir")
		}
		if config.UsePVCScratchVolumes {
			r.emitEventf(ib, corev1.EventTypeWarning, "SecureBuildConfigDrift", "OperatorConfig.usePVCScratchVolumes is enabled but bundle tasks use emptyDir")
		}
		if config.UseOCIVolumes {
			r.emitEventf(ib, corev1.EventTypeWarning, "SecureBuildConfigDrift", "OperatorConfig has OCIVolumes enabled but bundle tasks do not include OCI volume mounts")
		}
	}
	return nil
}
