package bus

import (
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

func TestStreams(t *testing.T) {
	seen := map[string]string{}
	for _, cfg := range Streams() {
		require.Equal(t, jetstream.WorkQueuePolicy, cfg.Retention, cfg.Name)
		require.Equal(t, jetstream.FileStorage, cfg.Storage, cfg.Name)
		for _, s := range cfg.Subjects {
			require.NotEmpty(t, s, cfg.Name)
			require.NotContains(t, s, "*", "subjects are literal")
			require.NotContains(t, s, ">", "subjects are literal")
			require.True(t, strings.HasPrefix(s, "sniffarr."), s)
			require.NotContains(t, seen, s, "%s in %s and %s", s, seen[s], cfg.Name)
			seen[s] = cfg.Name
		}
		switch cfg.Name {
		case StreamSightings:
			require.Equal(t, SightingsDuplicates, cfg.Duplicates)
		default:
			require.Zero(t, cfg.MaxMsgs, cfg.Name)
			require.Zero(t, cfg.MaxBytes, cfg.Name)
			require.Zero(t, cfg.MaxAge, cfg.Name)
			require.Zero(t, cfg.MaxMsgsPerSubject, cfg.Name)
		}
	}
	require.Len(t, seen, 3+2*Priorities)
}

func TestConsumers(t *testing.T) {
	cs := Consumers()
	require.Len(t, cs, 2)
	for stream, subjects := range map[string][Priorities]string{StreamFetchOrders: FetchOrders, StreamProbeOrders: ProbeOrders} {
		require.Len(t, cs[stream], Priorities)
		for p, c := range cs[stream] {
			require.Equal(t, OrderConsumers[p], c.Durable)
			require.Equal(t, subjects[p], c.FilterSubject)
		}
	}
}
