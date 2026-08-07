package providerregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
)

func TestGeminiClientUsesModelCompatibleRequestAndRegisteredVendorBase(t *testing.T) {
	const credential = "workspace-gemini-key"
	legacyTemperature := 0.2
	tests := []struct {
		name            string
		model           string
		wantTemperature *float64
	}{
		{name: "Gemini 3 uses model default", model: "gemini-3.5-flash"},
		{name: "legacy model keeps configured sampling", model: "gemini-2.5-flash", wantTemperature: &legacyTemperature},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1beta/models/"+test.model+":generateContent" {
					t.Errorf("path = %q", request.URL.Path)
				}
				if request.URL.RawQuery != "" || request.Header.Get("x-goog-api-key") != credential {
					t.Errorf("credential placement: query=%q header=%q", request.URL.RawQuery, request.Header.Get("x-goog-api-key"))
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				assertGeminiImageRequest(t, payload)
				configuration, ok := payload["generationConfig"].(map[string]any)
				if !ok {
					t.Fatalf("generationConfig = %#v", payload["generationConfig"])
				}
				temperature, hasTemperature := configuration["temperature"].(float64)
				if test.wantTemperature == nil && hasTemperature {
					t.Errorf("deprecated Gemini 3 temperature was serialized: %v", temperature)
				}
				if test.wantTemperature != nil && (!hasTemperature || temperature != *test.wantTemperature) {
					t.Errorf("temperature = %v, present=%v, want %v", temperature, hasTemperature, *test.wantTemperature)
				}
				_ = json.NewEncoder(w).Encode(geminiResponse("STOP", "café 世界"))
			}))
			defer server.Close()

			descriptor := testGeminiDescriptor(test.model, server.URL+"/v1beta")
			client, err := descriptor.NewClient(test.model)
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithCredential(context.Background(), "gemini", providerAPIKeyField, credential)
			result, err := client.Extract(ctx, providers.Request{
				Model: test.model, Prompt: "transcribe", Temperature: legacyTemperature,
				Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(result.Text) != "café 世界" || result.EffectiveModel != "gemini-resolved" {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestGeminiClientRewritesEveryResolutionFallbackAttempt(t *testing.T) {
	var configurations []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		configuration, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("generationConfig = %#v", payload["generationConfig"])
		}
		configurations = append(configurations, configuration)
		if len(configurations) == 1 {
			_ = json.NewEncoder(w).Encode(geminiResponse("MAX_TOKENS", "retry"))
			return
		}
		_ = json.NewEncoder(w).Encode(geminiResponse("STOP", "finished"))
	}))
	defer server.Close()

	const model = "gemini-3.5-flash"
	descriptor := testGeminiDescriptor(model, server.URL+"/v1beta")
	client, err := descriptor.NewClient(model)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCredential(context.Background(), "gemini", providerAPIKeyField, "credential")
	result, err := client.Extract(ctx, providers.Request{
		Model: model, Prompt: "transcribe", Temperature: 0.8,
		Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "finished" || len(configurations) != 2 {
		t.Fatalf("result/configurations = %#v/%#v", result, configurations)
	}
	for index, configuration := range configurations {
		if _, ok := configuration["temperature"]; ok {
			t.Fatalf("attempt %d retained temperature: %#v", index+1, configuration)
		}
	}
	if got := configurations[0]["mediaResolution"]; got != nil {
		t.Fatalf("initial mediaResolution = %#v, want omitted", got)
	}
	if got := configurations[1]["mediaResolution"]; got != "MEDIA_RESOLUTION_HIGH" {
		t.Fatalf("fallback mediaResolution = %#v", got)
	}
}

func TestOmitGeminiDeprecatedSamplingPreservesEveryOtherByte(t *testing.T) {
	const prefix = `{"contents":[{"parts":[{"text":"escaped \"temperature\":0.9 stays"}]}],"generationConfig":{`
	tests := []struct {
		name        string
		temperature string
		remainder   string
	}{
		{name: "temperature only", temperature: "0", remainder: `}}`},
		{name: "negative exponent", temperature: "-1.25e-7", remainder: `}}`},
		{name: "unspecified media resolution", temperature: "0.1", remainder: `"mediaResolution":"MEDIA_RESOLUTION_UNSPECIFIED"}}`},
		{name: "media resolution", temperature: "0.2", remainder: `"mediaResolution":"MEDIA_RESOLUTION_HIGH"}}`},
		{name: "medium media resolution", temperature: "0.5", remainder: `"mediaResolution":"MEDIA_RESOLUTION_MEDIUM"}}`},
		{name: "thinking", temperature: "1", remainder: `"thinkingConfig":{"includeThoughts":true}}}`},
		{name: "media and thinking", temperature: "2", remainder: `"mediaResolution":"MEDIA_RESOLUTION_LOW","thinkingConfig":{"includeThoughts":true}}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			separator := ""
			if test.remainder != `}}` {
				separator = ","
			}
			member := `"temperature":` + test.temperature + separator
			body := []byte(prefix + member + test.remainder)
			originalLength := len(body)
			if err := omitGeminiDeprecatedSampling(body); err != nil {
				t.Fatal(err)
			}
			want := prefix + strings.Repeat(" ", len(member)) + test.remainder
			if string(body) != want {
				t.Fatalf("adapted body changed non-temperature bytes:\n got: %q\nwant: %q", body, want)
			}
			if len(body) != originalLength {
				t.Fatalf("adapted length = %d, want %d", len(body), originalLength)
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("adapted body is invalid JSON: %v", err)
			}
			configuration := decoded["generationConfig"].(map[string]any)
			if _, exists := configuration["temperature"]; exists {
				t.Fatalf("temperature remains in %#v", configuration)
			}
		})
	}
}

func TestGeminiDefaultSamplingTransportPreservesRequestLengthAndReplay(t *testing.T) {
	body := `{"contents":[],"generationConfig":{"temperature":0.2,"mediaResolution":"MEDIA_RESOLUTION_HIGH"}}`
	want := `{"contents":[],"generationConfig":{                  "mediaResolution":"MEDIA_RESOLUTION_HIGH"}}`
	request := geminiRequest(t, strings.NewReader(body), int64(len(body)))
	originalGetBody := request.GetBody
	getBodyCalls := 0
	request.GetBody = func() (io.ReadCloser, error) {
		getBodyCalls++
		return originalGetBody()
	}

	transport := geminiDefaultSamplingTransport{
		maxRequestBytes: int64(len(body)),
		next: roundTripperFunc(func(adapted *http.Request) (*http.Response, error) {
			if adapted == request {
				t.Fatal("RoundTrip mutated and forwarded the caller's request")
			}
			if adapted.ContentLength != request.ContentLength || adapted.ContentLength != int64(len(want)) {
				t.Fatalf("ContentLength = %d, want %d", adapted.ContentLength, len(want))
			}
			first, err := io.ReadAll(adapted.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := adapted.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if string(first) != want {
				t.Fatalf("first body = %q, want %q", first, want)
			}
			for retry := 0; retry < 2; retry++ {
				replay, err := adapted.GetBody()
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := io.ReadAll(replay)
				if err != nil {
					t.Fatal(err)
				}
				_ = replay.Close()
				if string(replayed) != want {
					t.Fatalf("retry %d body = %q, want %q", retry+1, replayed, want)
				}
			}
			return successfulGeminiHTTPResponse(adapted), nil
		}),
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if getBodyCalls != 2 {
		t.Fatalf("original GetBody calls = %d, want 2", getBodyCalls)
	}
}

func TestGeminiDefaultSamplingTransportPreservesNilGetBody(t *testing.T) {
	body := `{"contents":[],"generationConfig":{"temperature":0}}`
	request := geminiRequest(t, strings.NewReader(body), int64(len(body)))
	request.GetBody = nil
	transport := geminiDefaultSamplingTransport{
		maxRequestBytes: int64(len(body)),
		next: roundTripperFunc(func(adapted *http.Request) (*http.Response, error) {
			if adapted.GetBody != nil {
				t.Fatal("nil GetBody became replayable")
			}
			if _, err := io.Copy(io.Discard, adapted.Body); err != nil {
				t.Fatal(err)
			}
			_ = adapted.Body.Close()
			return successfulGeminiHTTPResponse(adapted), nil
		}),
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestGeminiDefaultSamplingTransportStreamsLargeBodyWithSmallTail(t *testing.T) {
	const payloadBytes int64 = 16 << 20
	prefix := `{"contents":[{"parts":[{"text":"`
	suffix := `"}]}],"generationConfig":{"temperature":0.2,"mediaResolution":"MEDIA_RESOLUTION_MEDIUM"}}`
	wantSuffix := `"}]}],"generationConfig":{                  "mediaResolution":"MEDIA_RESOLUTION_MEDIUM"}}`
	length := int64(len(prefix)+len(suffix)) + payloadBytes
	source := &trackingReadCloser{reader: io.MultiReader(
		strings.NewReader(prefix),
		&repeatedByteReader{remaining: payloadBytes, value: 'a'},
		strings.NewReader(suffix),
	)}
	request := geminiRequest(t, source, length)
	request.GetBody = nil

	wantDigest := sha256.New()
	_, _ = io.WriteString(wantDigest, prefix)
	_, _ = io.Copy(wantDigest, &repeatedByteReader{remaining: payloadBytes, value: 'a'})
	_, _ = io.WriteString(wantDigest, wantSuffix)
	transport := geminiDefaultSamplingTransport{
		maxRequestBytes: length,
		next: roundTripperFunc(func(adapted *http.Request) (*http.Response, error) {
			if source.bytesRead != 0 {
				t.Fatalf("adapter consumed %d source bytes before invoking next transport", source.bytesRead)
			}
			stream, ok := adapted.Body.(*geminiSamplingBody)
			if !ok {
				t.Fatalf("adapted body type = %T", adapted.Body)
			}
			if len(stream.buffer) != maxGeminiSamplingTailBytes+geminiStreamReadBytes {
				t.Fatalf("stream buffer = %d bytes", len(stream.buffer))
			}
			gotDigest := sha256.New()
			written, err := io.Copy(gotDigest, adapted.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = adapted.Body.Close()
			if written != length {
				t.Fatalf("streamed bytes = %d, want %d", written, length)
			}
			if !bytes.Equal(gotDigest.Sum(nil), wantDigest.Sum(nil)) {
				t.Fatal("large streamed body changed outside the temperature member")
			}
			return successfulGeminiHTTPResponse(adapted), nil
		}),
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if source.bytesRead != length || !source.closed {
		t.Fatalf("source read/closed = %d/%v, want %d/true", source.bytesRead, source.closed, length)
	}
}

func TestGeminiDefaultSamplingTransportRejectsInvalidEnvelopeBeforeNextTransport(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		limit         int64
		contentType   string
		wantErr       error
	}{
		{name: "unknown length", contentLength: -1, limit: 100, contentType: "application/json", wantErr: errGeminiRequestLength},
		{name: "empty length", contentLength: 0, limit: 100, contentType: "application/json", wantErr: errGeminiRequestLength},
		{name: "oversize", contentLength: 101, limit: 100, contentType: "application/json", wantErr: errGeminiRequestTooLarge},
		{name: "wrong content type", contentLength: 10, limit: 100, contentType: "text/plain", wantErr: errGeminiRequestMalformed},
		{name: "invalid bound", contentLength: 10, limit: 0, contentType: "application/json", wantErr: errGeminiRequestMalformed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &trackingReadCloser{reader: strings.NewReader("not inspected")}
			request := geminiRequest(t, source, test.contentLength)
			request.Header.Set("Content-Type", test.contentType)
			nextCalls := 0
			transport := geminiDefaultSamplingTransport{
				maxRequestBytes: test.limit,
				next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					nextCalls++
					return nil, errors.New("unexpected network call")
				}),
			}
			_, err := transport.RoundTrip(request)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RoundTrip() error = %v, want %v", err, test.wantErr)
			}
			if nextCalls != 0 || source.bytesRead != 0 || !source.closed {
				t.Fatalf("next/read/closed = %d/%d/%v", nextCalls, source.bytesRead, source.closed)
			}
		})
	}
}

func TestGeminiDefaultSamplingTransportFailsClosedOnUntrustedTail(t *testing.T) {
	const secret = "workspace-secret-must-not-leak"
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "malformed JSON suffix", body: `{"contents":["` + secret + `"],"generationConfig":{"temperature":oops}}`, wantErr: errGeminiRequestMalformed},
		{name: "missing generation config", body: `{"contents":["` + secret + `"]}`, wantErr: errGeminiRequestMalformed},
		{name: "unknown generation field", body: `{"contents":["` + secret + `"],"generationConfig":{"temperature":0,"candidateCount":2}}`, wantErr: errGeminiRequestMalformed},
		{name: "unknown media resolution", body: `{"contents":["` + secret + `"],"generationConfig":{"temperature":0,"mediaResolution":"UNREGISTERED"}}`, wantErr: errGeminiRequestMalformed},
		{name: "generation config is not trailing", body: `{"contents":["` + secret + `"],"generationConfig":{"temperature":0},"extra":true}`, wantErr: errGeminiRequestMalformed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := geminiRequest(t, strings.NewReader(test.body), int64(len(test.body)))
			nextCalls := 0
			transport := geminiDefaultSamplingTransport{
				maxRequestBytes: int64(len(test.body)),
				next: roundTripperFunc(func(adapted *http.Request) (*http.Response, error) {
					nextCalls++
					_, readErr := io.Copy(io.Discard, adapted.Body)
					_ = adapted.Body.Close()
					return nil, readErr
				}),
			}
			_, err := transport.RoundTrip(request)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RoundTrip() error = %v, want %v", err, test.wantErr)
			}
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("RoundTrip() leaked request data: %v", err)
			}
			if nextCalls != 1 {
				t.Fatalf("next transport calls = %d, want streaming call", nextCalls)
			}
		})
	}
}

func TestGeminiDefaultSamplingBodyRedactsReadLengthCloseAndReplayFailures(t *testing.T) {
	const secret = "provider-body-secret"
	validBody := `{"contents":[],"generationConfig":{"temperature":0}}`
	t.Run("read", func(t *testing.T) {
		source := &failingReadCloser{err: errors.New(secret)}
		body := newGeminiSamplingBody(source, int64(len(validBody)), int64(len(validBody)))
		_, err := io.Copy(io.Discard, body)
		if !errors.Is(err, errGeminiRequestRead) || strings.Contains(err.Error(), secret) {
			t.Fatalf("read error = %v", err)
		}
	})
	t.Run("short length", func(t *testing.T) {
		body := newGeminiSamplingBody(io.NopCloser(strings.NewReader(validBody)), int64(len(validBody)+1), int64(len(validBody)+1))
		_, err := io.Copy(io.Discard, body)
		if !errors.Is(err, errGeminiRequestLength) {
			t.Fatalf("short error = %v", err)
		}
	})
	t.Run("long length", func(t *testing.T) {
		body := newGeminiSamplingBody(io.NopCloser(strings.NewReader(validBody+"x")), int64(len(validBody)), int64(len(validBody)+1))
		_, err := io.Copy(io.Discard, body)
		if !errors.Is(err, errGeminiRequestLength) {
			t.Fatalf("long error = %v", err)
		}
	})
	t.Run("close", func(t *testing.T) {
		body := newGeminiSamplingBody(&failingReadCloser{reader: strings.NewReader(validBody), closeErr: errors.New(secret)}, int64(len(validBody)), int64(len(validBody)))
		if err := body.Close(); !errors.Is(err, errGeminiRequestRead) || strings.Contains(err.Error(), secret) {
			t.Fatalf("close error = %v", err)
		}
	})
	t.Run("replay", func(t *testing.T) {
		request := geminiRequest(t, strings.NewReader(validBody), int64(len(validBody)))
		request.GetBody = func() (io.ReadCloser, error) { return nil, errors.New(secret) }
		transport := geminiDefaultSamplingTransport{
			maxRequestBytes: int64(len(validBody)),
			next: roundTripperFunc(func(adapted *http.Request) (*http.Response, error) {
				_, err := adapted.GetBody()
				_ = adapted.Body.Close()
				return nil, err
			}),
		}
		_, err := transport.RoundTrip(request)
		if !errors.Is(err, errGeminiRequestReplay) || strings.Contains(err.Error(), secret) {
			t.Fatalf("replay error = %v", err)
		}
	})
}

func testGeminiDescriptor(model, endpoint string) Provider {
	descriptor := newProvider(
		"gemini", "Gemini", []string{model}, model, ExecutionAdapter,
		Capabilities{SystemPrompt: true, Temperature: true}, apiKeySchema(),
		EndpointPolicy{Mode: EndpointVendor, ServerOwned: true, URL: endpoint},
		newGeminiClient, nil, nil,
	)
	descriptor.Limits.Timeout = 10 * time.Second
	descriptor.Limits.MaxResponseBytes = 8 << 10
	return descriptor
}

func geminiRequest(t *testing.T, body io.Reader, contentLength int64) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash:generateContent", body)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = contentLength
	request.Header.Set("Content-Type", "application/json")
	return request
}

func successfulGeminiHTTPResponse(request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
		Request:    request,
	}
}

func geminiResponse(finishReason, text string) map[string]any {
	return map[string]any{
		"modelVersion": "gemini-resolved",
		"candidates": []any{map[string]any{
			"finishReason": finishReason,
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "private reasoning", "thought": true},
				map[string]any{"text": text},
			}},
		}},
	}
}

func assertGeminiImageRequest(t *testing.T, payload map[string]any) {
	t.Helper()
	contents, ok := payload["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %#v", payload["contents"])
	}
	content, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatalf("content = %#v", contents[0])
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("parts = %#v", content["parts"])
	}
	prompt, ok := parts[0].(map[string]any)
	if !ok || prompt["text"] != "transcribe" {
		t.Fatalf("prompt part = %#v", parts[0])
	}
	imagePart, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("image part = %#v", parts[1])
	}
	inlineData, ok := imagePart["inline_data"].(map[string]any)
	if !ok || inlineData["mime_type"] != "image/png" || inlineData["data"] != base64.StdEncoding.EncodeToString([]byte("image")) {
		t.Fatalf("inline image = %#v", imagePart["inline_data"])
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type trackingReadCloser struct {
	reader    io.Reader
	bytesRead int64
	closed    bool
}

func (r *trackingReadCloser) Read(destination []byte) (int, error) {
	n, err := r.reader.Read(destination)
	r.bytesRead += int64(n)
	return n, err
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

type failingReadCloser struct {
	reader   io.Reader
	err      error
	closeErr error
}

func (r *failingReadCloser) Read(destination []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	return r.reader.Read(destination)
}

func (r *failingReadCloser) Close() error { return r.closeErr }

type repeatedByteReader struct {
	remaining int64
	value     byte
}

func (r *repeatedByteReader) Read(destination []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(destination)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for index := 0; index < n; index++ {
		destination[index] = r.value
	}
	r.remaining -= int64(n)
	return n, nil
}
