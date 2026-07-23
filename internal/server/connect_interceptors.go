package server

import (
	"context"
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
		connectLoggingInterceptor{},
		validate.NewInterceptor(),
	}
	if authManager != nil {
		interceptors = append(interceptors, authManager.Interceptor())
	}
	return []connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		connect.WithReadMaxBytes(maxConnectReadBytes),
	}
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
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("service unavailable"))
	default:
		return err
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
