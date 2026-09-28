package test

import (
	"context"

	api "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2" //nolint:revive // Dot import is standard for Ginkgo
	. "github.com/onsi/gomega"    //nolint:revive // Dot import is standard for Gomega
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ImageBuild Git source immutability", func() {
	ctx := context.Background()

	It("keeps the source fixed while allowing controller defaults", func() {
		build := &api.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "immutable-git-source", Namespace: "default"},
			Spec: api.ImageBuildSpec{AIB: &api.AIBSpec{
				Distro: "autosd", Target: "qemu",
				GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "images/os.aib.yml"},
			}},
		}
		Expect(k8sClient.Create(ctx, build)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, build)).To(Succeed()) })

		build.Spec.Architecture = "arm64"
		build.Spec.AIB.Target = "board"
		Expect(k8sClient.Update(ctx, build)).To(Succeed())

		for _, change := range []struct {
			name   string
			mutate func(*api.ImageBuild)
		}{
			{name: "manifest", mutate: func(b *api.ImageBuild) { b.Spec.AIB.GitSource.ManifestPath = "images/other.aib.yml" }},
			{name: "revision", mutate: func(b *api.ImageBuild) { b.Spec.AIB.GitSource.Revision = "main" }},
			{name: "source removal", mutate: func(b *api.ImageBuild) { b.Spec.AIB.GitSource = nil }},
			{name: "parent removal", mutate: func(b *api.ImageBuild) { b.Spec.AIB = nil }},
			{name: "spec removal", mutate: func(b *api.ImageBuild) { b.Spec = api.ImageBuildSpec{} }},
		} {
			By("rejecting " + change.name)
			changed := build.DeepCopy()
			change.mutate(changed)
			err := k8sClient.Update(ctx, changed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("gitSource is immutable"))
		}
	})

	It("rejects adding a Git source to an existing build", func() {
		build := &api.ImageBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "no-git-source", Namespace: "default"},
			Spec:       api.ImageBuildSpec{AIB: &api.AIBSpec{Distro: "autosd", Target: "qemu"}},
		}
		Expect(k8sClient.Create(ctx, build)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, build)).To(Succeed()) })

		build.Spec.AIB.GitSource = &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "os.aib.yml"}
		err := k8sClient.Update(ctx, build)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("gitSource is immutable"))
	})

	It("allows a schedule template to change its source for future builds", func() {
		schedule := &api.ScheduledImageBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "mutable-git-schedule", Namespace: "default"},
			Spec: api.ScheduledImageBuildSpec{
				Schedule: "0 0 * * *",
				ImageBuildTemplate: api.ImageBuildTemplateSpec{Spec: api.ImageBuildSpec{AIB: &api.AIBSpec{
					Distro: "autosd", Target: "qemu",
					GitSource: &api.GitSource{URL: "https://git.example.com/os.git", ManifestPath: "images/os.aib.yml"},
				}}},
			},
		}
		Expect(k8sClient.Create(ctx, schedule)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, schedule)).To(Succeed()) })

		schedule.Spec.ImageBuildTemplate.Spec.AIB.GitSource.ManifestPath = "images/next.aib.yml"
		Expect(k8sClient.Update(ctx, schedule)).To(Succeed())
	})
})
