/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operatorconfig

import (
	"context"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var _ = Describe("metrics reader ClusterRoleBinding reconciliation", func() {
	DescribeTable("should reconcile existing bindings",
		func(mutate func(*rbacv1.ClusterRoleBinding), expectedDeletes, expectedCreates, expectedUpdates int, unmanaged bool) {
			ctx := context.Background()
			r := &OperatorConfigReconciler{Log: logr.Discard()}
			desired := r.buildMetricsReaderClusterRoleBinding("test-ns")
			existing := desired.DeepCopy()
			mutate(existing)
			existing.UID = "original-binding"

			scheme := runtime.NewScheme()
			Expect(rbacv1.AddToScheme(scheme)).To(Succeed())
			var deletes, creates, updates int
			r.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						deletes++
						Expect(obj.GetUID()).To(Equal(existing.UID))
						return c.Delete(ctx, obj, opts...)
					},
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						creates++
						Expect(obj.GetResourceVersion()).To(BeEmpty())
						return c.Create(ctx, obj, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						updates++
						Expect(obj.(*rbacv1.ClusterRoleBinding).RoleRef).To(Equal(existing.RoleRef), "roleRef is immutable")
						return c.Update(ctx, obj, opts...)
					},
				}).Build()

			Expect(r.createOrUpdate(ctx, desired, nil)).To(Succeed())
			Expect(deletes).To(Equal(expectedDeletes))
			Expect(creates).To(Equal(expectedCreates))
			Expect(updates).To(Equal(expectedUpdates))

			actual := &rbacv1.ClusterRoleBinding{}
			Expect(r.Get(ctx, client.ObjectKeyFromObject(desired), actual)).To(Succeed())
			if unmanaged {
				Expect(actual.RoleRef).To(Equal(existing.RoleRef))
				Expect(actual.UID).To(Equal(existing.UID))
				return
			}
			Expect(actual.RoleRef).To(Equal(desired.RoleRef))
			Expect(actual.Subjects).To(Equal(desired.Subjects))
			Expect(actual.Annotations).To(HaveKeyWithValue(specHashAnnotation,
				computeSpecHash(r.buildMetricsReaderClusterRoleBinding("test-ns"))))

			Expect(r.createOrUpdate(ctx, r.buildMetricsReaderClusterRoleBinding("test-ns"), nil)).To(Succeed())
			Expect(deletes).To(Equal(expectedDeletes))
			Expect(creates).To(Equal(expectedCreates))
			Expect(updates).To(Equal(expectedUpdates))
		},
		Entry("replace the legacy role reference with its old desired-state hash", func(binding *rbacv1.ClusterRoleBinding) {
			binding.RoleRef.Name = "metrics-reader"
			setSpecHashAnnotation(binding, computeSpecHash(binding))
		}, 1, 1, 0, false),
		Entry("replace the legacy role reference without a hash", func(binding *rbacv1.ClusterRoleBinding) {
			binding.RoleRef.Name = "metrics-reader"
		}, 1, 1, 0, false),
		Entry("leave unmanaged legacy bindings untouched", func(binding *rbacv1.ClusterRoleBinding) {
			binding.RoleRef.Name = "metrics-reader"
			binding.Annotations = map[string]string{unmanagedAnnotationKey: unmanagedAnnotationTrue}
		}, 0, 0, 0, true),
		Entry("skip unchanged bindings", func(binding *rbacv1.ClusterRoleBinding) {
			setSpecHashAnnotation(binding, computeSpecHash(binding))
		}, 0, 0, 0, false),
		Entry("update subjects without replacing the binding", func(binding *rbacv1.ClusterRoleBinding) {
			binding.Subjects[0].Namespace = "old-ns"
		}, 0, 0, 1, false),
	)
})

var _ = Describe("setCondition", func() {
	var (
		r      *OperatorConfigReconciler
		config *automotivev1alpha1.OperatorConfig
	)

	BeforeEach(func() {
		r = &OperatorConfigReconciler{}
		config = &automotivev1alpha1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-config",
				Generation: 1,
			},
		}
	})

	It("should add a new condition", func() {
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionTrue, "ReconcileSucceeded", "all good")

		Expect(config.Status.Conditions).To(HaveLen(1))
		c := config.Status.Conditions[0]
		Expect(c.Type).To(Equal(automotivev1alpha1.OperatorConfigConditionReady))
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.Reason).To(Equal("ReconcileSucceeded"))
		Expect(c.Message).To(Equal("all good"))
		Expect(c.ObservedGeneration).To(Equal(int64(1)))
	})

	It("should update an existing condition of the same type", func() {
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionFalse, "DeployFailed", "broken")
		firstTransition := config.Status.Conditions[0].LastTransitionTime

		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionTrue, "ReconcileSucceeded", "fixed")

		Expect(config.Status.Conditions).To(HaveLen(1))
		c := config.Status.Conditions[0]
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.Reason).To(Equal("ReconcileSucceeded"))
		Expect(c.LastTransitionTime).NotTo(Equal(firstTransition))
	})

	It("should track multiple condition types independently", func() {
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionTrue, "ReconcileSucceeded", "ok")
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionDegraded, metav1.ConditionFalse, "ReconcileSucceeded", "ok")
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReconciling, metav1.ConditionFalse, "ReconcileSucceeded", "done")

		Expect(config.Status.Conditions).To(HaveLen(3))

		types := map[string]metav1.ConditionStatus{}
		for _, c := range config.Status.Conditions {
			types[c.Type] = c.Status
		}
		Expect(types).To(HaveKeyWithValue("Ready", metav1.ConditionTrue))
		Expect(types).To(HaveKeyWithValue("Degraded", metav1.ConditionFalse))
		Expect(types).To(HaveKeyWithValue("Reconciling", metav1.ConditionFalse))
	})

	It("should use the config's generation for ObservedGeneration", func() {
		config.Generation = 5
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionTrue, "Ok", "ok")

		Expect(config.Status.Conditions[0].ObservedGeneration).To(Equal(int64(5)))
	})

	It("should set all three conditions for a failed deploy scenario", func() {
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionFalse, "DeployFailed", "err")
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionDegraded, metav1.ConditionTrue, "DeployFailed", "err")
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReconciling, metav1.ConditionFalse, "ReconcileFailed", "err")

		types := map[string]metav1.ConditionStatus{}
		for _, c := range config.Status.Conditions {
			types[c.Type] = c.Status
		}
		Expect(types).To(HaveKeyWithValue("Ready", metav1.ConditionFalse))
		Expect(types).To(HaveKeyWithValue("Degraded", metav1.ConditionTrue))
		Expect(types).To(HaveKeyWithValue("Reconciling", metav1.ConditionFalse))
	})

	It("should transition from degraded to healthy", func() {
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionFalse, "DeployFailed", "broken")
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionDegraded, metav1.ConditionTrue, "DeployFailed", "broken")

		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionReady, metav1.ConditionTrue, "ReconcileSucceeded", "recovered")
		r.setCondition(config, automotivev1alpha1.OperatorConfigConditionDegraded, metav1.ConditionFalse, "ReconcileSucceeded", "recovered")

		types := map[string]metav1.ConditionStatus{}
		for _, c := range config.Status.Conditions {
			types[c.Type] = c.Status
		}
		Expect(types).To(HaveKeyWithValue("Ready", metav1.ConditionTrue))
		Expect(types).To(HaveKeyWithValue("Degraded", metav1.ConditionFalse))
	})
})
