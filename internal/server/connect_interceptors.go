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

	"connectrpc.com/connect"
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

func connectHandlerOptions(authManager *auth.Manager) []connect.HandlerOption {
	interceptors := []connect.Interceptor{
		connectRecoveryInterceptor{},
		connectErrorSanitizerInterceptor{},
	}
	telemetryInterceptor, err := newConnectTelemetryInterceptor(
		otel.Meter("github.com/lehigh-university-libraries/scribe/internal/server"),
		otel.Tracer("github.com/lehigh-university-libraries/scribe/internal/server"),
	)
	if err != nil {
		slog.Warn("connect telemetry is unavailable", "error_type", fmt.Sprintf("%T", err))
	} else {
		interceptors = append(interceptors, telemetryInterceptor)
	}
	interceptors = append(interceptors,
		connectLoggingInterceptor{},
		validate.NewInterceptor(),
	)
	if authManager != nil {
		interceptors = append(interceptors, authManager.Interceptor())
	}
	return []connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		connect.WithReadMaxBytes(maxConnectReadBytes),
	}
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

func (interceptor connectTelemetryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (response connect.AnyResponse, err error) {
		procedure := boundedConnectProcedure(req.Spec().Procedure)
		ctx, span := interceptor.startSpan(ctx, procedure)
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				interceptor.finish(ctx, span, procedure, start, connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error")))
				panic(recovered)
			}
			interceptor.finish(ctx, span, procedure, start, err)
		}()
		return next(ctx, req)
	}
}

func (interceptor connectTelemetryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (interceptor connectTelemetryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		procedure := boundedConnectProcedure(conn.Spec().Procedure)
		ctx, span := interceptor.startSpan(ctx, procedure)
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				interceptor.finish(ctx, span, procedure, start, connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error")))
				panic(recovered)
			}
			interceptor.finish(ctx, span, procedure, start, err)
		}()
		return next(ctx, conn)
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

func registerConnectServices(mux *http.ServeMux, handler *Handler, authManager *auth.Manager, opts ...connect.HandlerOption) {
	services := []struct {
		register func(*Handler, ...connect.HandlerOption) (string, http.Handler)
	}{
		{register: func(h *Handler, opts ...connect.HandlerOption) (string, http.Handler) {
			return scribev1connect.NewImageProcessingServiceHandler(h, opts...)
		}},
		{register: func(h *Handler, opts ...connect.HandlerOption) (string, http.Handler) {
			return scribev1connect.NewItemServiceHandler(h, opts...)
		}},
		{register: func(h *Handler, opts ...connect.HandlerOption) (string, http.Handler) {
			return scribev1connect.NewContextServiceHandler(h, opts...)
		}},
		{register: func(h *Handler, opts ...connect.HandlerOption) (string, http.Handler) {
			return scribev1connect.NewAnnotationServiceHandler(h, opts...)
		}},
		{register: func(h *Handler, opts ...connect.HandlerOption) (string, http.Handler) {
			return scribev1connect.NewTranscriptionServiceHandler(h, opts...)
		}},
		{register: func(h *Handler, opts ...connect.HandlerOption) (string, http.Handler) {
			return scribev1connect.NewWebhookServiceHandler(h, opts...)
		}},
	}
	for _, service := range services {
		path, svcHandler := service.register(handler, opts...)
		mux.Handle(path, svcHandler)
	}
	if authManager != nil {
		path, svcHandler := scribev1connect.NewWorkspaceServiceHandler(authManager, opts...)
		mux.Handle(path, svcHandler)
		path, svcHandler = scribev1connect.NewAuthServiceHandler(authManager, opts...)
		mux.Handle(path, svcHandler)
	}
}

// connectErrorSanitizerInterceptor keeps database, provider, and internal
// topology details out of RPC responses. The logging interceptor records only
// categorical failure metadata because error strings can contain provider
// responses, document text, credentials, and persistence topology.
type connectErrorSanitizerInterceptor struct{}

func (connectErrorSanitizerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		response, err := next(ctx, req)
		return response, sanitizeConnectError(err)
	}
}

func (connectErrorSanitizerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (connectErrorSanitizerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return sanitizeConnectError(next(ctx, conn))
	}
}

func sanitizeConnectError(err error) error {
	if err == nil {
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss:
		return connect.NewError(connect.CodeOf(err), fmt.Errorf("internal server error"))
	case connect.CodeUnavailable:
		if message, ok := publicUnavailableConnectMessage(err); ok {
			return connect.NewError(connect.CodeUnavailable, errors.New(message))
		}
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("service unavailable"))
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

func (connectRecoveryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logConnectPanic("connect rpc panic", req.Spec().Procedure, recovered)
				resp = nil
				err = connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error"))
			}
		}()
		return next(ctx, req)
	}
}

func (connectRecoveryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (connectRecoveryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logConnectPanic("connect streaming rpc panic", conn.Spec().Procedure, recovered)
				err = connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error"))
			}
		}()
		return next(ctx, conn)
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

func (connectLoggingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		code := connectLogCode(err)
		attrs := []any{
			"procedure", req.Spec().Procedure,
			"code", code,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if err != nil && connectErrorNeedsDiagnosticLog(connect.CodeOf(err)) {
			attrs = append(attrs, "error_type", fmt.Sprintf("%T", err))
		}
		slog.Info("connect rpc", attrs...)
		return resp, err
	}
}

func (connectLoggingInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (connectLoggingInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		code := connectLogCode(err)
		attrs := []any{
			"procedure", conn.Spec().Procedure,
			"code", code,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if err != nil && connectErrorNeedsDiagnosticLog(connect.CodeOf(err)) {
			attrs = append(attrs, "error_type", fmt.Sprintf("%T", err))
		}
		slog.Info("connect streaming rpc", attrs...)
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
