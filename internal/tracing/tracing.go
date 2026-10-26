// Package tracing wires OpenTelemetry: OTLP/HTTP export configured with the
// standard OTEL_* environment variables, HTTP and database spans, and trace
// ids in log records. Without an OTLP endpoint everything is a no-op.
package tracing

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var enabled bool

// Enabled reports whether spans are exported.
func Enabled() bool { return enabled }

// Tracer returns the application tracer.
func Tracer() trace.Tracer { return otel.Tracer("github.com/damhau/awxnext") }

// Setup configures the global tracer provider when an OTLP endpoint is set.
// The returned function flushes and stops the exporter.
func Setup(ctx context.Context, version, node string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = "awxnext"
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", name),
		attribute.String("service.version", version),
		attribute.String("service.instance.id", node),
	))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	enabled = true
	return tp.Shutdown, nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Middleware starts a server span per request, continuing incoming W3C trace context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := Tracer().Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("url.path", r.URL.Path)))
		defer span.End()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(ctx))
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			span.SetName(r.Method + " " + rc.RoutePattern())
			span.SetAttributes(attribute.String("http.route", rc.RoutePattern()))
		}
		status := sw.status
		if status == 0 {
			status = 200
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}

// QueryTracer records database queries as spans (pgx.QueryTracer).
type QueryTracer struct{}

type spanKey struct{}

func (QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !enabled || !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	stmt := strings.Join(strings.Fields(data.SQL), " ")
	if len(stmt) > 500 {
		stmt = stmt[:500]
	}
	op, _, _ := strings.Cut(stmt, " ")
	ctx, span := Tracer().Start(ctx, "db "+strings.ToUpper(op), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.statement", stmt)))
	return context.WithValue(ctx, spanKey{}, span)
}

func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok {
		return
	}
	if data.Err != nil && data.Err != pgx.ErrNoRows {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

// StartSpan starts an internal span.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// EndSpan ends a span, recording err.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// logHandler adds trace and span ids to log records.
type logHandler struct{ slog.Handler }

// LogHandler wraps h so records logged with a traced context carry trace_id and span_id.
func LogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h logHandler) WithAttrs(a []slog.Attr) slog.Handler { return logHandler{h.Handler.WithAttrs(a)} }
func (h logHandler) WithGroup(n string) slog.Handler      { return logHandler{h.Handler.WithGroup(n)} }

// Since is a helper for duration attributes.
func Since(t time.Time) attribute.KeyValue {
	return attribute.Float64("duration_seconds", time.Since(t).Seconds())
}
