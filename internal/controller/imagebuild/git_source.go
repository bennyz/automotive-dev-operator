package imagebuild

import (
	"context"
	"fmt"
	"regexp"
	"time"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/labels"
	"github.com/centos-automotive-suite/automotive-dev-operator/internal/common/tasks"
	controllerutils "github.com/centos-automotive-suite/automotive-dev-operator/internal/controller/controllerutils"
	pod "github.com/tektoncd/pipeline/pkg/apis/pipeline/pod"
	tekton "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var gitCommitPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func (r *ImageBuildReconciler) prepareGitSource(ctx context.Context, ib *api.ImageBuild) (ctrl.Result, error) {
	fail := func(err error) (ctrl.Result, error) {
		return ctrl.Result{}, r.updateStatus(ctx, ib, phaseFailed, "Git source preparation failed: "+err.Error())
	}
	if err := api.ValidateGitSourceSpec(&ib.Spec); err != nil {
		return fail(err)
	}
	name := safeDerivedName(ib.Name, "-source")
	tr := &tekton.TaskRun{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: name}, tr)
	if errors.IsNotFound(err) {
		return r.startGitSource(ctx, ib, name)
	}

	if err != nil {
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(tr, ib) {
		return fail(fmt.Errorf("source TaskRun is not owned by this build"))
	}
	if ib.Status.SourceTaskRunName != name || ib.Status.PVCName == "" {
		pvc := ""
		for _, ws := range tr.Spec.Workspaces {
			if ws.Name == "source" && ws.PersistentVolumeClaim != nil {
				pvc = ws.PersistentVolumeClaim.ClaimName
			}
		}
		if pvc == "" {
			return fail(fmt.Errorf("source TaskRun has no source PVC"))
		}
		if err := r.updateStatus(ctx, ib, api.ImageBuildPhasePending, "Fetching Git source", func(fresh *api.ImageBuild) { fresh.Status.SourceTaskRunName = name; fresh.Status.PVCName = pvc }); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !tr.IsDone() {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if !tr.IsSuccessful() {
		return fail(fmt.Errorf("source TaskRun %s failed; inspect its logs", name))
	}
	commit, target := "", ""
	for _, result := range tr.Status.Results {
		switch result.Name {
		case "commit":
			commit = result.Value.StringVal
		case "target":
			target = result.Value.StringVal
		}
	}
	if !gitCommitPattern.MatchString(commit) || target == "" {
		return fail(fmt.Errorf("source TaskRun returned incomplete results"))
	}
	if err := r.applyGitTargetDefaults(ctx, ib, target); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateStatus(ctx, ib, phaseBuilding, "Git source prepared at "+commit, func(fresh *api.ImageBuild) { fresh.Status.SourceCommit = commit; fresh.Status.SourceTaskRunName = name }); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

func (r *ImageBuildReconciler) applyGitTargetDefaults(ctx context.Context, ib *api.ImageBuild, target string) error {
	fresh := &api.ImageBuild{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(ib), fresh); err != nil {
		return err
	}
	patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if fresh.Spec.AIB.Target == "" {
		fresh.Spec.AIB.Target = target
	}
	defaults := r.getTargetDefaults(ctx, fresh.Spec.AIB.Target)
	if fresh.Spec.Architecture == "" {
		fresh.Spec.Architecture = "arm64"
		if defaults != nil && defaults.Architecture != "" {
			fresh.Spec.Architecture = controllerutils.NormalizeArchToK8s(defaults.Architecture)
		}
	}
	if fresh.Labels == nil {
		fresh.Labels = map[string]string{}
	}
	fresh.Labels[labels.Target] = controllerutils.SanitizeLabelValue(fresh.Spec.GetTarget())
	fresh.Labels[labels.Architecture] = fresh.Spec.Architecture
	return r.Patch(ctx, fresh, patch)
}

func (r *ImageBuildReconciler) gitSourceConfig(ctx context.Context, ib *api.ImageBuild) (*tasks.BuildConfig, error) {
	config := r.resolveBuildConfig(ctx)
	if !ib.Spec.SecureBuild {
		return config, nil
	}
	op := &api.OperatorConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: "config", Namespace: controllerutils.OperatorNamespace()}, op); err != nil {
		return nil, err
	}
	if err := r.configureSecureTaskBundle(ctx, ib, op, config, false); err != nil {
		return nil, err
	}
	return config, nil
}

func (r *ImageBuildReconciler) startGitSource(ctx context.Context, ib *api.ImageBuild, name string) (ctrl.Result, error) {
	source := ib.Spec.GetGitSource()
	if source.CredentialsSecretRef != "" {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ib.Namespace, Name: source.CredentialsSecretRef}, secret); err != nil {
			if errors.IsNotFound(err) {
				return ctrl.Result{}, r.updateStatus(ctx, ib, phaseFailed, "Git credentials Secret is unavailable: "+err.Error())
			}
			return ctrl.Result{}, err
		}
		if err := api.ValidateGitCredentialsSecret(source, secret); err != nil {
			return ctrl.Result{}, r.updateStatus(ctx, ib, phaseFailed, err.Error())
		}
	}
	config, configErr := r.gitSourceConfig(ctx, ib)
	if configErr != nil {
		return ctrl.Result{}, r.updateStatus(ctx, ib, phaseFailed, "Git source preparation failed: "+configErr.Error())
	}
	pvc, pvcErr := r.getOrCreateWorkspacePVC(ctx, ib)
	if pvcErr != nil {
		return ctrl.Result{}, pvcErr
	}
	podTemplate, err := r.gitSourcePodTemplate(ctx, ib, config)
	if err != nil {
		return ctrl.Result{}, err
	}
	sourceTimeout := 10 * time.Minute
	if config != nil && config.BuildTimeoutMinutes > 0 {
		sourceTimeout = time.Duration(config.BuildTimeoutMinutes) * time.Minute
	}
	tr := &tekton.TaskRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ib.Namespace, Labels: buildLabels(ib, tasks.GitSourceTaskName)},
		Spec: tekton.TaskRunSpec{
			ServiceAccountName: api.BuildServiceAccountName,
			PodTemplate:        podTemplate,
			TaskRef:            tasks.GitSourceTaskRef(controllerutils.OperatorNamespace(), config),
			Timeout:            &metav1.Duration{Duration: sourceTimeout},
			Params: []tekton.Param{
				{Name: "url", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: source.URL}},
				{Name: "revision", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: source.Revision}},
				{Name: "manifest", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: source.ManifestPath}},
			},
			Workspaces: []tekton.WorkspaceBinding{{Name: "source", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc}}},
		},
	}
	if image := ib.Spec.GetAIBImage(); image != "" {
		tr.Spec.Params = append(tr.Spec.Params, tekton.Param{Name: "aib-image", Value: tekton.ParamValue{Type: tekton.ParamTypeString, StringVal: image}})
	}
	if source.CredentialsSecretRef != "" {
		tr.Spec.Workspaces = append(tr.Spec.Workspaces, tekton.WorkspaceBinding{Name: "git-auth", Secret: &corev1.SecretVolumeSource{SecretName: source.CredentialsSecretRef}})
	}
	if err := controllerutil.SetControllerReference(ib, tr, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateStatus(ctx, ib, api.ImageBuildPhasePending, "Fetching Git source", func(fresh *api.ImageBuild) { fresh.Status.SourceTaskRunName = name; fresh.Status.PVCName = pvc }); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, tr); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *ImageBuildReconciler) gitSourcePodTemplate(ctx context.Context, ib *api.ImageBuild, config *tasks.BuildConfig) (*pod.PodTemplate, error) {
	template := &pod.PodTemplate{}
	op := &api.OperatorConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: "config", Namespace: controllerutils.OperatorNamespace()}, op); err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("load source scheduling configuration: %w", err)
	}
	if op.Spec.OSBuilds != nil {
		template.NodeSelector = op.Spec.OSBuilds.NodeSelector
		template.Tolerations = op.Spec.OSBuilds.Tolerations
	}
	if config != nil && config.RuntimeClassName != "" {
		template.RuntimeClassName = &config.RuntimeClassName
	}
	if ib.Spec.RuntimeClassName != "" {
		template.RuntimeClassName = &ib.Spec.RuntimeClassName
	}
	if ib.Spec.Architecture != "" {
		template.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelArchStable, Operator: corev1.NodeSelectorOpIn, Values: []string{controllerutils.NormalizeArchToK8s(ib.Spec.Architecture)}}},
			}}},
		}}
	}
	return template, nil
}
