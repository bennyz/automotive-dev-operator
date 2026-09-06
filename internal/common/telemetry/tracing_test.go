package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func saveAndRestoreOTelGlobals(t *testing.T) {
	t.Helper()
	origTP := otel.GetTracerProvider()
	origProp := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(origTP)
		otel.SetTextMapPropagator(origProp)
	})
}

func TestInitTracing_NoEndpoint(t *testing.T) {
	saveAndRestoreOTelGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := InitTracing(context.Background(), "test-service", "", 1.0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	tp := otel.GetTracerProvider()
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	if span.SpanContext().IsValid() {
		t.Error("expected no-op span without endpoint, got valid span context")
	}
}

func TestInitTracing_WithEndpoint(t *testing.T) {
	saveAndRestoreOTelGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := InitTracing(context.Background(), "test-service", "localhost:4317", 1.0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Logf("shutdown error (expected with fake endpoint): %v", err)
		}
	}()

	tp := otel.GetTracerProvider()
	if _, ok := tp.(noop.TracerProvider); ok {
		t.Error("expected real TracerProvider, got noop")
	}

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	if !span.SpanContext().IsValid() {
		t.Error("expected valid span context with endpoint configured")
	}
}

func TestInitTracing_EnvVarFallback(t *testing.T) {
	saveAndRestoreOTelGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

	shutdown, err := InitTracing(context.Background(), "test-service", "", 0.5, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Logf("shutdown error (expected with fake endpoint): %v", err)
		}
	}()

	tp := otel.GetTracerProvider()
	if _, ok := tp.(noop.TracerProvider); ok {
		t.Error("expected real TracerProvider with env var fallback, got noop")
	}
}

func TestInitTracing_NoEndpoint_ReturnsNoopProvider(t *testing.T) {
	saveAndRestoreOTelGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := InitTracing(context.Background(), "test-service", "", 1.0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	// Global provider should NOT have been changed (still default noop)
	tp := otel.GetTracerProvider()
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	if span.SpanContext().TraceID() != (trace.TraceID{}) {
		t.Error("expected zero TraceID from noop provider")
	}
}

func TestInitTracing_PropagatorSet(t *testing.T) {
	saveAndRestoreOTelGlobals(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := InitTracing(context.Background(), "test-service", "localhost:4317", 1.0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = shutdown(context.Background())
	})

	prop := otel.GetTextMapPropagator()
	if _, ok := prop.(propagation.TraceContext); !ok {
		t.Errorf("expected TraceContext propagator, got %T", prop)
	}
}
