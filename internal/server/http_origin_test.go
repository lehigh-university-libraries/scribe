package server

import (
	"net/http/httptest"
	"testing"
)

func TestRequestOriginUsesForwardedHeadersFromTrustedProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "http://10.202.0.7:8080/v1/item-images/1/manifest", nil)
	r.RemoteAddr = "172.18.0.5:43124"
	r.Host = "10.202.0.7:8080"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "scribe-915966395449.us-east5.run.app:8080")

	got := requestOrigin(r)
	want := "https://scribe-915966395449.us-east5.run.app"
	if got != want {
		t.Fatalf("requestOrigin() = %q; want %q", got, want)
	}
}

func TestRequestOriginUsesStandardForwardedHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "http://api:8080/v1/item-images/1/manifest", nil)
	r.RemoteAddr = "10.0.1.2:9000"
	r.Host = "api:8080"
	r.Header.Set("Forwarded", `for=192.0.2.60;proto=https;host=example.org`)

	got := requestOrigin(r)
	want := "https://example.org"
	if got != want {
		t.Fatalf("requestOrigin() = %q; want %q", got, want)
	}
}

func TestRequestOriginIgnoresForwardedHeadersFromUntrustedProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost:8080/v1/item-images/1/manifest", nil)
	r.RemoteAddr = "8.8.8.8:443"
	r.Host = "localhost:8080"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "evil.example")

	got := requestOrigin(r)
	want := "http://localhost"
	if got != want {
		t.Fatalf("requestOrigin() = %q; want %q", got, want)
	}
}

func TestNormalizeOriginHostDropsPort(t *testing.T) {
	tests := map[string]string{
		"scribe-915966395449.us-east5.run.app:8080": "scribe-915966395449.us-east5.run.app",
		"localhost:8080":                             "localhost",
		"example.org":                                "example.org",
	}

	for input, want := range tests {
		if got := normalizeOriginHost(input); got != want {
			t.Fatalf("normalizeOriginHost(%q) = %q; want %q", input, got, want)
		}
	}
}
