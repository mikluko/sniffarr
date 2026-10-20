package sniff

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	sniffarrv1 "github.com/mikluko/sniffarr/internal/apis/sniffarr/v1"
)

func meter(mp metric.MeterProvider) metric.Meter {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	return mp.Meter("github.com/mikluko/sniffarr/internal/sniff")
}

func counter(m metric.Meter, name, unit, desc string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
	if err != nil {
		otel.Handle(err)
	}
	return c
}

func sourceAttr(s sniffarrv1.Source) metric.AddOption {
	v := "sample"
	if s == sniffarrv1.Source_SOURCE_ANNOUNCE {
		v = "announce"
	}
	return metric.WithAttributes(attribute.String("sniffarr.discovery", v))
}

func outcomeAttr(o string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("sniffarr.fetch.outcome", o))
}
