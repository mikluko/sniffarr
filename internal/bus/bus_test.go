package bus

import (
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/store"
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
	require.Len(t, seen, 4+2*Priorities)
	require.NotContains(t, seen, SubjectStored, "the stored hashes are not kept")
}

func TestConsumers(t *testing.T) {
	cs := Consumers()
	require.Len(t, cs, 3)
	require.Equal(t, []string{SubjectSightAnnounce, SubjectSightSample},
		[]string{cs[StreamSightings][0].FilterSubject, cs[StreamSightings][1].FilterSubject})
	require.Equal(t, []string{ConsumerAnnounce, ConsumerSample}, []string{cs[StreamSightings][0].Durable, cs[StreamSightings][1].Durable})
	for stream, subjects := range map[string][Priorities]string{StreamFetchOrders: FetchOrders, StreamProbeOrders: ProbeOrders} {
		require.Len(t, cs[stream], Priorities)
		for p, c := range cs[stream] {
			require.Equal(t, OrderConsumers[p], c.Durable)
			require.Equal(t, subjects[p], c.FilterSubject)
		}
	}
}

func TestSightingMsg(t *testing.T) {
	h := [20]byte{0xab, 1}
	m, ok := SightingMsg(h, store.Announce)
	require.True(t, ok)
	require.Equal(t, SubjectSightAnnounce, m.Subject)
	require.Equal(t, h[:], m.Data)
	require.Equal(t, "ab01000000000000000000000000000000000000", m.Header.Get(jetstream.MsgIDHeader))
	m, ok = SightingMsg(h, store.Sample)
	require.True(t, ok)
	require.Equal(t, SubjectSightSample, m.Subject)
	_, ok = SightingMsg(h, store.Arr)
	require.False(t, ok, "only the sniffer's discoveries have a subject")
}
