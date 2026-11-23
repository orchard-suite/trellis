package obs

import (
	"context"
	"runtime/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// registerProcess reports the process's own health on the same export:
// process.cpu.time by process.cpu.state and process.memory.usage by
// process.memory.kind: heap, the live Go heap, and rss, the memory the
// process holds resident, which is what a container's memory limit counts.
// The pod's limits and restarts come from the cluster.
func registerProcess(m metric.Meter) error {
	cpu, err := m.Float64ObservableCounter("process.cpu.time", metric.WithUnit("s"), metric.WithDescription("CPU seconds the process has used, by state."))
	if err != nil {
		return err
	}
	mem, err := m.Int64ObservableGauge("process.memory.usage", metric.WithUnit("By"), metric.WithDescription("Memory the process holds, by kind."))
	if err != nil {
		return err
	}
	user := metric.WithAttributes(attribute.String("process.cpu.state", "user"))
	system := metric.WithAttributes(attribute.String("process.cpu.state", "system"))
	heap := metric.WithAttributes(attribute.String("process.memory.kind", "heap"))
	rss := metric.WithAttributes(attribute.String("process.memory.kind", "rss"))
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if u, s, ok := cpuSeconds(); ok {
			o.ObserveFloat64(cpu, u, user)
			o.ObserveFloat64(cpu, s, system)
		}
		metrics.Read(samples)
		o.ObserveInt64(mem, int64(samples[0].Value.Uint64()), heap)
		resident, ok := rssBytes()
		if !ok {
			resident = samples[1].Value.Uint64()
		}
		o.ObserveInt64(mem, int64(resident), rss)
		return nil
	}, cpu, mem)
	return err
}
