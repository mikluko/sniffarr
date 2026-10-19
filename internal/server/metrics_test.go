package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"github.com/mikluko/sniffarr/internal/rot13"
)

// value returns the int64 data point of the named metric whose attributes
// include kv, summed over the matching points.
func value(t *testing.T, rm metricdata.ResourceMetrics, name string, kv ...attribute.KeyValue) int64 {
	t.Helper()
	var sum int64
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			var points []metricdata.DataPoint[int64]
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				points = d.DataPoints
			case metricdata.Gauge[int64]:
				points = d.DataPoints
			}
			for _, p := range points {
				match := true
				for _, want := range kv {
					if got, ok := p.Attributes.Value(want.Key); !ok || got != want.Value {
						match = false
					}
				}
				if match {
					sum += p.Value
					found = true
				}
			}
		}
	}
	require.True(t, found, "no %s %v", name, kv)
	return sum
}

func TestMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	srv, hc := start(t, Config{Meter: mp, Poll: 20 * time.Millisecond})
	ingest := ingestClient(srv, hc)

	_, err := sight(t, ingest, sniffarrv1.Source_SOURCE_SAMPLE, 1, 2, 3, 1)
	require.NoError(t, err)
	f := openFetch(t, ingest)
	f.credit(3)
	f.expect(1, 2, 3)
	f.torrent(1, "The.Thing.1982.1080p.BluRay.x264", "The.Thing.1982.mkv")
	f.torrent(2, rot13.String("Ovt.Cnpxntr.KKK.1080c"), "video.mp4")
	f.failure(3, sniffarrv1.Failure_FAILURE_NO_PEERS)
	f.finish()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	result := func(v string) attribute.KeyValue { return attribute.String("sniffarr.sighting.result", v) }
	outcome := func(v string) attribute.KeyValue { return attribute.String("sniffarr.fetch.outcome", v) }
	require.EqualValues(t, 3, value(t, rm, "sniffarr.ingest.sightings", result("new")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.sightings", result("known")))
	require.EqualValues(t, 2, value(t, rm, "sniffarr.ingest.results", outcome("torrent")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.results", outcome("no_peers")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.stored"))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.ingest.rejections", attribute.String("sniffarr.rules.reason", "adult")))
	require.EqualValues(t, 1, value(t, rm, "sniffarr.queue.depth", attribute.String("sniffarr.discovery", "sample")), "the failed one waits for its retry")
	require.EqualValues(t, 0, value(t, rm, "sniffarr.queue.leases"))
}
