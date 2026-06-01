package server

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
)

type fakeConnectStreamingConn struct {
	spec connect.Spec
}

func (c fakeConnectStreamingConn) Spec() connect.Spec { return c.spec }

func (fakeConnectStreamingConn) Peer() connect.Peer { return connect.Peer{} }

func (fakeConnectStreamingConn) Receive(any) error { return nil }

func (fakeConnectStreamingConn) RequestHeader() http.Header { return http.Header{} }

func (fakeConnectStreamingConn) Send(any) error { return nil }

func (fakeConnectStreamingConn) ResponseHeader() http.Header { return http.Header{} }

func (fakeConnectStreamingConn) ResponseTrailer() http.Header { return http.Header{} }

func TestConnectRecoveryInterceptorRecoversStreamingPanics(t *testing.T) {
	t.Parallel()

	handler := (connectRecoveryInterceptor{}).WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		panic("boom")
	})

	err := handler(context.Background(), fakeConnectStreamingConn{
		spec: connect.Spec{Procedure: "/scribe.v1.TranscriptionService/StreamTranscriptionJob"},
	})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeInternal, err)
	}
}
