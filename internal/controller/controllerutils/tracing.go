package controllerutils

import (
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// EndSpanWithError records the error on the span and sets error status before ending it.
// Use with named returns: defer controllerutils.EndSpanWithError(span, &err)
func EndSpanWithError(span trace.Span, err *error) {
	if *err != nil {
		span.RecordError(*err)
		span.SetStatus(codes.Error, (*err).Error())
	}
	span.End()
}
