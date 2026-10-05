package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// GaugeDefinition names one process gauge; definitions are fixed by the engine.
type GaugeDefinition struct{ Name, Help string }

// RegisterGauges samples one related group through the OTLP reader, without
// needing Prometheus requests or a second polling goroutine. A failed read
// omits observations instead of falsely reporting zero. The provider owns
// callback lifetime; register once before serving, after Init.
func RegisterGauges(definitions []GaugeDefinition, sample func(context.Context) ([]float64, error)) {
	if !Enabled() {
		return
	}
	meter := otel.Meter("quivr")
	instruments := make([]metric.Float64ObservableGauge, len(definitions))
	observables := make([]metric.Observable, len(definitions))
	for i, definition := range definitions {
		gauge, err := meter.Float64ObservableGauge(definition.Name, metric.WithDescription(definition.Help))
		if err != nil {
			otel.Handle(err)
			return
		}
		instruments[i], observables[i] = gauge, gauge
	}
	_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		read, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		values, err := sample(read)
		if err != nil || len(values) != len(instruments) {
			return nil
		}
		for i, value := range values {
			observer.ObserveFloat64(instruments[i], value)
		}
		return nil
	}, observables...)
	if err != nil {
		otel.Handle(err)
	}
}
