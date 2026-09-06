package imagebuild

import (
	"context"
	"fmt"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
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

	// ActiveBuilds tracks the number of currently in-progress builds.
	ActiveBuilds = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "active",
			Help:      "Number of currently in-progress builds",
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

func adjustActiveBuildsGauge(oldPhase, newPhase string) {
	if oldPhase == newPhase {
		return
	}
	if newPhase == phaseBuilding {
		ActiveBuilds.Inc()
	} else if oldPhase == phaseBuilding {
		ActiveBuilds.Dec()
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
	var active float64

	for i := range builds {
		b := &builds[i]

		if b.Status.Phase == phaseBuilding {
			active++
		}

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

	ActiveBuilds.Set(active)
}

// seedMetricsFromCRs pre-loads counters from existing ImageBuild CRs so that
// metrics survive operator pod restarts. Histograms are not seeded to avoid
// inflating observation counts on each restart.
func (r *ImageBuildReconciler) seedMetricsFromCRs(mgr ctrl.Manager) manager.RunnableFunc {
	return func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("cache sync failed")
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
