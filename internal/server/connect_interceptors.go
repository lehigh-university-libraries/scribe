package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func connectHandlerOptions(authManager *auth.Manager) []connect.HandlerOption {
	interceptors := []connect.Interceptor{
		connect.UnaryInterceptorFunc(connectRecoveryInterceptor),
		connect.UnaryInterceptorFunc(connectLoggingInterceptor),
		validate.NewInterceptor(),
	}
	if authManager != nil {
		interceptors = append(interceptors, authManager.Interceptor())
	}
	return []connect.HandlerOption{connect.WithInterceptors(interceptors...)}
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
	}
}

func connectRecoveryInterceptor(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("connect rpc panic",
					"procedure", req.Spec().Procedure,
					"panic", fmt.Sprint(recovered),
				)
				resp = nil
				err = connect.NewError(connect.CodeInternal, fmt.Errorf("internal server error"))
			}
		}()
		return next(ctx, req)
	}
}

func connectLoggingInterceptor(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		code := connect.CodeOf(err)
		attrs := []any{
			"procedure", req.Spec().Procedure,
			"code", fmt.Sprint(code),
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if err != nil {
			attrs = append(attrs, "error", err.Error())
		}
		slog.Info("connect rpc", attrs...)
		return resp, err
	}
}
