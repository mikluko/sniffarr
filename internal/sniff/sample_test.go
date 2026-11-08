package sniff

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// sums returns the int64 sums collected from reader, keyed by metric name
// and the value of the attribute key; "" for a point without it.
func sums(t *testing.T, reader sdkmetric.Reader, key string) map[string]map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			out[m.Name] = map[string]int64{}
			for _, p := range sum.DataPoints {
				v, _ := p.Attributes.Value(attribute.Key(key))
				out[m.Name][v.AsString()] = p.Value
			}
		}
	}
	return out
}

func TestSampleQueriesByOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	n := started(t, NetworkConfig{Port: freePort(t), Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))}, testID)
	n.sampleWait = 300 * time.Millisecond
	answering := started(t, NetworkConfig{Port: freePort(t)}, [20]byte{0x43})
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = silent.Close() })
	refusing := querier(t)

	sample := func(port int) {
		n.sampleNode(context.Background(), krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}, func(Hash) {}, func(krpc.NodeAddr) {})
	}
	sample(answering.Port())
	sample(answering.Port())
	sample(silent.LocalAddr().(*net.UDPAddr).Port)
	sample(refusing.Addr().(*net.UDPAddr).Port)

	got := sums(t, reader, "sniffarr.dht.outcome")
	require.Equal(t, map[string]int64{"answered": 2, "timeout": 1, "error": 1}, got["sniffarr.dht.sample_queries"])
	require.Equal(t, map[string]int64{"": 2}, got["sniffarr.dht.sample_answers"])
}

func TestSampleQueryCanceledIsNotCounted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	n := started(t, NetworkConfig{Port: freePort(t), Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))}, testID)
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = silent.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	n.sampleNode(ctx, krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: silent.LocalAddr().(*net.UDPAddr).Port}, func(Hash) {}, func(krpc.NodeAddr) {})

	require.NotContains(t, sums(t, reader, "sniffarr.dht.outcome"), "sniffarr.dht.sample_queries")
}
