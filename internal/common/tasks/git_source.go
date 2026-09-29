package tasks

import (
	_ "embed"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	GitSourceTaskName          = "prepare-git-source"
	GitSourceDiscoveryTaskName = "discover-git-source"
)

//go:embed scripts/clone_git_source.sh
var CloneGitSourceScript string

//go:embed scripts/prepare_git_source.py
var PrepareGitSourceScript string

func GitSourceTaskRef(namespace string, config *BuildConfig) *tektonv1.TaskRef {
	return buildTaskRef(GitSourceTaskName, namespace, config)
}

func GitSourceDiscoveryTaskRef(namespace string, config *BuildConfig) *tektonv1.TaskRef {
	return buildTaskRef(GitSourceDiscoveryTaskName, namespace, config)
}

func GenerateGitSourceTask(namespace string, config *BuildConfig) *tektonv1.Task {
	return generateGitSourceTask(namespace, config, false)
}

func GenerateGitSourceDiscoveryTask(namespace string, config *BuildConfig) *tektonv1.Task {
	return generateGitSourceTask(namespace, config, true)
}

func generateGitSourceTask(namespace string, config *BuildConfig, discovery bool) *tektonv1.Task {
	gitImage := api.DefaultGitCloneImage
	if config != nil && config.GitCloneImage != "" {
		gitImage = config.GitCloneImage
	}
	name := GitSourceTaskName
	mode := "false"
	if discovery {
		name = GitSourceDiscoveryTaskName
		mode = "true"
	}
	return &tektonv1.Task{
		TypeMeta:   metav1.TypeMeta{APIVersion: "tekton.dev/v1", Kind: "Task"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "automotive-dev-operator"}},
		Spec: tektonv1.TaskSpec{
			Params: []tektonv1.ParamSpec{
				{Name: "url", Type: tektonv1.ParamTypeString},
				{Name: "revision", Type: tektonv1.ParamTypeString, Default: &tektonv1.ParamValue{Type: tektonv1.ParamTypeString}},
				{Name: "manifest", Type: tektonv1.ParamTypeString},
				{Name: "lockfile", Type: tektonv1.ParamTypeString, Default: &tektonv1.ParamValue{Type: tektonv1.ParamTypeString}},
				{Name: "commit", Type: tektonv1.ParamTypeString, Default: &tektonv1.ParamValue{Type: tektonv1.ParamTypeString}},
				{Name: "aib-image", Type: tektonv1.ParamTypeString, Default: &tektonv1.ParamValue{Type: tektonv1.ParamTypeString, StringVal: config.getAutomotiveImageBuilderImage()}},
			},
			Results:    []tektonv1.TaskResult{{Name: "commit", Type: tektonv1.ResultsTypeString}, {Name: "target", Type: tektonv1.ResultsTypeString}},
			Workspaces: []tektonv1.WorkspaceDeclaration{{Name: "source"}, {Name: "git-auth", Optional: true, MountPath: "/git-auth", ReadOnly: true}},
			Volumes:    []corev1.Volume{{Name: "source-ca", VolumeSource: trustedCABundleVolumeSource(config)}},
			Steps: []tektonv1.Step{
				{Name: "clone", Image: gitImage, Script: CloneGitSourceScript,
					VolumeMounts: []corev1.VolumeMount{{Name: "source-ca", MountPath: "/source-ca", ReadOnly: true}},
					Env:          []corev1.EnvVar{taskParamEnvVar("SOURCE_URL", "url"), taskParamEnvVar("SOURCE_REVISION", "revision"), taskParamEnvVar("SOURCE_MANIFEST", "manifest"), taskParamEnvVar("SOURCE_COMMIT", "commit"), {Name: "SOURCE_DISCOVERY", Value: mode}, {Name: "SOURCE_WORKSPACE", Value: "$(workspaces.source.path)"}},
				},
				{Name: "prepare", Image: "$(params.aib-image)", Script: PrepareGitSourceScript,
					Env: []corev1.EnvVar{taskParamEnvVar("SOURCE_URL", "url"), taskParamEnvVar("SOURCE_REVISION", "revision"), taskParamEnvVar("SOURCE_MANIFEST", "manifest"), taskParamEnvVar("SOURCE_LOCKFILE", "lockfile"), {Name: "SOURCE_DISCOVERY", Value: mode}, {Name: "SOURCE_WORKSPACE", Value: "$(workspaces.source.path)"}},
				},
			},
		},
	}
}
