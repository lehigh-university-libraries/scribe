package safehttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestValidateURLRejectsLoopback(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:8080/image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateURL(u); err == nil {
		t.Fatal("ValidateURL accepted loopback address")
	}
}

func TestValidatePublicURLRejectsNonPublicSpecialPurposeAddresses(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"http://0.0.0.1/image.jpg",
		"http://100.64.0.1/image.jpg",
		"http://169.254.169.254/latest/meta-data",
		"http://192.0.2.1/image.jpg",
		"http://198.18.0.1/image.jpg",
		"http://198.51.100.1/image.jpg",
		"http://203.0.113.1/image.jpg",
		"http://240.0.0.1/image.jpg",
		"http://[64:ff9b::a9fe:a9fe]/image.jpg",
		"http://[100::1]/image.jpg",
		"http://[2001:db8::1]/image.jpg",
		"http://[2002:a9fe:a9fe::1]/image.jpg",
		"http://[::ffff:169.254.169.254]/latest/meta-data",
	} {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(rawURL)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePublicURL(u); err == nil {
				t.Fatalf("ValidatePublicURL accepted non-public address %q", rawURL)
			}
		})
	}
}

func TestValidatePublicURLAllowsPublicLiteralAddresses(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://8.8.8.8/image.jpg",
		"https://[2606:4700:4700::1111]/image.jpg",
	} {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePublicURL(u); err != nil {
			t.Fatalf("ValidatePublicURL(%q) = %v", rawURL, err)
		}
	}
}

func TestValidatePublicURLRejectsLoopbackWhenTestFetchesAreEnabled(t *testing.T) {
	t.Setenv(AllowPrivateFetchesEnv, "true")
	u, err := url.Parse("http://127.0.0.1:8080/image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateURL(u); err != nil {
		t.Fatalf("ValidateURL rejected enabled test fixture: %v", err)
	}
	if err := ValidatePublicURL(u); err == nil {
		t.Fatal("ValidatePublicURL accepted loopback address")
	}
}

func TestGetAllowsPrivateFetchesWhenConfigured(t *testing.T) {
	t.Setenv(AllowPrivateFetchesEnv, "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestDoRedactsSignedURLFromTransportErrorsAndLogs(t *testing.T) {
	const secret = "sentinel-signed-query-secret"
	originalClient := client
	client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: context.DeadlineExceeded}
	})}
	t.Cleanup(func() { client = originalClient })

	resp, err := Get(context.Background(), "https://images.example.edu/full/full/0/default.jpg?token="+secret)
	if resp != nil {
		t.Fatalf("response = %+v, want nil", resp)
	}
	if err == nil {
		t.Fatal("Get returned no transport error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transport error does not preserve deadline classification: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("transport error = %q, want timeout category", err)
	}
	var leakedURL *url.Error
	if errors.As(err, &leakedURL) {
		t.Fatalf("transport error retained URL-bearing cause: %+v", leakedURL)
	}

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Error("remote image request", "error", err)
	logged := output.String()
	if strings.Contains(logged, secret) || strings.Contains(logged, "token=") {
		t.Fatalf("transport log exposed signed URL: %s", logged)
	}
}
