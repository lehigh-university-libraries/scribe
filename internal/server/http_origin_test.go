package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIUnknownRoutesFailClosed(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	for _, requestPath := range []string{"/", "/editor", "/assets/application.js", "/not-an-api-route"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://scribe.test"+requestPath, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d; want 404", requestPath, response.Code)
		}
	}
}

func TestAPIRetiredIIIFRoutesFailClosed(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	paths := []string{
		"/iiif/2/image/info.json",
		"/iiif/3/image/info.json",
		"/presentation/v3/item-image-1/manifest",
		"/v1/items/item-1/manifest",
		"/v1/item-images/1/manifest",
		"/v1/item-images/1/manifest/canvas/page-1",
		"/v1/item-images/1/manifest/painting",
		"/v1/item-images/1/manifest/painting/items/image",
		"/v1/item-images/1/annotations",
		"/v1/item-images/1/annotations/items/annotation-1",
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, requestPath := range paths {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "http://scribe.test"+requestPath, nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("%s %s status = %d; want 404", method, requestPath, response.Code)
			}
		}
	}
}

func TestCORSAllowsOnlyRawUploadPublicationReadsCrossOrigin(t *testing.T) {
	publicPaths := []string{
		"/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg",
	}
	for _, requestPath := range publicPaths {
		requestPath := requestPath
		t.Run(requestPath, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodOptions, "https://scribe.example"+requestPath, nil)
			request.Header.Set("Origin", "https://viewer.example")
			response := httptest.NewRecorder()

			if applyCORS(response, request) {
				t.Fatal("IIIF preflight continued to the application handler")
			}
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("allow origin = %q, want *", response.Header().Get("Access-Control-Allow-Origin"))
			}
			if response.Header().Get("Access-Control-Allow-Methods") != "GET,HEAD,OPTIONS" {
				t.Fatalf("allow methods = %q", response.Header().Get("Access-Control-Allow-Methods"))
			}
			if response.Header().Get("Access-Control-Allow-Headers") != "Accept,If-None-Match,If-Modified-Since,Range" {
				t.Fatalf("allow headers = %q", response.Header().Get("Access-Control-Allow-Headers"))
			}
			if response.Header().Get("Access-Control-Expose-Headers") != "ETag,Last-Modified,Content-Length,Content-Range,Accept-Ranges" {
				t.Fatalf("expose headers = %q", response.Header().Get("Access-Control-Expose-Headers"))
			}
			if response.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("public response allowed credentials: %q", response.Header().Get("Access-Control-Allow-Credentials"))
			}
		})
	}

	privateOrInvalid := []struct {
		method string
		path   string
	}{
		{method: http.MethodOptions, path: "/iiif/3/page.png/info.json"},
		{method: http.MethodOptions, path: "/presentation/v3/item-image-1/manifest"},
		{method: http.MethodOptions, path: "/v1/items/item-1/manifest"},
		{method: http.MethodOptions, path: "/v1/item-images/1/manifest"},
		{method: http.MethodOptions, path: "/v1/item-images/1/manifest/canvas/page-1"},
		{method: http.MethodOptions, path: "/v1/item-images/1/manifest/painting"},
		{method: http.MethodOptions, path: "/v1/item-images/1/manifest/painting/items/image"},
		{method: http.MethodOptions, path: "/v1/item-images/1/annotations"},
		{method: http.MethodOptions, path: "/v1/item-images/1/annotations/items/annotation-1"},
		{method: http.MethodOptions, path: "/v1/item-images/1/annotations/revisions/1/hocr"},
		{method: http.MethodOptions, path: "/v1/item-exports/token"},
		{method: http.MethodOptions, path: "/v1/item-images/1/manifest/extra"},
		{method: http.MethodPost, path: "/v1/item-images/1/annotations"},
	}
	for _, test := range privateOrInvalid {
		test := test
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://scribe.example"+test.path, nil)
			request.Header.Set("Origin", "https://viewer.example")
			response := httptest.NewRecorder()
			if applyCORS(response, request) {
				t.Fatal("untrusted origin continued to a private or invalid application route")
			}
			if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("response = %d, origin %q; want 403 without CORS grant", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}
