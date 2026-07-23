package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestConnectErrorSanitizerRedactsInternalDetails(t *testing.T) {
	t.Parallel()

	sensitive := connect.NewError(connect.CodeInternal, context.Canceled)
	sensitive.Meta().Set("X-Database-Host", "private-db.example")
	err := sanitizeConnectError(sensitive)
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want internal", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "internal server error") || strings.Contains(err.Error(), "private-db") || strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("sanitized error leaked internal details: %v", err)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("sanitized error is not a Connect error: %T", err)
	}
	if len(connectErr.Meta()) != 0 {
		t.Fatalf("sanitized metadata = %#v, want empty", connectErr.Meta())
	}
}

func TestConnectLogCodeReportsSuccessfulRPCsAsOK(t *testing.T) {
	t.Parallel()

	if got := connectLogCode(nil); got != "ok" {
		t.Fatalf("successful RPC log code = %q, want ok", got)
	}
	if got := connectLogCode(connect.NewError(connect.CodeInvalidArgument, errors.New("bad request"))); got != "invalid_argument" {
		t.Fatalf("failed RPC log code = %q, want invalid_argument", got)
	}
}

func TestConnectLoggingInterceptorRedactsInternalDiagnostics(t *testing.T) {
	const privateDiagnostic = "PRIVATE_PROVIDER_RESPONSE_/tmp/database.internal"
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))

	handler := (connectLoggingInterceptor{}).WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New(privateDiagnostic))
	})
	_, err := handler(context.Background(), connect.NewRequest(&scribev1.GetItemRequest{}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want internal", connect.CodeOf(err))
	}
	if strings.Contains(logs.String(), privateDiagnostic) || strings.Contains(logs.String(), "database.internal") {
		t.Fatalf("connect log exposed internal diagnostic: %s", logs.String())
	}
	for _, safeMetadata := range []string{
		`"msg":"connect rpc"`,
		`"code":"internal"`,
		`"error_type":"*connect.Error"`,
	} {
		if !strings.Contains(logs.String(), safeMetadata) {
			t.Fatalf("connect log omitted safe failure metadata %s: %s", safeMetadata, logs.String())
		}
	}
}

func TestImageProcessingInternalErrorsAreSanitized(t *testing.T) {
	t.Parallel()

	err := imageProcessingConnectError("process image", errors.New("/tmp/private-image: database.internal"))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want internal", connect.CodeOf(err))
	}
	sanitized := sanitizeConnectError(err)
	if strings.Contains(sanitized.Error(), "/tmp/private-image") || strings.Contains(sanitized.Error(), "database.internal") {
		t.Fatalf("sanitized processing error leaked internal details: %v", sanitized)
	}
}

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
	const sensitivePanic = "PRIVATE_DOCUMENT_CONTENT_/tmp/provider-response.json"
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))

	handler := (connectRecoveryInterceptor{}).WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		panic(sensitivePanic)
	})

	err := handler(context.Background(), fakeConnectStreamingConn{
		spec: connect.Spec{Procedure: "/scribe.v1.TranscriptionService/StreamTranscriptionJob"},
	})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeInternal, err)
	}
	if strings.Contains(logs.String(), sensitivePanic) || strings.Contains(logs.String(), "/tmp/provider-response.json") {
		t.Fatalf("panic log exposed recovered value: %s", logs.String())
	}
	for _, safeMetadata := range []string{
		`"msg":"connect streaming rpc panic"`,
		`"panic_type":"string"`,
		`"panic_function":`,
		`"panic_file":"connect_interceptors_test.go"`,
		`"panic_line":`,
	} {
		if !strings.Contains(logs.String(), safeMetadata) {
			t.Fatalf("panic log omitted safe location metadata %s: %s", safeMetadata, logs.String())
		}
	}
}

func TestStructuralAnnotationRequestBoundsRunBeforeHandlers(t *testing.T) {
	t.Parallel()

	_, service := scribev1connect.NewAnnotationServiceHandler(&Handler{}, connectHandlerOptions(nil)...)
	server := httptest.NewServer(service)
	t.Cleanup(server.Close)
	client := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, server.URL)

	baseSplitRequest := func(words []string) *scribev1.SplitLineIntoWordsRequest {
		return &scribev1.SplitLineIntoWordsRequest{
			ItemImageId:          1,
			AnnotationPageJson:   `{}`,
			SelectedAnnotationId: "line",
			Words:                words,
		}
	}
	tooManyWords := make([]string, 10001)
	for index := range tooManyWords {
		tooManyWords[index] = "x"
	}
	_, err := client.SplitLineIntoWords(context.Background(), connect.NewRequest(baseSplitRequest(tooManyWords)))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("too many split words code = %v, want invalid_argument (err=%v)", connect.CodeOf(err), err)
	}

	_, err = client.SplitLineIntoWords(context.Background(), connect.NewRequest(baseSplitRequest([]string{
		strings.Repeat("é", 8193), // 16,386 UTF-8 bytes.
	})))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized split word code = %v, want invalid_argument (err=%v)", connect.CodeOf(err), err)
	}

	tooManyIDs := make([]string, 10001)
	for index := range tooManyIDs {
		tooManyIDs[index] = "annotation-" + strconv.Itoa(index)
	}
	joinRequests := []struct {
		name string
		call func() error
	}{
		{
			name: "lines",
			call: func() error {
				_, callErr := client.JoinLines(context.Background(), connect.NewRequest(&scribev1.JoinLinesRequest{
					ItemImageId:           1,
					AnnotationPageJson:    `{}`,
					SelectedAnnotationIds: tooManyIDs,
				}))
				return callErr
			},
		},
		{
			name: "words",
			call: func() error {
				_, callErr := client.JoinWordsIntoLine(context.Background(), connect.NewRequest(&scribev1.JoinWordsIntoLineRequest{
					ItemImageId:           1,
					AnnotationPageJson:    `{}`,
					SelectedAnnotationIds: tooManyIDs,
				}))
				return callErr
			},
		},
	}
	for _, request := range joinRequests {
		if err := request.call(); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("too many join %s IDs code = %v, want invalid_argument (err=%v)", request.name, connect.CodeOf(err), err)
		}
	}
}
