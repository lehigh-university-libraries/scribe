package auth

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
)

type fakeStreamingHandlerConn struct {
	spec connect.Spec
}

func (c fakeStreamingHandlerConn) Spec() connect.Spec { return c.spec }

func (fakeStreamingHandlerConn) Peer() connect.Peer { return connect.Peer{} }

func (fakeStreamingHandlerConn) Receive(any) error { return nil }

func (fakeStreamingHandlerConn) RequestHeader() http.Header { return http.Header{} }

func (fakeStreamingHandlerConn) Send(any) error { return nil }

func (fakeStreamingHandlerConn) ResponseHeader() http.Header { return http.Header{} }

func (fakeStreamingHandlerConn) ResponseTrailer() http.Header { return http.Header{} }

func TestAuthInterceptorAuthorizesStreamingHandlers(t *testing.T) {
	t.Parallel()

	manager := &Manager{}
	interceptor := manager.Interceptor()
	called := false
	handler := interceptor.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		called = true
		return nil
	})
	ctx := WithPrincipal(context.Background(), Principal{
		Authenticated: true,
		WorkspaceRole: "read",
	})

	err := handler(ctx, fakeStreamingHandlerConn{
		spec: connect.Spec{Procedure: "/scribe.v1.TranscriptionService/ListTranscriptionJobs"},
	})
	if err != nil {
		t.Fatalf("streaming handler returned error: %v", err)
	}
	if !called {
		t.Fatal("streaming handler was not called after authorization")
	}
}

func TestAuthInterceptorRejectsAnonymousStreamingHandlers(t *testing.T) {
	t.Parallel()

	manager := &Manager{}
	interceptor := manager.Interceptor()
	called := false
	handler := interceptor.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		called = true
		return nil
	})

	err := handler(context.Background(), fakeStreamingHandlerConn{
		spec: connect.Spec{Procedure: "/scribe.v1.TranscriptionService/ListTranscriptionJobs"},
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
	if called {
		t.Fatal("streaming handler was called for anonymous principal")
	}
}
