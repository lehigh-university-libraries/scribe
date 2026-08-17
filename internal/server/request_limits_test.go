package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestRequestBodyLimit(t *testing.T) {
	if maxConnectReadBytes != maxImageRequestBytes {
		t.Fatalf("Connect read limit = %d, want largest request class %d", maxConnectReadBytes, maxImageRequestBytes)
	}
	if got := requestBodyLimit("/scribe.v1.ItemService/UploadItemImage"); got != maxImageRequestBytes {
		t.Fatalf("upload limit = %d, want %d", got, maxImageRequestBytes)
	}
	if got := requestBodyLimit("/scribe.v1.ImageProcessingService/ProcessHOCR"); got != maxImageRequestBytes {
		t.Fatalf("hOCR image limit = %d, want %d", got, maxImageRequestBytes)
	}
	annotationPaths := []string{
		"/scribe.v1.AnnotationService/SaveAnnotationPage",
		"/scribe.v1.AnnotationService/EnrichAnnotation",
		"/scribe.v1.AnnotationService/SplitLineIntoWords",
		"/scribe.v1.AnnotationService/SplitPageIntoWords",
		"/scribe.v1.AnnotationService/SplitLineIntoTwoLines",
		"/scribe.v1.AnnotationService/JoinLines",
		"/scribe.v1.AnnotationService/JoinWordsIntoLine",
	}
	for _, path := range annotationPaths {
		if got := requestBodyLimit(path); got != maxAnnotationBytes {
			t.Fatalf("annotation limit for %s = %d, want %d", path, got, maxAnnotationBytes)
		}
	}
	if got := requestBodyLimit("/scribe.v1.ItemService/ListItems"); got != maxDefaultBodyBytes {
		t.Fatalf("default limit = %d, want %d", got, maxDefaultBodyBytes)
	}
}

func TestCanonicalPageReadsShareOneWorkspaceAdmissionPool(t *testing.T) {
	limiter := newBodyConcurrencyLimiter(1, 1)
	handler := &Handler{canonicalReadLimiter: limiter}
	release, ok := limiter.TryAcquire("workspace:42")
	if !ok {
		t.Fatal("failed to reserve canonical read fixture")
	}
	defer release()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	for _, path := range []string{
		"/scribe.v1.AnnotationService/GetAnnotationPage",
		"/scribe.v1.AnnotationService/GetAnnotation",
		"/scribe.v1.AnnotationService/SearchAnnotations",
	} {
		req := httptest.NewRequest(http.MethodPost, "https://scribe.example"+path, nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
			Authenticated: true, UserID: 7, WorkspaceID: 42, WorkspaceRole: "read",
		}))
		response := httptest.NewRecorder()
		handler.requestLimitMiddleware(next).ServeHTTP(response, req)
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("%s status = %d, want %d", path, response.Code, http.StatusTooManyRequests)
		}
	}
	if called {
		t.Fatal("rejected canonical read reached its handler")
	}
}

func TestRetiredIIIFRoutesUseNoCanonicalReadOrPublicResourceClass(t *testing.T) {
	for _, path := range []string{
		"/iiif/3/image/info.json",
		"/presentation/v3/item-image-9/manifest",
		"/v1/items/item-1/manifest",
		"/v1/item-images/9/manifest",
		"/v1/item-images/9/annotations",
		"/v1/item-images/9/annotations/items/line-1",
	} {
		request := httptest.NewRequest(http.MethodGet, "https://scribe.example"+path, nil)
		if isHeavyCanonicalRead(request) {
			t.Errorf("retired route %s retained canonical-read admission", path)
		}
		if got := requestLimitClass(path); got != "web" {
			t.Errorf("request limit class for retired route %s = %q, want web", path, got)
		}
	}
}

func TestExportRequestLimitClassCoversEveryExportAdapter(t *testing.T) {
	for _, path := range []string{
		"/scribe.v1.AnnotationService/ExportAnnotationPage",
		"/scribe.v1.ItemService/PrepareItemExport",
		"/v1/item-exports/signed-token",
		"/v1/item-images/42/annotations/revisions/7/hocr",
	} {
		if got := requestLimitClass(path); got != "export" {
			t.Fatalf("request limit class for %s = %q, want export", path, got)
		}
	}
}

func TestAuthServiceRequestLimitClassHasDedicatedBucket(t *testing.T) {
	for _, path := range []string{
		"/auth/google",
		"/scribe.v1.AuthService/GetAuthMe",
		"/scribe.v1.AuthService/ListAPIKeys",
		"/scribe.v1.AuthService/CreateAPIKey",
		"/scribe.v1.AuthService/DeleteAPIKey",
	} {
		if got := requestLimitClass(path); got != "auth" {
			t.Fatalf("request limit class for %s = %q, want auth", path, got)
		}
	}
	if got := requestLimitClass("/scribe.v1.ItemService/ListItems"); got != "rpc" {
		t.Fatalf("item service request limit class = %q, want rpc", got)
	}
}

func TestAuthServiceDoesNotConsumeEditorRateBucket(t *testing.T) {
	limiter := newRequestLimiterWithPolicy(0.000001, 1)
	handler := &Handler{requestLimiter: limiter}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	limited := handler.requestLimitMiddleware(next)

	request := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, "https://scribe.example"+path, nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
			Authenticated: true, UserID: 7, WorkspaceID: 42, WorkspaceRole: "write",
		}))
		response := httptest.NewRecorder()
		limited.ServeHTTP(response, req)
		return response.Code
	}

	if status := request("/scribe.v1.ItemService/ListItems"); status != http.StatusNoContent {
		t.Fatalf("editor request status = %d, want %d", status, http.StatusNoContent)
	}
	if status := request("/scribe.v1.AuthService/GetAuthMe"); status != http.StatusNoContent {
		t.Fatalf("auth request status = %d, want %d", status, http.StatusNoContent)
	}
}

func TestRequestLimitMiddlewareRejectsCompressedRPCBodiesBeforeDispatch(t *testing.T) {
	for _, header := range []string{"Content-Encoding", "Connect-Content-Encoding", "Grpc-Encoding"} {
		t.Run(header, func(t *testing.T) {
			dispatched := false
			handler := &Handler{}
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched = true })
			req := httptest.NewRequest(http.MethodPost, "https://scribe.example/scribe.v1.AnnotationService/SaveAnnotationPage", bytes.NewReader([]byte("compressed")))
			req.Header.Set(header, "gzip")
			response := httptest.NewRecorder()

			handler.requestAdmissionMiddleware(next).ServeHTTP(response, req)

			if response.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
			}
			if dispatched {
				t.Fatal("compressed request reached the RPC handler")
			}
		})
	}
}

func TestRequestLimitMiddlewareAllowsIdentityAndNonRPCEncoding(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		header string
		value  string
	}{
		{name: "identity RPC", path: "/scribe.v1.ItemService/ListItems", header: "Content-Encoding", value: "identity"},
		{name: "response negotiation", path: "/scribe.v1.ItemService/ListItems", header: "Accept-Encoding", value: "gzip"},
		{name: "non-RPC", path: "/v1/example", header: "Content-Encoding", value: "gzip"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatched := false
			handler := &Handler{}
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dispatched = true
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, "https://scribe.example"+test.path, bytes.NewReader([]byte("body")))
			req.Header.Set(test.header, test.value)
			response := httptest.NewRecorder()

			handler.requestAdmissionMiddleware(next).ServeHTTP(response, req)

			if response.Code != http.StatusNoContent || !dispatched {
				t.Fatalf("request was not dispatched: status=%d dispatched=%t", response.Code, dispatched)
			}
		})
	}
}

func TestRequestAdmissionRateLimitsInvalidCredentialsBeforeAuthentication(t *testing.T) {
	handler := &Handler{edgeRequestLimiter: newRequestLimiterWithPolicy(0.000001, 2)}
	authenticationCalls := 0
	authentication := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		authenticationCalls++
		w.WriteHeader(http.StatusUnauthorized)
	})
	admission := handler.requestAdmissionMiddleware(authentication)
	for attempt := 1; attempt <= 3; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "https://scribe.example/scribe.v1.ItemService/ListItems", bytes.NewReader([]byte(`{}`)))
		req.RemoteAddr = "192.0.2.44:1234"
		req.Header.Set("Authorization", "Bearer definitely-invalid")
		response := httptest.NewRecorder()
		admission.ServeHTTP(response, req)
		want := http.StatusUnauthorized
		if attempt == 3 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
	if authenticationCalls != 2 {
		t.Fatalf("authentication calls = %d, want 2 before edge rejection", authenticationCalls)
	}
}

func TestRequestAdmissionFitsEditorGoldenPathBurstAndRetainsBound(t *testing.T) {
	limiter := newEdgeRequestLimiter()
	now := time.Date(2026, time.August, 7, 14, 23, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	handler := &Handler{edgeRequestLimiter: limiter}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	admission := handler.requestAdmissionMiddleware(next)
	paths := []string{
		"/scribe.v1.ImageProcessingService/GetOCRRun",
		"/scribe.v1.TranscriptionService/GetTranscriptionJob",
		"/scribe.v1.ItemService/GetEditorManifest",
		"/scribe.v1.AnnotationService/GetAnnotationPage",
		"/scribe.v1.ContextService/GetContext",
	}
	request := func(attempt int) int {
		req := httptest.NewRequest(
			http.MethodPost,
			"https://scribe.example"+paths[(attempt-1)%len(paths)],
			bytes.NewReader([]byte(`{}`)),
		)
		req.RemoteAddr = "192.0.2.44:1234"
		req.AddCookie(&http.Cookie{Name: "scribe_session", Value: "editor-golden-path"})
		response := httptest.NewRecorder()
		admission.ServeHTTP(response, req)
		return response.Code
	}

	const editorGoldenPathBurst = 40
	for attempt := 1; attempt <= editorGoldenPathBurst; attempt++ {
		if status := request(attempt); status != http.StatusNoContent {
			t.Fatalf("editor request %d status = %d, want %d", attempt, status, http.StatusNoContent)
		}
	}
	if status := request(editorGoldenPathBurst + 1); status != http.StatusTooManyRequests {
		t.Fatalf("request beyond editor burst status = %d, want %d", status, http.StatusTooManyRequests)
	}

	now = now.Add(500 * time.Millisecond)
	if status := request(editorGoldenPathBurst + 2); status != http.StatusNoContent {
		t.Fatalf("request after one-token refill status = %d, want %d", status, http.StatusNoContent)
	}
	if status := request(editorGoldenPathBurst + 3); status != http.StatusTooManyRequests {
		t.Fatalf("second request after one-token refill status = %d, want %d", status, http.StatusTooManyRequests)
	}
}

func TestRequestAdmissionSeparatesSessionsBehindSharedInstitutionalNAT(t *testing.T) {
	previousRuntime := config.Get()
	config.Init(config.Runtime{Config: config.Config{Auth: config.AuthConfig{CookieName: "scribe_session"}}})
	t.Cleanup(func() { config.Init(previousRuntime) })

	handler := &Handler{
		edgeRequestLimiter:   newRequestLimiterWithPolicy(0.000001, 2),
		edgeAggregateLimiter: newRequestLimiterWithPolicy(0.000001, 10),
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	admission := handler.requestAdmissionMiddleware(next)
	for user := 1; user <= 3; user++ {
		for requestNumber := 1; requestNumber <= 2; requestNumber++ {
			req := httptest.NewRequest(http.MethodPost, "https://scribe.example/scribe.v1.ItemService/ListItems", bytes.NewReader([]byte(`{}`)))
			req.RemoteAddr = "192.0.2.44:1234"
			req.AddCookie(&http.Cookie{Name: "scribe_session", Value: fmt.Sprintf("session-%d", user)})
			response := httptest.NewRecorder()
			admission.ServeHTTP(response, req)
			if response.Code != http.StatusNoContent {
				t.Fatalf("user %d request %d status = %d, want %d", user, requestNumber, response.Code, http.StatusNoContent)
			}
		}
	}
}

func TestRequestAdmissionKeepsAnonymousTrafficStrictBehindSharedIP(t *testing.T) {
	handler := &Handler{
		edgeRequestLimiter:   newRequestLimiterWithPolicy(0.000001, 2),
		edgeAggregateLimiter: newRequestLimiterWithPolicy(0.000001, 10),
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	admission := handler.requestAdmissionMiddleware(next)
	for attempt := 1; attempt <= 3; attempt++ {
		req := httptest.NewRequest(http.MethodGet, "https://scribe.example/v1/public", nil)
		req.RemoteAddr = "192.0.2.44:1234"
		response := httptest.NewRecorder()
		admission.ServeHTTP(response, req)
		want := http.StatusNoContent
		if attempt == 3 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestRequestAdmissionBoundsRotatingInvalidCredentialsByClientIP(t *testing.T) {
	handler := &Handler{
		edgeRequestLimiter:   newRequestLimiterWithPolicy(0.000001, 1),
		edgeAggregateLimiter: newRequestLimiterWithPolicy(0.000001, 3),
	}
	authenticationCalls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		authenticationCalls++
		w.WriteHeader(http.StatusUnauthorized)
	})
	admission := handler.requestAdmissionMiddleware(next)
	for attempt := 1; attempt <= 4; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "https://scribe.example/scribe.v1.ItemService/ListItems", bytes.NewReader([]byte(`{}`)))
		req.RemoteAddr = "192.0.2.44:1234"
		req.Header.Set("X-Scribe-API-Key", fmt.Sprintf("rotated-invalid-%d", attempt))
		response := httptest.NewRecorder()
		admission.ServeHTTP(response, req)
		want := http.StatusUnauthorized
		if attempt == 4 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
	if authenticationCalls != 3 {
		t.Fatalf("authentication calls = %d, want 3 before aggregate rejection", authenticationCalls)
	}
}

func TestEdgeCredentialFingerprintIsStableBoundedAndSecretFree(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://scribe.example/", nil)
	request.Header.Set("Authorization", "Bearer top-secret-value")
	first, ok := edgeCredentialFingerprint(request)
	if !ok {
		t.Fatal("expected bearer credential fingerprint")
	}
	second, _ := edgeCredentialFingerprint(request)
	if first != second || len(first) != 32 {
		t.Fatalf("fingerprint = %q / %q, want stable 128-bit hex", first, second)
	}
	if strings.Contains(first, "top-secret-value") {
		t.Fatal("fingerprint retained credential material")
	}
}

func TestRequestAdmissionRejectsOversizeInvalidCredentialBeforeAuthentication(t *testing.T) {
	authenticationCalls := 0
	handler := &Handler{}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { authenticationCalls++ })
	req := httptest.NewRequest(http.MethodPost, "https://scribe.example/scribe.v1.ItemService/ListItems", bytes.NewReader([]byte("body")))
	req.ContentLength = maxDefaultBodyBytes + 1
	req.Header.Set("X-Scribe-API-Key", "invalid")
	response := httptest.NewRecorder()

	handler.requestAdmissionMiddleware(next).ServeHTTP(response, req)

	if response.Code != http.StatusRequestEntityTooLarge || authenticationCalls != 0 {
		t.Fatalf("oversize invalid credential status/calls = %d/%d", response.Code, authenticationCalls)
	}
}

func TestLargeRequestConcurrencyIsRejectedBeforeBodyDispatch(t *testing.T) {
	for _, path := range []string{
		"/scribe.v1.ItemService/UploadItemImage",
		"/scribe.v1.AnnotationService/SaveAnnotationPage",
	} {
		t.Run(path, func(t *testing.T) {
			handler := &Handler{largeBodyLimiter: newBodyConcurrencyLimiter(1, 1)}
			started := make(chan struct{})
			release := make(chan struct{})
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
				w.WriteHeader(http.StatusNoContent)
			})
			admission := handler.requestAdmissionMiddleware(next)
			firstDone := make(chan int, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, "https://scribe.example"+path, bytes.NewReader([]byte("first")))
				req.RemoteAddr = "192.0.2.10:1234"
				response := httptest.NewRecorder()
				admission.ServeHTTP(response, req)
				firstDone <- response.Code
			}()
			<-started

			second := httptest.NewRequest(http.MethodPost, "https://scribe.example"+path, bytes.NewReader([]byte("second")))
			second.RemoteAddr = "192.0.2.11:1234"
			secondResponse := httptest.NewRecorder()
			admission.ServeHTTP(secondResponse, second)
			if secondResponse.Code != http.StatusTooManyRequests {
				t.Fatalf("concurrent large request status = %d, want %d", secondResponse.Code, http.StatusTooManyRequests)
			}
			close(release)
			if status := <-firstDone; status != http.StatusNoContent {
				t.Fatalf("first large request status = %d", status)
			}
		})
	}
}

func TestReadinessConcurrencyIsBoundedBeforeDatabaseCheck(t *testing.T) {
	handler := &Handler{readinessLimiter: newBodyConcurrencyLimiter(1, 1)}
	started := make(chan struct{})
	release := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	admission := handler.requestAdmissionMiddleware(next)
	firstDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "https://scribe.example/readyz", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		response := httptest.NewRecorder()
		admission.ServeHTTP(response, req)
		firstDone <- response.Code
	}()
	<-started

	second := httptest.NewRequest(http.MethodGet, "https://scribe.example/readyz", nil)
	second.RemoteAddr = "192.0.2.11:1234"
	secondResponse := httptest.NewRecorder()
	admission.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent readiness status = %d, want %d", secondResponse.Code, http.StatusTooManyRequests)
	}
	close(release)
	if status := <-firstDone; status != http.StatusNoContent {
		t.Fatalf("first readiness status = %d", status)
	}
}

func TestRequestLimitKeySeparatesClientsBehindTrustedProxy(t *testing.T) {
	previousRuntime := config.Get()
	config.Init(config.Runtime{Config: config.Config{Server: config.ServerConfig{
		TrustedProxyCIDRs: config.CIDRList{"172.18.0.5/32"},
	}}})
	t.Cleanup(func() { config.Init(previousRuntime) })

	requestFor := func(clientIP, path string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "https://scribe.example"+path, nil)
		req.RemoteAddr = "172.18.0.5:8080"
		// Traefik's reviewed frontend entry point preserves PPB/Node's one
		// canonical address and does not append the frontend VPC hop.
		req.Header.Set("X-Forwarded-For", clientIP)
		return req
	}

	first := requestLimitKey(requestFor("192.0.2.10", "/auth/google"))
	second := requestLimitKey(requestFor("192.0.2.11", "/auth/google"))
	if first == second {
		t.Fatalf("proxied clients share rate-limit key %q", first)
	}
	if first == requestLimitKey(requestFor("192.0.2.10", "/")) {
		t.Fatalf("auth and web requests share rate-limit key %q", first)
	}
}

func TestRequestLimitKeyIgnoresSpoofedForwardingFromUntrustedPeer(t *testing.T) {
	requestFor := func(spoofedIP string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "https://scribe.example/auth/google", nil)
		req.RemoteAddr = "203.0.113.8:8080"
		req.Header.Set("X-Forwarded-For", spoofedIP)
		return req
	}

	first := requestLimitKey(requestFor("192.0.2.10"))
	second := requestLimitKey(requestFor("192.0.2.11"))
	if first != second {
		t.Fatalf("spoofed forwarding changed key: %q != %q", first, second)
	}
}

func TestRequestLimiterBoundsUntrustedKeys(t *testing.T) {
	limiter := newRequestLimiter()
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for index := 0; index < maxRateLimitKeys; index++ {
		if !limiter.Allow(fmt.Sprintf("key-%d", index)) {
			t.Fatalf("key %d was unexpectedly rejected", index)
		}
	}
	if limiter.Allow("one-key-too-many") {
		t.Fatal("limiter accepted a new key beyond its memory bound")
	}
	if got := len(limiter.buckets); got != maxRateLimitKeys {
		t.Fatalf("bucket count = %d, want %d", got, maxRateLimitKeys)
	}
}
