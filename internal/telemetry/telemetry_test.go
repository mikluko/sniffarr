package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestDisabled(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "TRUE": true, " true ": true, "false": false, "": false, "1": false} {
		require.Equal(t, want, Disabled(func(string) string { return v }), "%q", v)
	}
}

func TestSetupDisabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	before := otel.GetTracerProvider()
	shutdown, err := Setup(context.Background(), "test", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
	require.Equal(t, before, otel.GetTracerProvider(), "no provider is installed")
	_, isSDK := otel.GetMeterProvider().(*sdkmetric.MeterProvider)
	require.False(t, isSDK)
}

func TestSetupEnabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	tp, mp := otel.GetTracerProvider(), otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetTracerProvider(tp); otel.SetMeterProvider(mp) })

	shutdown, err := Setup(context.Background(), "test", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	_, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, isSDK)
	require.NoError(t, shutdown(context.Background()))
}

func TestHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(Handler{slog.NewJSONHandler(&buf, nil)})
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	log.InfoContext(ctx, "inside")
	span.End()
	log.Info("outside")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)
	var inside, outside map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &inside))
	require.NoError(t, json.Unmarshal(lines[1], &outside))
	require.Equal(t, span.SpanContext().TraceID().String(), inside["trace_id"])
	require.Equal(t, span.SpanContext().SpanID().String(), inside["span_id"])
	require.NotContains(t, outside, "trace_id")
}
