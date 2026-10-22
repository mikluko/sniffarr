// Package telemetrytest checks the spans code under test records.
package telemetrytest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// RequireClientChild runs call under a parent span, with a span recorder
// as the global TracerProvider and TraceContext as the global propagator,
// and requires call to have recorded a client span under the parent. It
// returns that span. The globals are restored before it returns, so tests
// calling it must not run in parallel.
func RequireClientChild(t *testing.T, call func(ctx context.Context)) sdktrace.ReadOnlySpan {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	rec := tracetest.NewSpanRecorder()
	sdk := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(sdk)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(prop)
		require.NoError(t, sdk.Shutdown(context.Background()))
	}()
	ctx, parent := sdk.Tracer("telemetrytest").Start(t.Context(), "parent")
	call(ctx)
	parent.End()
	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindClient && s.Parent().SpanID() == parent.SpanContext().SpanID() {
			return s
		}
	}
	require.Fail(t, "no client span under the parent", "spans: %d", len(rec.Ended()))
	return nil
}
