package imagebuild

import (
	"context"
	"fmt"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	metricsNamespace = "ado"
	metricsSubsystem = "build"

	buildStatusSuccess = "success"
	buildStatusFailure = "failure"
)

var (
	// BuildDuration tracks the total build duration in seconds.
	BuildDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "duration_seconds",
			Help:      "Total build duration in seconds",
			Buckets:   []float64{30, 60, 120, 180, 240, 300, 420, 600, 900, 1200},
		},
		[]string{"mode", "distro", "target", "format", "arch", "status"},
	)

	// BuildPhaseDuration tracks duration of individual build phases in seconds.
	BuildPhaseDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "phase_duration_seconds",
			Help:      "Duration of individual build phases in seconds",
			Buckets:   []float64{1, 5, 10, 30, 60, 120, 180, 240, 300, 600},
		},
		[]string{"mode", "distro", "target", "phase"},
	)

	// BuildTotal counts total builds by status.
	BuildTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "total",
			Help:      "Total number of builds by status",
		},
		[]string{"mode", "distro", "target", "format", "arch", "status"},
	)

	// ActiveBuilds tracks the number of ImageBuilds in the Building phase.
	ActiveBuilds = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "active",
			Help:      "Number of ImageBuilds in the Building phase",
		},
	)

	// FlashTotal counts pipeline-triggered flash operations by status.
	FlashTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "flash",
			Name:      "total",
			Help:      "Total number of pipeline flash operations by status",
		},
		[]string{"target", "status"},
	)

	// FlashDuration tracks pipeline flash duration in seconds.
	FlashDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "flash",
			Name:      "duration_seconds",
			Help:      "Pipeline flash operation duration in seconds",
			Buckets:   []float64{10, 30, 60, 120, 180, 300, 600, 900},
		},
		[]string{"target", "status"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		BuildDuration,
		BuildPhaseDuration,
		BuildTotal,
		ActiveBuilds,
		FlashTotal,
		FlashDuration,
	)
}

func newActiveBuildsHandler(gauge prometheus.Gauge) cache.ResourceEventHandler {
	// Informer notifications are ordered. Track names so replays and updates
	// replacing a deleted build with a new UID cannot double-count a build.
	active := make(map[types.NamespacedName]struct{})
	observe := func(obj any) {
		build, ok := obj.(*automotivev1alpha1.ImageBuild)
		if !ok {
			return
		}
		key := types.NamespacedName{Namespace: build.Namespace, Name: build.Name}
		if build.Status.Phase == phaseBuilding {
			active[key] = struct{}{}
		} else {
			delete(active, key)
		}
		gauge.Set(float64(len(active)))
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    observe,
		UpdateFunc: func(_, obj any) { observe(obj) },
		DeleteFunc: func(obj any) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return
			}
			namespace, name, err := cache.SplitMetaNamespaceKey(key)
			if err != nil {
				return
			}
			delete(active, types.NamespacedName{Namespace: namespace, Name: name})
			gauge.Set(float64(len(active)))
		},
	}
}

func buildMetricStatus(b *automotivev1alpha1.ImageBuild) string {
	phase := b.Status.Phase
	if phase == automotivev1alpha1.ImageBuildPhaseExpired {
		if b.Status.PreviousPhase != "" {
			phase = b.Status.PreviousPhase
		} else {
			return buildStatusSuccess
		}
	}
	if phase == automotivev1alpha1.ImageBuildPhaseCompleted {
		return buildStatusSuccess
	}
	return buildStatusFailure
}

func seedMetrics(builds []automotivev1alpha1.ImageBuild) {
	for i := range builds {
		b := &builds[i]

		if !automotivev1alpha1.IsTerminalBuildPhase(b.Status.Phase) {
			continue
		}

		status := buildMetricStatus(b)
		mode := b.Spec.GetMode()
		distro := b.Spec.GetDistro()
		target := b.Spec.GetTarget()
		format := b.Spec.GetExportFormat()
		arch := b.Spec.Architecture

		BuildTotal.WithLabelValues(mode, distro, target, format, arch, status).Add(1)

		if b.Status.FlashTaskRunName != "" {
			FlashTotal.WithLabelValues(target, status).Add(1)
		}
	}
}

// seedMetricsFromCRs pre-loads terminal counters from existing ImageBuild CRs
// and registers live active-build tracking on the leader. Histograms are not
// seeded to avoid inflating observation counts on each restart.
func (r *ImageBuildReconciler) seedMetricsFromCRs(mgr ctrl.Manager) manager.RunnableFunc {
	return func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("cache sync failed")
		}
		informer, err := mgr.GetCache().GetInformer(ctx, &automotivev1alpha1.ImageBuild{})
		if err != nil {
			return fmt.Errorf("failed to get ImageBuild informer: %w", err)
		}
		// Register on the leader only. The informer replays its existing objects
		// before delivering updates, avoiding a separate gauge-seeding race.
		if _, err := informer.AddEventHandler(newActiveBuildsHandler(ActiveBuilds)); err != nil {
			return fmt.Errorf("failed to register active build metrics handler: %w", err)
		}
		var builds automotivev1alpha1.ImageBuildList
		if err := mgr.GetClient().List(ctx, &builds); err != nil {
			r.Log.Error(err, "Failed to seed metrics from CRs")
			return err
		}

		seedMetrics(builds.Items)

		r.Log.Info("Seeded metrics from CRs", "totalCRs", len(builds.Items))
		return nil
	}
}
