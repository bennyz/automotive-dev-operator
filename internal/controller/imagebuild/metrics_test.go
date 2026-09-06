package imagebuild

import (
	"testing"
	"time"

	automotivev1alpha1 "github.com/centos-automotive-suite/automotive-dev-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func gaugeValue(g prometheus.Gauge) float64 {
	m := &io_prometheus_client.Metric{}
	if err := g.Write(m); err != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

func TestAdjustActiveBuildsGauge(t *testing.T) {
	ActiveBuilds.Set(0)

	adjustActiveBuildsGauge("", "Building")
	if v := gaugeValue(ActiveBuilds); v != 1 {
		t.Errorf("after entering Building: got %v, want 1", v)
	}

	adjustActiveBuildsGauge("Building", "Building")
	if v := gaugeValue(ActiveBuilds); v != 1 {
		t.Errorf("same phase should not change gauge: got %v, want 1", v)
	}

	adjustActiveBuildsGauge("Building", "Completed")
	if v := gaugeValue(ActiveBuilds); v != 0 {
		t.Errorf("after leaving Building: got %v, want 0", v)
	}

	adjustActiveBuildsGauge("Completed", "Failed")
	if v := gaugeValue(ActiveBuilds); v != 0 {
		t.Errorf("non-Building transition should not change gauge: got %v, want 0", v)
	}
}

// counterValue returns the current value of a counter with the given labels.
func counterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	m := &io_prometheus_client.Metric{}
	if err := cv.WithLabelValues(labels...).Write(m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// histogramCount returns the sample count of a histogram with the given labels.
func histogramCount(hv *prometheus.HistogramVec, labels ...string) uint64 {
	m := &io_prometheus_client.Metric{}
	obs, err := hv.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		return 0
	}
	return m.GetHistogram().GetSampleCount()
}

// histogramSum returns the sample sum of a histogram with the given labels.
func histogramSum(hv *prometheus.HistogramVec, labels ...string) float64 {
	m := &io_prometheus_client.Metric{}
	obs, err := hv.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		return 0
	}
	return m.GetHistogram().GetSampleSum()
}

func newImageBuild(mode, target, format, arch string, start, end *metav1.Time) *automotivev1alpha1.ImageBuild {
	ib := &automotivev1alpha1.ImageBuild{
		Spec: automotivev1alpha1.ImageBuildSpec{
			Architecture: arch,
			AIB: &automotivev1alpha1.AIBSpec{
				Distro: "autosd",
				Target: target,
				Mode:   mode,
			},
			Export: &automotivev1alpha1.ExportSpec{
				Format: format,
			},
		},
		Status: automotivev1alpha1.ImageBuildStatus{
			StartTime:      start,
			CompletionTime: end,
		},
	}
	return ib
}

func pipelineRunWithTiming(json string) *tektonv1.PipelineRun {
	return &tektonv1.PipelineRun{
		Status: tektonv1.PipelineRunStatus{
			PipelineRunStatusFields: tektonv1.PipelineRunStatusFields{
				Results: []tektonv1.PipelineRunResult{
					{
						Name:  "build-timing",
						Value: tektonv1.ResultValue{Type: tektonv1.ParamTypeString, StringVal: json},
					},
				},
			},
		},
	}
}

func TestRecordBuildMetrics_Counter(t *testing.T) {
	labels := []string{"package", "autosd", "ebbr", "simg", "arm64", "success"}
	before := counterValue(BuildTotal, labels...)

	start := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	end := metav1.Now()
	ib := newImageBuild("package", "ebbr", "simg", "arm64", &start, &end)
	pr := pipelineRunWithTiming(`{"setup_s":2,"build_s":170,"post_build_s":8,"total_s":180}`)

	recordBuildMetrics(ib, pr, buildStatusSuccess)

	after := counterValue(BuildTotal, labels...)
	if after-before != 1 {
		t.Errorf("BuildTotal counter increment = %v, want 1", after-before)
	}
}

func TestRecordBuildMetrics_FailureCounter(t *testing.T) {
	labels := []string{"package", "autosd", "ebbr", "simg", "amd64", "failure"}
	before := counterValue(BuildTotal, labels...)

	ib := newImageBuild("package", "ebbr", "simg", "amd64", nil, nil)
	recordBuildMetrics(ib, nil, buildStatusFailure)

	after := counterValue(BuildTotal, labels...)
	if after-before != 1 {
		t.Errorf("BuildTotal failure counter increment = %v, want 1", after-before)
	}
}

func TestRecordBuildMetrics_Duration(t *testing.T) {
	labels := []string{"package", "autosd", "ebbr", "simg", "arm64", "success"}
	beforeCount := histogramCount(BuildDuration, labels...)
	beforeSum := histogramSum(BuildDuration, labels...)

	start := metav1.NewTime(time.Now().Add(-180 * time.Second))
	end := metav1.Now()
	ib := newImageBuild("package", "ebbr", "simg", "arm64", &start, &end)
	pr := pipelineRunWithTiming(`{"setup_s":2,"build_s":170,"post_build_s":8,"total_s":180}`)

	recordBuildMetrics(ib, pr, buildStatusSuccess)

	afterCount := histogramCount(BuildDuration, labels...)
	afterSum := histogramSum(BuildDuration, labels...)
	if afterCount-beforeCount != 1 {
		t.Errorf("BuildDuration sample count increment = %v, want 1", afterCount-beforeCount)
	}
	delta := afterSum - beforeSum
	if delta < 179 || delta > 181 {
		t.Errorf("BuildDuration observed value = %v, want ~180", delta)
	}
}

func TestRecordBuildMetrics_NoDurationWithoutTimestamps(t *testing.T) {
	labels := []string{"bootc", "autosd", "ebbr", "simg", "arm64", "success"}
	beforeCount := histogramCount(BuildDuration, labels...)

	ib := newImageBuild("bootc", "ebbr", "simg", "arm64", nil, nil)
	recordBuildMetrics(ib, &tektonv1.PipelineRun{}, buildStatusSuccess)

	afterCount := histogramCount(BuildDuration, labels...)
	if afterCount != beforeCount {
		t.Errorf("BuildDuration should not record without timestamps, got count delta %v", afterCount-beforeCount)
	}
}

func TestRecordBuildMetrics_PhaseDurations(t *testing.T) {
	setupLabels := []string{"package", "autosd", "ebbr", "setup"}
	buildLabels := []string{"package", "autosd", "ebbr", "build"}
	postLabels := []string{"package", "autosd", "ebbr", "post_build"}

	beforeSetup := histogramCount(BuildPhaseDuration, setupLabels...)
	beforeBuild := histogramCount(BuildPhaseDuration, buildLabels...)
	beforePost := histogramCount(BuildPhaseDuration, postLabels...)

	start := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	end := metav1.Now()
	ib := newImageBuild("package", "ebbr", "simg", "arm64", &start, &end)
	pr := pipelineRunWithTiming(`{"setup_s":5,"build_s":160,"post_build_s":15,"total_s":180}`)

	recordBuildMetrics(ib, pr, buildStatusSuccess)

	if histogramCount(BuildPhaseDuration, setupLabels...)-beforeSetup != 1 {
		t.Error("setup phase not recorded")
	}
	if histogramCount(BuildPhaseDuration, buildLabels...)-beforeBuild != 1 {
		t.Error("build phase not recorded")
	}
	if histogramCount(BuildPhaseDuration, postLabels...)-beforePost != 1 {
		t.Error("post_build phase not recorded")
	}

	// Verify observed values
	setupSum := histogramSum(BuildPhaseDuration, setupLabels...)
	if setupSum < 5 {
		t.Errorf("setup phase sum = %v, want >= 5", setupSum)
	}
}

func TestRecordBuildMetrics_NoPhaseDurationsOnFailure(t *testing.T) {
	labels := []string{"package", "autosd", "x86", "setup"}
	beforeCount := histogramCount(BuildPhaseDuration, labels...)

	ib := newImageBuild("package", "x86", "simg", "amd64", nil, nil)
	pr := pipelineRunWithTiming(`{"setup_s":5,"build_s":160,"post_build_s":15,"total_s":180}`)

	recordBuildMetrics(ib, pr, buildStatusFailure)

	afterCount := histogramCount(BuildPhaseDuration, labels...)
	if afterCount != beforeCount {
		t.Error("phase durations should not be recorded on failure")
	}
}

func TestRecordBuildMetrics_MalformedTimingJSON(t *testing.T) {
	labels := []string{"package", "autosd", "qemu", "setup"}
	beforeCount := histogramCount(BuildPhaseDuration, labels...)

	start := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	end := metav1.Now()
	ib := newImageBuild("package", "qemu", "qcow2", "amd64", &start, &end)
	pr := pipelineRunWithTiming(`not valid json`)

	// Should not panic or record phase metrics
	recordBuildMetrics(ib, pr, buildStatusSuccess)

	afterCount := histogramCount(BuildPhaseDuration, labels...)
	if afterCount != beforeCount {
		t.Error("malformed JSON should not produce phase metrics")
	}
}

func TestRecordBuildMetrics_NilPipelineRun(t *testing.T) {
	labels := []string{"package", "autosd", "none", "simg", "amd64", "success"}
	before := counterValue(BuildTotal, labels...)

	ib := newImageBuild("package", "none", "simg", "amd64", nil, nil)

	// Should not panic with nil PipelineRun
	recordBuildMetrics(ib, nil, buildStatusSuccess)

	after := counterValue(BuildTotal, labels...)
	if after-before != 1 {
		t.Errorf("counter should still increment with nil PipelineRun, got delta %v", after-before)
	}
}

func TestRecordBuildMetrics_NoTimingResult(t *testing.T) {
	labels := []string{"package", "autosd", "generic", "setup"}
	beforeCount := histogramCount(BuildPhaseDuration, labels...)

	start := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	end := metav1.Now()
	ib := newImageBuild("package", "generic", "raw", "amd64", &start, &end)
	pr := &tektonv1.PipelineRun{
		Status: tektonv1.PipelineRunStatus{
			PipelineRunStatusFields: tektonv1.PipelineRunStatusFields{
				Results: []tektonv1.PipelineRunResult{
					{Name: "other-result", Value: tektonv1.ResultValue{StringVal: "foo"}},
				},
			},
		},
	}

	recordBuildMetrics(ib, pr, buildStatusSuccess)

	afterCount := histogramCount(BuildPhaseDuration, labels...)
	if afterCount != beforeCount {
		t.Error("should not record phase metrics when build-timing result is absent")
	}
}

func TestBuildMetricStatus(t *testing.T) {
	tests := []struct {
		name          string
		phase         string
		previousPhase string
		want          string
	}{
		{"completed", "Completed", "", buildStatusSuccess},
		{"failed", "Failed", "", buildStatusFailure},
		{"cancelled", "Cancelled", "", buildStatusFailure},
		{"expired with previous completed", "Expired", "Completed", buildStatusSuccess},
		{"expired with previous failed", "Expired", "Failed", buildStatusFailure},
		{"expired without previous phase (legacy)", "Expired", "", buildStatusSuccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &automotivev1alpha1.ImageBuild{
				Status: automotivev1alpha1.ImageBuildStatus{
					Phase:         tt.phase,
					PreviousPhase: tt.previousPhase,
				},
			}
			if got := buildMetricStatus(b); got != tt.want {
				t.Errorf("buildMetricStatus(%q, prev=%q) = %q, want %q", tt.phase, tt.previousPhase, got, tt.want)
			}
		})
	}
}

func TestSeedMetricsFromCRs(t *testing.T) {
	// Use unique label values to avoid interference from other tests
	buildLabels := []string{"bootc", "fedora", "seed-target", "raw", "arm64", "success"}
	failLabels := []string{"image", "fedora", "seed-target", "qcow2", "amd64", "failure"}
	flashLabels := []string{"seed-target", "success"}

	beforeBuild := counterValue(BuildTotal, buildLabels...)
	beforeFail := counterValue(BuildTotal, failLabels...)
	beforeFlash := counterValue(FlashTotal, flashLabels...)

	start := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	end := metav1.Now()

	builds := []automotivev1alpha1.ImageBuild{
		{
			Spec: automotivev1alpha1.ImageBuildSpec{
				Architecture: "arm64",
				AIB:          &automotivev1alpha1.AIBSpec{Distro: "fedora", Target: "seed-target", Mode: "bootc"},
				Export:       &automotivev1alpha1.ExportSpec{Format: "raw"},
			},
			Status: automotivev1alpha1.ImageBuildStatus{
				Phase:            "Completed",
				StartTime:        &start,
				CompletionTime:   &end,
				FlashTaskRunName: "flash-run-1",
			},
		},
		{
			Spec: automotivev1alpha1.ImageBuildSpec{
				Architecture: "arm64",
				AIB:          &automotivev1alpha1.AIBSpec{Distro: "fedora", Target: "seed-target", Mode: "bootc"},
				Export:       &automotivev1alpha1.ExportSpec{Format: "raw"},
			},
			Status: automotivev1alpha1.ImageBuildStatus{
				Phase:          "Completed",
				StartTime:      &start,
				CompletionTime: &end,
			},
		},
		// Expired build with PreviousPhase=Completed → counts as success
		{
			Spec: automotivev1alpha1.ImageBuildSpec{
				Architecture: "arm64",
				AIB:          &automotivev1alpha1.AIBSpec{Distro: "fedora", Target: "seed-target", Mode: "bootc"},
				Export:       &automotivev1alpha1.ExportSpec{Format: "raw"},
			},
			Status: automotivev1alpha1.ImageBuildStatus{
				Phase:          "Expired",
				PreviousPhase:  "Completed",
				StartTime:      &start,
				CompletionTime: &end,
			},
		},
		{
			Spec: automotivev1alpha1.ImageBuildSpec{
				Architecture: "amd64",
				AIB:          &automotivev1alpha1.AIBSpec{Distro: "fedora", Target: "seed-target", Mode: "image"},
				Export:       &automotivev1alpha1.ExportSpec{Format: "qcow2"},
			},
			Status: automotivev1alpha1.ImageBuildStatus{
				Phase: "Failed",
			},
		},
		// Expired build with PreviousPhase=Failed → counts as failure
		{
			Spec: automotivev1alpha1.ImageBuildSpec{
				Architecture: "amd64",
				AIB:          &automotivev1alpha1.AIBSpec{Distro: "fedora", Target: "seed-target", Mode: "image"},
				Export:       &automotivev1alpha1.ExportSpec{Format: "qcow2"},
			},
			Status: automotivev1alpha1.ImageBuildStatus{
				Phase:         "Expired",
				PreviousPhase: "Failed",
			},
		},
		// In-progress build — should only count as active, not seed counters
		{
			Spec: automotivev1alpha1.ImageBuildSpec{
				Architecture: "amd64",
				AIB:          &automotivev1alpha1.AIBSpec{Distro: "fedora", Target: "seed-target", Mode: "image"},
			},
			Status: automotivev1alpha1.ImageBuildStatus{
				Phase: "Building",
			},
		},
	}

	seedMetrics(builds)

	afterBuild := counterValue(BuildTotal, buildLabels...)
	if afterBuild-beforeBuild != 3 {
		t.Errorf("BuildTotal(success) delta = %v, want 3 (2 completed + 1 expired-from-completed)", afterBuild-beforeBuild)
	}

	afterFail := counterValue(BuildTotal, failLabels...)
	if afterFail-beforeFail != 2 {
		t.Errorf("BuildTotal(failure) delta = %v, want 2 (1 failed + 1 expired-from-failed)", afterFail-beforeFail)
	}

	afterFlash := counterValue(FlashTotal, flashLabels...)
	if afterFlash-beforeFlash != 1 {
		t.Errorf("FlashTotal delta = %v, want 1", afterFlash-beforeFlash)
	}
}

func TestSeedMetricsFromCRs_ActiveBuilds(t *testing.T) {
	ActiveBuilds.Set(0)

	builds := []automotivev1alpha1.ImageBuild{
		{
			Status: automotivev1alpha1.ImageBuildStatus{Phase: "Building"},
		},
		{
			Status: automotivev1alpha1.ImageBuildStatus{Phase: "Building"},
		},
		{
			Status: automotivev1alpha1.ImageBuildStatus{Phase: "Completed"},
		},
	}

	seedMetrics(builds)

	if v := gaugeValue(ActiveBuilds); v != 2 {
		t.Errorf("ActiveBuilds = %v, want 2", v)
	}
}
