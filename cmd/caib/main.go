package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/apimachinery/pkg/util/wait"

	progressbar "github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	automotivev1 "github.com/rh-sdv-cloud-incubator/automotive-dev-operator/api/v1"
)

var (
	kubeconfig    string
	namespace     string
	imageBuildCfg string
	manifest      string
	buildName     string
	distro        string
	target        string
	architecture  string
	exportFormat  string
	mode          string
	osbuildImage  string
	storageClass  string
	outputDir     string
	timeout       int
	waitForBuild  bool
	download      bool
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	rootCmd := &cobra.Command{
		Use:   "caib",
		Short: "Cloud Automotive Image Builder",
	}

	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Create an ImageBuild resource",
		Run:   runBuild,
	}

	downloadCmd := &cobra.Command{
		Use:   "download",
		Short: "Download artifacts from a completed build",
		Run:   runDownload,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List existing ImageBuilds",
		Run:   runList,
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show details of a specific ImageBuild",
		Args:  cobra.ExactArgs(1),
		Run:   runShow,
	}

	if home := homedir.HomeDir(); home != "" {
		buildCmd.Flags().StringVar(&kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to the kubeconfig file")
		downloadCmd.Flags().StringVar(&kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to the kubeconfig file")
		listCmd.Flags().StringVar(&kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to the kubeconfig file")
		showCmd.Flags().StringVar(&kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to the kubeconfig file")
	} else {
		buildCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
		downloadCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
		listCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
		showCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file")
	}

	buildCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace to create the ImageBuild in")
	buildCmd.Flags().StringVar(&imageBuildCfg, "config", "", "path to ImageBuild YAML configuration file")
	buildCmd.Flags().StringVar(&manifest, "manifest", "", "path to manifest YAML file for the build")
	buildCmd.Flags().StringVar(&buildName, "name", "", "name for the ImageBuild")
	buildCmd.Flags().StringVar(&distro, "distro", "cs9", "distribution to build")
	buildCmd.Flags().StringVar(&target, "target", "qemu", "target platform")
	buildCmd.Flags().StringVar(&architecture, "arch", "arm64", "architecture (amd64, arm64)")
	buildCmd.Flags().StringVar(&exportFormat, "export-format", "image", "export format (image, qcow2)")
	buildCmd.Flags().StringVar(&mode, "mode", "image", "build mode")
	buildCmd.Flags().StringVar(&osbuildImage, "osbuild-image", "quay.io/centos-sig-automotive/automotive-osbuild:latest", "automotive osbuild image")
	buildCmd.Flags().StringVar(&storageClass, "storage-class", "", "storage class for build PVC")
	buildCmd.Flags().IntVar(&timeout, "timeout", 60, "timeout in minutes when waiting for build completion")
	buildCmd.Flags().BoolVarP(&waitForBuild, "wait", "w", false, "wait for the build to complete")
	buildCmd.Flags().BoolVarP(&download, "download", "d", false, "automatically download artifacts when build completes")

	downloadCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace where the ImageBuild exists")
	downloadCmd.Flags().StringVar(&buildName, "name", "", "name of the ImageBuild")
	downloadCmd.Flags().StringVar(&outputDir, "output-dir", "./output", "directory to save artifacts")
	downloadCmd.MarkFlagRequired("name")

	listCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace to list ImageBuilds from")

	showCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace of the ImageBuild")

	rootCmd.AddCommand(buildCmd, downloadCmd, listCmd, showCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runBuild(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	c, err := getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	if buildName == "" {
		fmt.Println("Error: --name flag is required")
		os.Exit(1)
	}

	existingIB := &automotivev1.ImageBuild{}
	err = c.Get(ctx, types.NamespacedName{Name: buildName, Namespace: namespace}, existingIB)
	if err == nil {
		fmt.Printf("Deleting existing ImageBuild %s\n", buildName)
		if err := c.Delete(ctx, existingIB); err != nil {
			fmt.Printf("Error deleting existing ImageBuild: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Waiting for ImageBuild %s to be deleted...\n", buildName)
		err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			err := c.Get(ctx, types.NamespacedName{Name: buildName, Namespace: namespace}, &automotivev1.ImageBuild{})
			return errors.IsNotFound(err), nil
		})
		if err != nil {
			fmt.Printf("Error waiting for ImageBuild deletion: %v\n", err)
			os.Exit(1)
		}
	} else if !errors.IsNotFound(err) {
		fmt.Printf("Error checking for existing ImageBuild: %v\n", err)
		os.Exit(1)
	}

	if manifest == "" {
		fmt.Println("Error: --manifest is required")
		os.Exit(1)
	}

	manifestData, err := os.ReadFile(manifest)
	if err != nil {
		fmt.Printf("Error reading manifest file: %v\n", err)
		os.Exit(1)
	}

	configMapName := fmt.Sprintf("%s-manifest-config", buildName)

	existingCM := &corev1.ConfigMap{}
	err = c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, existingCM)
	if err == nil {
		fmt.Printf("Deleting existing ConfigMap %s\n", configMapName)
		if err := c.Delete(ctx, existingCM); err != nil {
			fmt.Printf("Error deleting ConfigMap: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Waiting for ConfigMap %s to be deleted...\n", configMapName)
		err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			err := c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, &corev1.ConfigMap{})
			return errors.IsNotFound(err), nil
		})
		if err != nil {
			fmt.Printf("Error waiting for ConfigMap deletion: %v\n", err)
			os.Exit(1)
		}
	} else if !errors.IsNotFound(err) {
		fmt.Printf("Error checking for existing ConfigMap: %v\n", err)
		os.Exit(1)
	}

	fileName := filepath.Base(manifest)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			fileName: string(manifestData),
		},
	}

	fmt.Printf("Creating ConfigMap %s with manifest file %s\n", configMapName, fileName)
	if err := c.Create(ctx, configMap); err != nil {
		fmt.Printf("Error creating ConfigMap: %v\n", err)
		os.Exit(1)
	}

	localFileRefs := findLocalFileReferences(string(manifestData))
	hasLocalFiles := len(localFileRefs) > 0

	imageBuild := &automotivev1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildName,
			Namespace: namespace,
		},
		Spec: automotivev1.ImageBuildSpec{
			Distro:                 distro,
			Target:                 target,
			Architecture:           architecture,
			ExportFormat:           exportFormat,
			Mode:                   mode,
			AutomativeOSBuildImage: osbuildImage,
			StorageClass:           storageClass,
			ServeArtifact:          waitForBuild && download,
			ServeExpiryHours:       24,
			ManifestConfigMap:      configMapName,
			InputFilesServer:       hasLocalFiles,
		},
	}

	if download {
		imageBuild.Spec.ServeArtifact = true
	}

	fmt.Printf("Creating ImageBuild %s\n", imageBuild.Name)
	if err := c.Create(ctx, imageBuild); err != nil {
		fmt.Printf("Error creating ImageBuild: %v\n", err)
		os.Exit(1)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, configMap); err != nil {
		fmt.Printf("Error retrieving ConfigMap for owner update: %v\n", err)
		os.Exit(1)
	}

	configMap.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion:         "automotive.sdv.cloud.redhat.com/v1",
			Kind:               "ImageBuild",
			Name:               imageBuild.Name,
			UID:                imageBuild.UID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		},
	}

	if err := c.Update(ctx, configMap); err != nil {
		fmt.Printf("Warning: Failed to update ConfigMap with owner reference: %v\n", err)
	}

	if hasLocalFiles {
		localFileRefs := findLocalFileReferences(string(manifestData))
		if len(localFileRefs) > 0 {
			uploadPod, err := waitForUploadPod(ctx, c, namespace, imageBuild.Name)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			fmt.Println("Found local file references in manifest.")
			fmt.Println("Uploading local files to artifact server...")

			if err := uploadLocalFiles(namespace, localFileRefs, uploadPod); err != nil {
				fmt.Printf("Error uploading files: %v\n", err)
				os.Exit(1)
			}

			fmt.Println("Files uploaded successfully.")
			if err := markUploadsComplete(ctx, c, namespace, imageBuild.Name); err != nil {
				fmt.Printf("Error marking uploads as complete: %v\n", err)
				os.Exit(1)
			}
		}
	}

	fmt.Printf("ImageBuild %s created successfully in namespace %s\n", imageBuild.Name, namespace)

	if waitForBuild {
		fmt.Printf("Waiting for build %s to complete (timeout: %d minutes)...\n", imageBuild.Name, timeout)
		var completeBuild *automotivev1.ImageBuild
		if completeBuild, err = waitForBuildCompletion(c, imageBuild.Name, namespace, timeout); err != nil {
			fmt.Printf("Error waiting for build: %v\n", err)
			os.Exit(1)
		}

		if download && completeBuild.Status.Phase == "Completed" {
			downloadArtifacts(completeBuild)
		}
	}
}

func findLocalFileReferences(manifestContent string) []map[string]string {
	var manifestData map[string]any
	var localFiles []map[string]string

	if err := yaml.Unmarshal([]byte(manifestContent), &manifestData); err != nil {
		fmt.Printf("failed to parse manifest YAML: %v\n", err)
		return localFiles
	}

	processAddFiles := func(addFiles []any) {
		for _, file := range addFiles {
			if fileMap, ok := file.(map[string]any); ok {
				path, hasPath := fileMap["path"].(string)
				sourcePath, hasSourcePath := fileMap["source_path"].(string)
				if hasPath && hasSourcePath && sourcePath != "" && sourcePath != "/" {
					localFiles = append(localFiles, map[string]string{
						"path":        path,
						"source_path": sourcePath,
					})
				}
			}
		}
	}

	if content, ok := manifestData["content"].(map[string]any); ok {
		if addFiles, ok := content["add_files"].([]any); ok {
			processAddFiles(addFiles)
		}
	}

	if qm, ok := manifestData["qm"].(map[string]any); ok {
		if qmContent, ok := qm["content"].(map[string]any); ok {
			if addFiles, ok := qmContent["add_files"].([]any); ok {
				processAddFiles(addFiles)
			}
		}
	}

	return localFiles
}

func uploadLocalFiles(namespace string, files []map[string]string, uploadPod *corev1.Pod) error {
	config, err := getRESTConfig()
	if err != nil {
		return fmt.Errorf("unable to get REST config: %w", err)
	}

	fmt.Printf("uploading %d files to build pod\n", len(files))

	for _, fileRef := range files {
		sourcePath := fileRef["source_path"]
		destPath := fileRef["source_path"]

		destDir := filepath.Dir(destPath)
		if destDir != "." && destDir != "/" {
			mkdirCmd := []string{"/bin/sh", "-c", fmt.Sprintf("mkdir -p /workspace/shared/%s", destDir)}
			if err := execInPod(config, namespace, uploadPod.Name, uploadPod.Spec.Containers[0].Name, mkdirCmd); err != nil {
				return fmt.Errorf("error creating directory structure: %w", err)
			}
		}

		fmt.Printf("Copying %s to pod:/workspace/shared/%s\n", sourcePath, destPath)
		if err := copyFile(config, namespace, uploadPod.Name, uploadPod.Spec.Containers[0].Name, sourcePath, "/workspace/shared/"+destPath, true); err != nil {
			return fmt.Errorf("error copying file %s: %w", sourcePath, err)
		}

	}

	return nil
}

func copyFile(config *rest.Config, namespace, podName, containerName, localPath, podPath string, toPod bool) error {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	if toPod {
		tarBuffer := new(bytes.Buffer)
		tarWriter := tar.NewWriter(tarBuffer)

		file, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("error opening local file: %w", err)
		}
		defer file.Close()

		stat, err := file.Stat()
		if err != nil {
			return fmt.Errorf("error getting file stats: %w", err)
		}

		header := &tar.Header{
			Name:    filepath.Base(podPath),
			Size:    stat.Size(),
			Mode:    int64(stat.Mode()),
			ModTime: stat.ModTime(),
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("error writing tar header: %w", err)
		}

		bar := progressbar.DefaultBytes(
			stat.Size(),
			"Uploading",
		)

		if _, err := io.Copy(io.MultiWriter(tarWriter, bar), file); err != nil {
			return fmt.Errorf("error copying file data to tar: %w", err)
		}

		if err := tarWriter.Close(); err != nil {
			return fmt.Errorf("error closing tar writer: %w", err)
		}

		destDir := filepath.Dir(podPath)
		mkdirCmd := []string{"mkdir", "-p", destDir}
		if err := execInPod(config, namespace, podName, containerName, mkdirCmd); err != nil {
			return fmt.Errorf("error creating destination directory: %w", err)
		}

		req := clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Name(podName).
			Namespace(namespace).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: containerName,
				Command:   []string{"tar", "-xf", "-", "-C", filepath.Dir(podPath)},
				Stdin:     true,
				Stdout:    true,
				Stderr:    true,
			}, scheme.ParameterCodec)

		exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
		if err != nil {
			return fmt.Errorf("error creating SPDY executor: %w", err)
		}

		var stdout, stderr bytes.Buffer
		err = exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
			Stdin:  bytes.NewReader(tarBuffer.Bytes()),
			Stdout: &stdout,
			Stderr: &stderr,
		})

		if err != nil {
			return fmt.Errorf("exec error: %v, stderr: %s", err, stderr.String())
		}
	} else {
		sizeCmd := []string{"stat", "-c", "%s", podPath}
		req := clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Name(podName).
			Namespace(namespace).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: containerName,
				Command:   sizeCmd,
				Stdout:    true,
				Stderr:    true,
			}, scheme.ParameterCodec)

		config.Timeout = 30 * time.Minute
		config.Transport = &http.Transport{
			IdleConnTimeout:    30 * time.Minute,
			DisableCompression: false,
			DisableKeepAlives:  false,
		}

		exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
		if err != nil {
			return fmt.Errorf("error creating SPDY executor: %w", err)
		}

		var sizeStdout, sizeStderr bytes.Buffer
		err = exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
			Stdout: &sizeStdout,
			Stderr: &sizeStderr,
		})

		if err != nil {
			return fmt.Errorf("error checking file: %v, stderr: %s", err, sizeStderr.String())
		}

		fileSize, err := strconv.ParseInt(strings.TrimSpace(sizeStdout.String()), 10, 64)
		if err != nil {
			return fmt.Errorf("error parsing file size: %w", err)
		}

		outFile, err := os.Create(localPath)
		if err != nil {
			return fmt.Errorf("error creating local file: %w", err)
		}
		defer outFile.Close()

		bar := progressbar.DefaultBytes(
			fileSize,
			"Downloading",
		)

		writer := io.MultiWriter(outFile, bar)

		req = clientset.CoreV1().RESTClient().Post().
			Resource("pods").
			Name(podName).
			Namespace(namespace).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: containerName,
				Command:   []string{"cat", podPath},
				Stdout:    true,
				Stderr:    true,
			}, scheme.ParameterCodec)

		exec, err = remotecommand.NewSPDYExecutor(config, "POST", req.URL())
		if err != nil {
			return fmt.Errorf("error creating SPDY executor: %w", err)
		}

		var stderr bytes.Buffer
		err = exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
			Stdout: writer,
			Stderr: &stderr,
		})

		if err != nil {
			return fmt.Errorf("exec error during download: %v, stderr: %s", err, stderr.String())
		}

		if info, err := outFile.Stat(); err == nil {
			if info.Size() != fileSize {
				os.Remove(localPath)
				return fmt.Errorf("incomplete download: got %d bytes, expected %d bytes",
					info.Size(), fileSize)
			}
		}
	}

	fmt.Println()
	return nil
}

func downloadArtifacts(imageBuild *automotivev1.ImageBuild) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("Error creating output directory: %v\n", err)
		return
	}

	artifactFileName := imageBuild.Status.ArtifactFileName
	if artifactFileName == "" {
		var fileExtension string
		if imageBuild.Spec.ExportFormat == "image" {
			fileExtension = ".raw"
		} else if imageBuild.Spec.ExportFormat == "qcow2" {
			fileExtension = ".qcow2"
		} else {
			fileExtension = fmt.Sprintf(".%s", imageBuild.Spec.ExportFormat)
		}

		artifactFileName = fmt.Sprintf("%s-%s%s",
			imageBuild.Spec.Distro,
			imageBuild.Spec.Target,
			fileExtension)
	}

	ctx := context.Background()

	c, err := getClient()
	if err != nil {
		fmt.Printf("Error creating client: %v\n", err)
		return
	}

	podList := &corev1.PodList{}
	if err := c.List(ctx, podList,
		client.InNamespace(imageBuild.Namespace),
		client.MatchingLabels{
			"automotive.sdv.cloud.redhat.com/imagebuild-name": imageBuild.Name,
			"app.kubernetes.io/name":                          "artifact-pod",
		}); err != nil {
		fmt.Printf("Error listing pods: %v\n", err)
		return
	}

	if len(podList.Items) == 0 {
		fmt.Println("No artifact pod found. Cannot download artifacts.")
		return
	}

	artifactPod := &podList.Items[0]
	containerName := "fileserver"

	sourcePath := "/workspace/shared/" + artifactFileName
	outputPath := filepath.Join(outputDir, artifactFileName)

	fmt.Printf("Downloading artifact from pod %s\n", artifactPod.Name)
	fmt.Printf("Pod path: %s\n", sourcePath)
	fmt.Printf("Saving to: %s\n", outputPath)

	config, err := getRESTConfig()
	if err != nil {
		fmt.Printf("Error getting REST config: %v\n", err)
		return
	}

	if err := copyFile(config, imageBuild.Namespace, artifactPod.Name, containerName, outputPath, sourcePath, false); err != nil {
		fmt.Printf("Error downloading artifact: %v\n", err)
		return
	}

	if fileInfo, err := os.Stat(outputPath); err == nil {
		fileSizeMB := float64(fileInfo.Size()) / 1024 / 1024
		fmt.Printf("Artifact downloaded successfully to %s (%.2f MB)\n", outputPath, fileSizeMB)
	} else {
		fmt.Printf("Artifact downloaded but unable to get file size: %v\n", err)
	}
}

func getRESTConfig() (*rest.Config, error) {
	var config *rest.Config
	var err error

	config, err = rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("error building config: %w", err)
		}
	}
	return config, nil
}

func waitForBuildCompletion(c client.Client, name, namespace string, timeoutMinutes int) (*automotivev1.ImageBuild, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMinutes)*time.Minute)
	defer cancel()

	var completedBuild *automotivev1.ImageBuild
	var lastPhase, lastMessage string

	err := wait.PollUntilContextTimeout(
		ctx,
		10*time.Second,
		time.Duration(timeoutMinutes)*time.Minute,
		false,
		func(ctx context.Context) (bool, error) {
			imageBuild := &automotivev1.ImageBuild{}
			if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, imageBuild); err != nil {
				return false, err
			}

			completedBuild = imageBuild

			if imageBuild.Status.Phase == "Completed" {
				if imageBuild.Status.Phase != lastPhase || imageBuild.Status.Message != lastMessage {
					fmt.Printf("\nstatus: %s - %s\n", imageBuild.Status.Phase, imageBuild.Status.Message)
				}
				return true, nil
			}

			if imageBuild.Status.Phase == "Failed" {
				fmt.Println()
				return false, fmt.Errorf("build failed: %s", imageBuild.Status.Message)
			}

			if imageBuild.Status.Phase != lastPhase || imageBuild.Status.Message != lastMessage {
				fmt.Printf("\nstatus: %s - %s\n", imageBuild.Status.Phase, imageBuild.Status.Message)
				lastPhase = imageBuild.Status.Phase
				lastMessage = imageBuild.Status.Message
			} else {
				fmt.Print(".")
			}

			return false, nil
		})

	fmt.Println()
	return completedBuild, err
}

func runDownload(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	c, err := getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	imageBuild := &automotivev1.ImageBuild{}
	if err := c.Get(ctx, types.NamespacedName{Name: buildName, Namespace: namespace}, imageBuild); err != nil {
		fmt.Printf("Error getting ImageBuild %s: %v\n", buildName, err)
		os.Exit(1)
	}

	if imageBuild.Status.Phase != "Completed" {
		fmt.Printf("Build %s is not completed (status: %s). Cannot download artifacts.\n", buildName, imageBuild.Status.Phase)
		os.Exit(1)
	}

	downloadArtifacts(imageBuild)
}

func runList(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	c, err := getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	builds := &automotivev1.ImageBuildList{}
	if err := c.List(ctx, builds, client.InNamespace(namespace)); err != nil {
		fmt.Printf("Error listing ImageBuilds: %v\n", err)
		os.Exit(1)
	}

	if len(builds.Items) == 0 {
		fmt.Printf("No ImageBuilds found in namespace %s\n", namespace)
		return
	}

	fmt.Printf("%-20s %-12s %-20s %-20s %-10s\n", "NAME", "STATUS", "DISTRO", "TARGET", "CREATED")
	for _, build := range builds.Items {
		createdTime := build.CreationTimestamp.Format("2006-01-02 15:04")
		fmt.Printf("%-20s %-12s %-20s %-20s %-10s\n",
			build.Name,
			build.Status.Phase,
			build.Spec.Distro,
			build.Spec.Target,
			createdTime)
	}
}

func runShow(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	c, err := getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	buildName := args[0]

	build := &automotivev1.ImageBuild{}
	if err := c.Get(ctx, types.NamespacedName{Name: buildName, Namespace: namespace}, build); err != nil {
		fmt.Printf("Error getting ImageBuild %s: %v\n", buildName, err)
		os.Exit(1)
	}

	fmt.Printf("Name:        %s\n", build.Name)
	fmt.Printf("Namespace:   %s\n", build.Namespace)
	fmt.Printf("Created:     %s\n", build.CreationTimestamp.Format(time.RFC3339))
	fmt.Printf("Status:      %s\n", build.Status.Phase)
	fmt.Printf("Message:     %s\n", build.Status.Message)

	fmt.Printf("\nBuild Specification:\n")
	fmt.Printf("  Distro:             %s\n", build.Spec.Distro)
	fmt.Printf("  Target:             %s\n", build.Spec.Target)
	fmt.Printf("  Architecture:       %s\n", build.Spec.Architecture)
	fmt.Printf("  Export Format:      %s\n", build.Spec.ExportFormat)
	fmt.Printf("  Mode:               %s\n", build.Spec.Mode)
	fmt.Printf("  Manifest ConfigMap:      %s\n", build.Spec.ManifestConfigMap)
	fmt.Printf("  OSBuild Image:      %s\n", build.Spec.AutomativeOSBuildImage)
	fmt.Printf("  Storage Class:      %s\n", build.Spec.StorageClass)
	fmt.Printf("  Serve Artifact:     %v\n", build.Spec.ServeArtifact)
	fmt.Printf("  Serve Expiry Hours: %d\n", build.Spec.ServeExpiryHours)

	if build.Status.Phase == "Completed" {
		fmt.Printf("\nArtifacts:\n")
		fmt.Printf("  PVC Name:       %s\n", build.Status.PVCName)
		fmt.Printf("  Artifact Path:  %s\n", build.Status.ArtifactPath)
		fmt.Printf("  File Name:      %s\n", build.Status.ArtifactFileName)
	}

}

func getClient() (client.Client, error) {
	ctrl.SetLogger(logr.Discard())

	var config *rest.Config
	var err error

	config, err = rest.InClusterConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("error building config: %w", err)
		}
	}

	scheme := runtime.NewScheme()
	if err := automotivev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding automotive scheme: %w", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("error adding core scheme: %w", err)
	}

	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("error creating Kubernetes client: %w", err)
	}

	return c, nil
}

func execInPod(config *rest.Config, namespace, podName, containerName string, command []string) error {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("error creating SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
	})

	if err != nil {
		return fmt.Errorf("exec error: %v, stderr: %s", err, stderr.String())
	}

	return nil
}

func markUploadsComplete(ctx context.Context, c client.Client, namespace, buildName string) error {
	original := &automotivev1.ImageBuild{}
	if err := c.Get(ctx, types.NamespacedName{Name: buildName, Namespace: namespace}, original); err != nil {
		return fmt.Errorf("error getting ImageBuild: %w", err)
	}

	patched := original.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = make(map[string]string)
	}
	patched.Annotations["automotive.sdv.cloud.redhat.com/uploads-complete"] = "true"

	if err := c.Patch(ctx, patched, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("error patching ImageBuild with completion annotation: %w", err)
	}

	fmt.Println("File uploads marked as complete. Build will proceed.")
	return nil
}

func waitForUploadPod(ctx context.Context, c client.Client, namespace, buildName string) (*corev1.Pod, error) {
	fmt.Println("Waiting for file upload server to be ready...")

	var uploadPod *corev1.Pod
	err := wait.PollUntilContextTimeout(
		ctx,
		5*time.Second,
		2*time.Minute,
		false,
		func(ctx context.Context) (bool, error) {
			podList := &corev1.PodList{}
			if err := c.List(ctx, podList,
				client.InNamespace(namespace),
				client.MatchingLabels{
					"automotive.sdv.cloud.redhat.com/imagebuild-name": buildName,
					"app.kubernetes.io/name":                          "upload-pod",
				}); err != nil {
				return false, err
			}

			for _, pod := range podList.Items {
				if pod.Status.Phase == corev1.PodRunning {
					uploadPod = &pod
					return true, nil
				}
			}
			fmt.Print(".")
			return false, nil
		})

	if err != nil {
		return nil, fmt.Errorf("timed out waiting for upload pod: %w", err)
	}

	fmt.Printf("\nUpload pod is ready: %s\n", uploadPod.Name)
	return uploadPod, nil
}
