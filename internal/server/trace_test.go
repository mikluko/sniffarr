package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"sarak.sh/mikluko/sniffarr/internal/arr"
)

const (
	callerTrace       = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan        = "00f067aa0ba902b7"
	callerTraceparent = "00-" + callerTrace + "-" + callerSpan + "-01"
)

// recordSpans installs a recorder as the global TracerProvider and
// TraceContext as the global propagator for the rest of the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	rec := tracetest.NewSpanRecorder()
	sdk := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(sdk)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(prop)
		require.NoError(t, sdk.Shutdown(context.Background()))
	})
	return rec
}

// requireServerSpan requires the ended span named name to be a server span
// continuing the caller's trace, with a span under it. The span may end
// after the client has read the response, so it waits for the span.
func requireServerSpan(t *testing.T, rec *tracetest.SpanRecorder, name string) {
	t.Helper()
	var span sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool {
		for _, s := range rec.Ended() {
			if s.Name() == name {
				span = s
			}
		}
		return span != nil
	}, 5*time.Second, 10*time.Millisecond, "no span %q", name)
	require.Equal(t, trace.SpanKindServer, span.SpanKind())
	require.Equal(t, callerTrace, span.Parent().TraceID().String())
	require.Equal(t, callerSpan, span.Parent().SpanID().String())
	require.True(t, span.Parent().IsRemote())
	var children int
	for _, s := range rec.Ended() {
		if s.Parent().SpanID() == span.SpanContext().SpanID() {
			children++
		}
	}
	require.NotZero(t, children, "the handler's store calls nest under %q", name)
}

func TestTorznabTrace(t *testing.T) {
	rec := recordSpans(t)
	srv, hc := start(t, Config{TorznabToken: "tz"})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/torznab/api?t=search&q=alien&apikey=tz", nil)
	require.NoError(t, err)
	req.Header.Set("traceparent", callerTraceparent)
	res, err := hc.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	requireServerSpan(t, rec, "GET /torznab/api")
}

func TestWebhookTrace(t *testing.T) {
	rec := recordSpans(t)
	srv, hc := start(t, Config{WebhookToken: "wh", Arr: &arr.Recorder{}})
	body := `{"eventType":"Grab","movie":{"id":1,"title":"Alien","year":1979,"tmdbId":348},
		"release":{"releaseTitle":"Alien.1979.1080p.BluRay.x264","indexer":"tracker","size":8589934592},
		"downloadClient":"qBittorrent","downloadClientType":"qBittorrent","downloadId":"0700000000000000000000000000000000000000"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/webhooks/arr", strings.NewReader(body))
	require.NoError(t, err)
	req.SetBasicAuth("radarr", "wh")
	req.Header.Set("traceparent", callerTraceparent)
	res, err := hc.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	requireServerSpan(t, rec, "POST /webhooks/arr")
}
