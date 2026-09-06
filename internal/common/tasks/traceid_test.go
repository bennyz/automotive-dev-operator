package tasks

import (
	"testing"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
)

const (
	traceIDParamName = "trace-id"
	traceIDParamRef  = "$(params.trace-id)"
)

func TestTraceIDParamSpec(t *testing.T) {
	ps := traceIDParamSpec()

	if ps.Name != traceIDParamName {
		t.Errorf("name = %q, want %q", ps.Name, traceIDParamName)
	}
	if ps.Type != tektonv1.ParamTypeString {
		t.Errorf("type = %q, want %q", ps.Type, tektonv1.ParamTypeString)
	}
	if ps.Default == nil {
		t.Fatal("expected non-nil default")
	}
	if ps.Default.StringVal != "" {
		t.Errorf("default = %q, want empty string", ps.Default.StringVal)
	}
}

func TestTraceIDEnvVar(t *testing.T) {
	ev := traceIDEnvVar()

	if ev.Name != "ADO_TRACE_ID" {
		t.Errorf("name = %q, want %q", ev.Name, "ADO_TRACE_ID")
	}
	if ev.Value != traceIDParamRef {
		t.Errorf("value = %q, want %q", ev.Value, traceIDParamRef)
	}
}

func TestTraceIDPipelineParam(t *testing.T) {
	p := traceIDPipelineParam()

	if p.Name != traceIDParamName {
		t.Errorf("name = %q, want %q", p.Name, traceIDParamName)
	}
	if p.Value.Type != tektonv1.ParamTypeString {
		t.Errorf("type = %q, want %q", p.Value.Type, tektonv1.ParamTypeString)
	}
	if p.Value.StringVal != traceIDParamRef {
		t.Errorf("value = %q, want %q", p.Value.StringVal, traceIDParamRef)
	}
}

func TestTraceIDParamSpec_InPipeline(t *testing.T) {
	pipeline := GenerateTektonPipeline("test-pipeline", "test-ns", &BuildConfig{})

	var found bool
	for _, p := range pipeline.Spec.Params {
		if p.Name == traceIDParamName {
			found = true
			if p.Default == nil || p.Default.StringVal != "" {
				t.Error("trace-id pipeline param should default to empty string")
			}
			break
		}
	}
	if !found {
		t.Fatal("pipeline should have trace-id param")
	}
}

func TestTraceIDEnvVar_InBuildTask(t *testing.T) {
	task := GenerateBuildAutomotiveImageTask("test-ns", nil, "")

	var found bool
	for _, step := range task.Spec.Steps {
		for _, env := range step.Env {
			if env.Name == "ADO_TRACE_ID" {
				found = true
				if env.Value != traceIDParamRef {
					t.Errorf("ADO_TRACE_ID value = %q, want Tekton param ref", env.Value)
				}
			}
		}
	}
	if !found {
		t.Fatal("build task should inject ADO_TRACE_ID env var")
	}
}
