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
			require.NotContains(t, s, ">", "no subject tree is taken whole")
			if strings.Contains(s, "*") {
				require.Contains(t, []string{SubjectFetchRetry + "*", SubjectProbeRetry + "*"}, s, "only the retries carry a token")
			}
			require.True(t, strings.HasPrefix(s, "sniffarr."), s)
			require.NotContains(t, seen, s, "%s in %s and %s", s, seen[s], cfg.Name)
			seen[s] = cfg.Name
		}
		switch cfg.Name {
		case StreamSightings:
			require.Equal(t, SightingsDuplicates, cfg.Duplicates)
		case StreamFetchOrders, StreamProbeOrders:
			require.True(t, cfg.AllowMsgSchedules, cfg.Name)
			require.Equal(t, jetstream.DiscardOld, cfg.Discard, "scheduling refuses DiscardNew")
			require.Equal(t, OrdersDuplicates, cfg.Duplicates)
			fallthrough
		default:
			require.Zero(t, cfg.MaxMsgs, cfg.Name)
			require.Zero(t, cfg.MaxBytes, cfg.Name)
			require.Zero(t, cfg.MaxAge, cfg.Name)
			require.Zero(t, cfg.MaxMsgsPerSubject, cfg.Name)
		}
	}
	require.Len(t, seen, 4+len(FetchLanes)+len(ProbeLanes)+2)
}

func TestConsumers(t *testing.T) {
	cs := Consumers()
	require.Len(t, cs, 3)
	require.Equal(t, []string{SubjectSightAnnounce, SubjectSightSample},
		[]string{cs[StreamSightings][0].FilterSubject, cs[StreamSightings][1].FilterSubject})
	require.Equal(t, []string{ConsumerAnnounce, ConsumerSample}, []string{cs[StreamSightings][0].Durable, cs[StreamSightings][1].Durable})
	for stream, lanes := range map[string][]Lane{StreamFetchOrders: FetchLanes, StreamProbeOrders: ProbeLanes} {
		require.Len(t, cs[stream], len(lanes))
		for i, c := range cs[stream] {
			require.Equal(t, lanes[i].Consumer, c.Durable)
			require.Equal(t, lanes[i].Subject, c.FilterSubject)
			require.Equal(t, OrderAckWait, c.AckWait)
			require.Equal(t, OrderInFlight, c.MaxAckPending)
		}
	}
}

func TestLanes(t *testing.T) {
	for _, lanes := range [][]Lane{FetchLanes, ProbeLanes} {
		for i := 1; i < len(lanes); i++ {
			require.LessOrEqual(t, lanes[i-1].Priority, lanes[i].Priority, "lanes are served by priority")
		}
	}
	for p, subject := range FetchOrders {
		require.Contains(t, FetchLanes, Lane{OrderConsumers[p], subject, p, store.TaskFetch})
	}
	for p, subject := range ProbeOrders {
		require.Equal(t, Lane{OrderConsumers[p], subject, p, store.TaskProbe}, ProbeLanes[p])
	}
}

func TestSightingMsg(t *testing.T) {
	h := [20]byte{0xab, 1}
	m, ok := SightingMsg(h, store.Announce)
	require.True(t, ok)
	require.Equal(t, SubjectSightAnnounce, m.Subject)
	require.Equal(t, h[:], m.Data)
	require.Equal(t, store.Announce+":ab01000000000000000000000000000000000000", m.Header.Get(jetstream.MsgIDHeader))
	s, ok := SightingMsg(h, store.Sample)
	require.True(t, ok)
	require.Equal(t, SubjectSightSample, s.Subject)
	require.NotEqual(t, m.Header.Get(jetstream.MsgIDHeader), s.Header.Get(jetstream.MsgIDHeader), "an announce is not a duplicate of a sample")
	_, ok = SightingMsg(h, store.Arr)
	require.False(t, ok, "only the sniffer's discoveries have a subject")
}

func TestOrderMsg(t *testing.T) {
	h := [20]byte{0xab, 1}
	m := OrderMsg(FetchLanes[3], h, 0, "", "")
	require.Equal(t, SubjectFetchP2, m.Subject)
	require.Equal(t, h[:], m.Data)
	require.Empty(t, m.Header.Get(HeaderAttempt))
	require.Empty(t, m.Header.Get(jetstream.MsgIDHeader))
	require.Empty(t, m.Header.Get(HeaderDiscovery))
	require.Zero(t, Attempt(m.Header))
	m = OrderMsg(ProbeLanes[1], h, 2, "x", store.Torznab)
	require.Equal(t, 2, Attempt(m.Header))
	require.Equal(t, "x", m.Header.Get(jetstream.MsgIDHeader))
	require.Equal(t, store.Torznab, m.Header.Get(HeaderDiscovery))
	m.Header.Set(HeaderAttempt, "-1")
	require.Zero(t, Attempt(m.Header))
}

func TestRetrySubject(t *testing.T) {
	h := [20]byte{0xab, 1}
	require.Equal(t, "sniffarr.orders.fetch.retry.ab01000000000000000000000000000000000000", RetrySubject(StreamFetchOrders, h))
	require.Equal(t, "sniffarr.orders.probe.retry.ab01000000000000000000000000000000000000", RetrySubject(StreamProbeOrders, h))
}
