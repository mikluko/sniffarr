package ingest

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const scope = "github.com/mikluko/sniffarr/internal/ingest"

type metrics struct {
	sightings, expiries, results, rejections, stored metric.Int64Counter
}

func newMetrics(mp metric.MeterProvider) *metrics {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	m := mp.Meter(scope)
	counter := func(name, unit, desc string) metric.Int64Counter {
		c, err := m.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
		if err != nil {
			otel.Handle(err)
		}
		return c
	}
	return &metrics{
		sightings:  counter("sniffarr.ingest.sightings", "{sighting}", "Sightings received, by whether they queued a new infohash."),
		expiries:   counter("sniffarr.queue.lease_expiries", "{lease}", "Leases that expired before their result came back."),
		results:    counter("sniffarr.ingest.results", "{result}", "Fetch results received, by outcome."),
		rejections: counter("sniffarr.ingest.rejections", "{torrent}", "Fetched torrents the rules rejected, by reason."),
		stored:     counter("sniffarr.ingest.stored", "{torrent}", "Fetched torrents stored."),
	}
}

var (
	resultNew   = metric.WithAttributes(attribute.String("sniffarr.sighting.result", "new"))
	resultKnown = metric.WithAttributes(attribute.String("sniffarr.sighting.result", "known"))
)

func outcome(o string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.fetch.outcome", o))
}

func reasonAttr(r string) metric.AddOption {
	return metric.WithAttributes(attribute.String("sniffarr.rules.reason", r))
}
