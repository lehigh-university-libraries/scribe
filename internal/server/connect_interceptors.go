package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/validate"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// The outer request-limit middleware applies the tighter per-procedure limit.
// RPC request compression is rejected there, so this decoded-message ceiling
// can safely match the largest accepted request class without allowing a
// compressed body to bypass a smaller route limit.
const maxConnectReadBytes = maxImageRequestBytes

func connectServerInterceptors(authManager *auth.Manager) []connect.ServerInterceptor {
	interceptors := []connect.ServerInterceptor{
		connectRecoveryInterceptor{}.Wrap,
		connectErrorSanitizerInterceptor{}.Wrap,
	}
	telemetryInterceptor, err := newConnectTelemetryInterceptor(
		otel.Meter("github.com/lehigh-university-libraries/scribe/internal/server"),
		otel.Tracer("github.com/lehigh-university-libraries/scribe/internal/server"),
	)
	if err != nil {
		slog.Warn("connect telemetry is unavailable", "error_type", fmt.Sprintf("%T", err))
	} else {
		interceptors = append(interceptors, telemetryInterceptor.Wrap)
	}
	interceptors = append(interceptors,
		connectLoggingInterceptor{}.Wrap,
		validate.NewServerInterceptor(),
	)
	if authManager != nil {
		interceptors = append(interceptors, authManager.Interceptor())
	}
	return interceptors
}

type connectTelemetryInterceptor struct {
	tracer   trace.Tracer
	requests metric.Int64Counter
	duration metric.Float64Histogram
}

var connectDurationBoundaries = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 90, 120,
}

func newConnectTelemetryInterceptor(meter metric.Meter, tracer trace.Tracer) (connectTelemetryInterceptor, error) {
	requests, err := meter.Int64Counter(
		"scribe.connect.server.requests",
		metric.WithDescription("Number of completed Connect RPC server requests."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return connectTelemetryInterceptor{}, err
	}
	duration, err := meter.Float64Histogram(
		"scribe.connect.server.duration",
		metric.WithDescription("Connect RPC server request duration in seconds."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(connectDurationBoundaries...),
	)
	if err != nil {
		return connectTelemetryInterceptor{}, err
	}
	return connectTelemetryInterceptor{
		tracer:   tracer,
		requests: requests,
		duration: duration,
	}, nil
}

func (interceptor connectTelemetryInterceptor) Wrap(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) (err error) {
		procedure := boundedConnectProcedure(spec.Procedure)
		ctx, span := interceptor.startSpan(ctx, procedure)
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				interceptor.finish(ctx, span, procedure, start, connect.Errorf(connect.CodeInternal, "internal server error"))
				panic(recovered)
			}
			interceptor.finish(ctx, span, procedure, start, err)
		}()
		return next(ctx, spec, stream)
	}
}

func (interceptor connectTelemetryInterceptor) startSpan(ctx context.Context, procedure connectProcedure) (context.Context, trace.Span) {
	return interceptor.tracer.Start(
		ctx,
		procedure.name,
		// The public caller controls both flags and trace ID in traceparent.
		// Begin a new root so it cannot preselect IDs that defeat deterministic
		// ratio sampling or force export volume.
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "connect_rpc"),
			attribute.String("rpc.service", procedure.service),
			attribute.String("rpc.method", procedure.method),
		),
	)
}

func (interceptor connectTelemetryInterceptor) finish(ctx context.Context, span trace.Span, procedure connectProcedure, start time.Time, err error) {
	code := connectLogCode(err)
	attrs := []attribute.KeyValue{
		attribute.String("rpc.service", procedure.service),
		attribute.String("rpc.method", procedure.method),
		attribute.String("rpc.connect.status_code", code),
	}
	interceptor.requests.Add(ctx, 1, metric.WithAttributes(attrs...))
	interceptor.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	span.SetAttributes(attribute.String("rpc.connect.status_code", code))
	if err != nil {
		// Do not RecordError: OpenTelemetry error events include error strings,
		// which can contain document content, credentials, or provider bodies.
		span.SetStatus(codes.Error, code)
	}
	span.End()
}

type connectProcedure struct {
	name    string
	service string
	method  string
}

// boundedConnectProcedure admits only methods present in Scribe's compiled
// protobuf descriptors. An attacker cannot create a metric or span series by
// varying an unknown URL below a generated service prefix.
func boundedConnectProcedure(raw string) connectProcedure {
	const unknown = "unknown"
	parts := strings.Split(strings.TrimPrefix(raw, "/"), "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "scribe.v1.") {
		return connectProcedure{name: unknown, service: unknown, method: unknown}
	}
	serviceName := protoreflect.FullName(parts[0])
	methodName := protoreflect.Name(parts[1])
	if !serviceName.IsValid() || !methodName.IsValid() {
		return connectProcedure{name: unknown, service: unknown, method: unknown}
	}
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(serviceName)
	if err != nil {
		return connectProcedure{name: unknown, service: unknown, method: unknown}
	}
	service, ok := descriptor.(protoreflect.ServiceDescriptor)
	if !ok || service.Methods().ByName(methodName) == nil {
		return connectProcedure{name: unknown, service: unknown, method: unknown}
	}
	return connectProcedure{name: raw, service: parts[0], method: parts[1]}
}

func registerConnectServices(mux *http.ServeMux, handler *Handler, authManager *auth.Manager, interceptors ...connect.ServerInterceptor) {
	server := connect.NewServer(interceptors...)
	scribev1connect.RegisterImageProcessingServiceHandler(server, handler)
	scribev1connect.RegisterItemServiceHandler(server, handler)
	scribev1connect.RegisterContextServiceHandler(server, handler)
	scribev1connect.RegisterAnnotationServiceHandler(server, handler)
	scribev1connect.RegisterTranscriptionServiceHandler(server, handler)
	scribev1connect.RegisterWebhookServiceHandler(server, handler)
	if authManager != nil {
		scribev1connect.RegisterWorkspaceServiceHandler(server, authManager)
		scribev1connect.RegisterAuthServiceHandler(server, authManager)
	}
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(maxConnectReadBytes))
}

// connectErrorSanitizerInterceptor keeps database, provider, and internal
// topology details out of RPC responses. The logging interceptor records only
// categorical failure metadata because error strings can contain provider
// responses, document text, credentials, and persistence topology.
type connectErrorSanitizerInterceptor struct{}

func (connectErrorSanitizerInterceptor) Wrap(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		return sanitizeConnectError(next(ctx, spec, stream))
	}
}

func sanitizeConnectError(err error) error {
	if err == nil {
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss:
		return connect.Errorf(connect.CodeOf(err), "internal server error")
	case connect.CodeUnavailable:
		if message, ok := publicUnavailableConnectMessage(err); ok {
			return connect.NewError(connect.CodeUnavailable, message)
		}
		return connect.Errorf(connect.CodeUnavailable, "service unavailable")
	default:
		return err
	}
}

// publicUnavailableConnectMessage is deliberately an exact allowlist. These
// messages identify only which public manifest input failed and contain no
// upstream response, URL, provider, credential, or topology detail.
func publicUnavailableConnectMessage(err error) (string, bool) {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return "", false
	}
	switch connectErr.Message() {
	case manifestDocumentUnavailableMessage, manifestHOCRUnavailableMessage:
		return connectErr.Message(), true
	default:
		return "", false
	}
}

type connectRecoveryInterceptor struct{}

func (connectRecoveryInterceptor) Wrap(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logConnectPanic("connect streaming rpc panic", spec.Procedure, recovered)
				err = connect.Errorf(connect.CodeInternal, "internal server error")
			}
		}()
		return next(ctx, spec, stream)
	}
}

// logConnectPanic records enough stack metadata to locate a defect without
// formatting the recovered value. Panic values can contain request bodies,
// provider responses, credentials, or document text.
func logConnectPanic(message, procedure string, recovered any) {
	attrs := []any{
		"procedure", procedure,
		"panic_type", fmt.Sprintf("%T", recovered),
	}
	if function, file, line := connectPanicLocation(); function != "" {
		attrs = append(attrs,
			"panic_function", function,
			"panic_file", file,
			"panic_line", line,
		)
	}
	slog.Error(message, attrs...)
}

func connectPanicLocation() (string, string, int) {
	pcs := make([]uintptr, 32)
	count := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:count])
	for {
		frame, more := frames.Next()
		if frame.Function != "" &&
			!strings.HasPrefix(frame.Function, "runtime.") &&
			!strings.Contains(frame.Function, "connectRecoveryInterceptor") &&
			!strings.HasSuffix(frame.Function, ".logConnectPanic") &&
			!strings.HasSuffix(frame.Function, ".connectPanicLocation") {
			return frame.Function, filepath.Base(frame.File), frame.Line
		}
		if !more {
			return "", "", 0
		}
	}
}

type connectLoggingInterceptor struct{}

func (connectLoggingInterceptor) Wrap(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		start := time.Now()
		err := next(ctx, spec, stream)
		code := connectLogCode(err)
		attrs := []any{
			"procedure", spec.Procedure,
			"code", code,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if err != nil && connectErrorNeedsDiagnosticLog(connect.CodeOf(err)) {
			attrs = append(attrs, "error_type", fmt.Sprintf("%T", err))
		}
		slog.Info("connect rpc", attrs...)
		return err
	}
}

func connectLogCode(err error) string {
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String()
}

func connectErrorNeedsDiagnosticLog(code connect.Code) bool {
	switch code {
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss, connect.CodeUnavailable:
		return true
	default:
		return false
	}
}
