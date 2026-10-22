package telemetry

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPClient returns a copy of c, http.DefaultClient when nil, whose
// requests record client spans and metrics and carry the trace context. The
// copy keeps c's timeout, redirect policy and cookie jar.
func HTTPClient(c *http.Client, opts ...otelhttp.Option) *http.Client {
	if c == nil {
		c = http.DefaultClient
	}
	traced := *c
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	traced.Transport = otelhttp.NewTransport(base, opts...)
	return &traced
}
