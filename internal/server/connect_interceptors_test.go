package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestConnectErrorSanitizerPreservesOnlyAllowlistedManifestAvailability(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "manifest document",
			err:  manifestImportConnectError(fmt.Errorf("%w: private upstream detail", errManifestDocumentUnavailable)),
			want: manifestDocumentUnavailableMessage,
		},
		{
			name: "manifest hOCR",
			err:  manifestImportConnectError(fmt.Errorf("%w: private upstream detail", errManifestHOCRSourceUnavailable)),
			want: manifestHOCRUnavailableMessage,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var sourceConnectErr *connect.Error
			if !errors.As(test.err, &sourceConnectErr) {
				t.Fatalf("source error type = %T, want *connect.Error", test.err)
			}
			sourceConnectErr.Meta().Set("X-Private-Upstream", "database.internal")
			detail, detailErr := connect.NewErrorDetail(&scribev1.GetItemRequest{ItemId: "PRIVATE_ITEM_ID"})
			if detailErr != nil {
				t.Fatalf("create private error detail: %v", detailErr)
			}
			sourceConnectErr.AddDetail(detail)
			sanitized := sanitizeConnectError(fmt.Errorf("outer private topology: %w", test.err))
			var connectErr *connect.Error
			if !errors.As(sanitized, &connectErr) {
				t.Fatalf("sanitized error type = %T, want *connect.Error", sanitized)
			}
			if connectErr.Code() != connect.CodeUnavailable || connectErr.Message() != test.want {
				t.Fatalf("sanitized error = %v/%q, want unavailable/%q", connectErr.Code(), connectErr.Message(), test.want)
			}
			if len(connectErr.Meta()) != 0 || len(connectErr.Details()) != 0 {
				t.Fatalf("sanitized error retained metadata or details: metadata=%v details=%v", connectErr.Meta(), connectErr.Details())
			}
			for _, privateDetail := range []string{"private upstream detail", "outer private topology", "database.internal", "PRIVATE_ITEM_ID"} {
				if strings.Contains(sanitized.Error(), privateDetail) {
					t.Fatalf("sanitized error retained private detail %q: %v", privateDetail, sanitized)
				}
			}
		})
	}

	for _, message := range []string{
		"manifest source is temporarily unavailable",
		manifestDocumentUnavailableMessage + " private suffix",
		"transcription provider is temporarily unavailable",
	} {
		sanitized := sanitizeConnectError(connect.NewError(connect.CodeUnavailable, errors.New(message)))
		var connectErr *connect.Error
		if !errors.As(sanitized, &connectErr) || connectErr.Message() != "service unavailable" {
			t.Fatalf("non-allowlisted message %q sanitized to %v, want generic unavailable", message, sanitized)
		}
	}
}

func TestConnectErrorSanitizerPreservesManifestAvailabilityOnWire(t *testing.T) {
	t.Parallel()

	for _, message := range []string{manifestDocumentUnavailableMessage, manifestHOCRUnavailableMessage} {
		message := message
		t.Run(message, func(t *testing.T) {
			t.Parallel()
			sourceErr := connect.NewError(connect.CodeUnavailable, errors.New(message))
			sourceErr.Meta().Set("X-Private-Upstream", "database.internal")
			detail, err := connect.NewErrorDetail(&scribev1.GetItemRequest{ItemId: "PRIVATE_ITEM_ID"})
			if err != nil {
				t.Fatalf("create private error detail: %v", err)
			}
			sourceErr.AddDetail(detail)
			handler := connect.NewUnaryHandler(
				"/test.v1.TestService/GetItem",
				func(context.Context, *connect.Request[scribev1.GetItemRequest]) (*connect.Response[scribev1.GetItemResponse], error) {
					return nil, fmt.Errorf("outer private topology: %w", sourceErr)
				},
				connect.WithInterceptors(connectErrorSanitizerInterceptor{}),
			)
			request := httptest.NewRequest(http.MethodPost, "/test.v1.TestService/GetItem", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Connect-Protocol-Version", "1")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("HTTP status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			var payload struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details []any  `json:"details"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode Connect error: %v", err)
			}
			if payload.Code != connect.CodeUnavailable.String() || payload.Message != message || len(payload.Details) != 0 {
				t.Fatalf("Connect payload = %#v, want unavailable/%q without details", payload, message)
			}
			for _, privateDetail := range []string{"outer private topology", "database.internal", "PRIVATE_ITEM_ID", "X-Private-Upstream"} {
				if strings.Contains(response.Body.String(), privateDetail) || response.Header().Get("X-Private-Upstream") != "" {
					t.Fatalf("Connect response retained private detail %q", privateDetail)
				}
			}
		})
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
