package database

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func TestTelemetry(t *testing.T) {
	ctx := context.Background()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"), Options{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), Tracer: tp})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.Migrate(ctx, "t", fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE t (x TEXT)")}}))
	spans.Reset()

	const insert = "INSERT INTO t (x) VALUES (?)"
	_, err = db.W.ExecContext(ctx, insert, "untraced-secret")
	require.NoError(t, err)
	require.Empty(t, spans.Ended(), "no parent, no span")

	parent, root := tp.Tracer("test").Start(ctx, "root")
	_, err = db.W.ExecContext(parent, insert, "traced-secret")
	require.NoError(t, err)
	var card int64
	require.NoError(t, db.R.QueryRowContext(parent, "SELECT hll_card(NULL)").Scan(&card), "custom functions work through the wrapper")
	require.Zero(t, card)
	root.End()

	var exec sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if s.Name() == "root" {
			continue
		}
		require.Equal(t, root.SpanContext().SpanID(), s.Parent().SpanID(), s.Name())
		require.NotEqual(t, "sql.rows", s.Name())
		if s.Name() == "sql.conn.exec" {
			exec = s
		}
		for _, a := range s.Attributes() {
			require.NotContains(t, a.Value.Emit(), "secret", "arguments are not recorded")
		}
	}
	require.NotNil(t, exec)
	attrs := attribute.NewSet(exec.Attributes()...)
	text, _ := attrs.Value(semconv.DBQueryTextKey)
	require.Equal(t, insert, text.AsString())
	pool, _ := attrs.Value(poolKey)
	require.Equal(t, "write", pool.AsString())

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))
	var points []metricdata.HistogramDataPoint[float64]
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "db.client.operation.duration" {
				points = m.Data.(metricdata.Histogram[float64]).DataPoints
			}
		}
	}
	allowed := map[attribute.Key]bool{semconv.DBSystemNameKey: true, poolKey: true, semconv.DBOperationNameKey: true, semconv.CodeFunctionNameKey: true, semconv.ErrorTypeKey: true}
	var execs uint64
	for _, p := range points {
		for _, a := range p.Attributes.ToSlice() {
			require.True(t, allowed[a.Key], "metric attribute %s", a.Key)
		}
		op, _ := p.Attributes.Value(semconv.DBOperationNameKey)
		fn, _ := p.Attributes.Value(semconv.CodeFunctionNameKey)
		if op.AsString() == "sql.conn.exec" && fn.AsString() == "sarak.sh/mikluko/sniffarr/internal/database.TestTelemetry" {
			execs += p.Count
		}
	}
	require.Equal(t, uint64(2), execs, "traced and untraced inserts are both measured")
}
