// Package obs wires trellis to the telemetry standard: JSON logs on
// stdout with the trace id, spans and metrics over OTLP when
// OTEL_EXPORTER_OTLP_ENDPOINT is set and nowhere otherwise, the app's
// build_info gauge, the process's own health, and instrumented HTTP on both
// sides.
package obs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Setup installs the propagator, and with an OTLP endpoint the exporters,
// `<service>.build_info`, and the process metrics. Without an endpoint
// nothing is exported, so a laptop run still works. The returned function
// flushes the exporters on shutdown.
func Setup(ctx context.Context, service, version string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	none := func(context.Context) error { return nil }
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return none, nil
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(service),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return nil, err
	}
	traces, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	metrics, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traces), sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)), sdkmetric.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	shutdown := func(ctx context.Context) error { return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx)) }
	m := mp.Meter(service)
	if err := errors.Join(registerBuildInfo(m, service, version), registerProcess(m)); err != nil {
		return nil, errors.Join(err, shutdown(ctx))
	}
	return shutdown, nil
}

func registerBuildInfo(m metric.Meter, service, version string) error {
	g, err := m.Int64ObservableGauge(service+".build_info", metric.WithDescription("1, with the running version."))
	if err != nil {
		return err
	}
	v := metric.WithAttributes(attribute.String("version", version))
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(g, 1, v)
		return nil
	}, g)
	return err
}

// NewLogger writes JSON lines with the standard's field names, the
// service and version on every line, and the trace id of the request or
// job a line belongs to.
func NewLogger(w io.Writer, service, version string, level slog.Leveler) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				a.Key = "timestamp"
			case slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			}
			return a
		},
	})
	return slog.New(traceHandler{h}).With("service", service, "version", version)
}

// ParseLevel reads a level name (debug, info, warn, error), info for
// anything else.
func ParseLevel(name string) slog.Level {
	var l slog.Level
	if l.UnmarshalText([]byte(name)) != nil {
		return slog.LevelInfo
	}
	return l
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// Handler serves h as one port: a server span per request named
// `METHOD /route/pattern` once the mux has matched it, an incoming
// traceparent continued, and http.server.request.duration by route,
// method, and status. port names the server in the metric.
func Handler(h http.Handler, port string) http.Handler {
	return otelhttp.NewHandler(h, port)
}

// Client is an HTTP client whose every call is a client span that sends
// traceparent.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
}
