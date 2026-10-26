package auth

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

type fakeStreamingHandlerConn struct{}

func (fakeStreamingHandlerConn) SendHeaders() error { return nil }

func (fakeStreamingHandlerConn) Receive(any) error { return nil }

func (fakeStreamingHandlerConn) Send(any) error { return nil }

func TestAuthInterceptorAuthorizesStreamingHandlers(t *testing.T) {
	t.Parallel()

	manager := &Manager{}
	interceptor := manager.Interceptor()
	called := false
	handler := interceptor(func(context.Context, connect.Spec, connect.ServerStream) error {
		called = true
		return nil
	})
	ctx := WithPrincipal(context.Background(), Principal{
		Authenticated: true,
		WorkspaceRole: "read",
	})

	err := handler(ctx, connect.Spec{Procedure: "/scribe.v1.TranscriptionService/ListTranscriptionJobs"}, fakeStreamingHandlerConn{})
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
	handler := interceptor(func(context.Context, connect.Spec, connect.ServerStream) error {
		called = true
		return nil
	})

	err := handler(context.Background(), connect.Spec{Procedure: "/scribe.v1.TranscriptionService/ListTranscriptionJobs"}, fakeStreamingHandlerConn{})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeUnauthenticated, err)
	}
	if called {
		t.Fatal("streaming handler was called for anonymous principal")
	}
}

func TestAuthInterceptorChecksDecodedResourceBeforeApplicationHandler(t *testing.T) {
	t.Parallel()
	manager := &Manager{}
	called := false
	handler := manager.Interceptor()(func(ctx context.Context, _ connect.Spec, stream connect.ServerStream) error {
		if err := stream.Receive(&scribev1.GetItemRequest{}); err != nil {
			return err
		}
		called = true
		return nil
	})
	ctx := WithPrincipal(context.Background(), Principal{Authenticated: true, WorkspaceRole: "read"})
	err := handler(ctx, connect.Spec{Procedure: "/scribe.v1.ItemService/GetItem"}, fakeStreamingHandlerConn{})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || called {
		t.Fatalf("missing resource ID reached handler: called=%v error=%v", called, err)
	}
}
