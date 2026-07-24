package segmentor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
)

func TestClientDelegatesSegmentAndTranscribeProtocolToHTR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if request.FormValue("model") != "registered-model" {
			t.Errorf("model = %q", request.FormValue("model"))
		}
		file, header, err := request.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 8 || header.Filename != "image.png" || header.Header.Get("Content-Type") != "image/png" {
			t.Errorf("HTR upload = filename %q, type %q, bytes %d", header.Filename, header.Header.Get("Content-Type"), len(data))
		}
		switch request.URL.Path {
		case "/v1/segment":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"provider": "kraken",
				"words":    []any{map[string]any{"X": 1, "Y": 2, "Width": 3, "Height": 4, "Text": "café 世界", "Confidence": 0.9}},
			})
		case "/v1/transcribe":
			_ = json.NewEncoder(w).Encode(map[string]any{"provider": "kraken", "model": "resolved-model", "text": "café 世界"})
		default:
			t.Errorf("path = %q", request.URL.Path)
		}
	}))
	defer server.Close()

	imagePath := filepath.Join(t.TempDir(), "private-document-name.png")
	if err := os.WriteFile(imagePath, []byte("\x89PNG\r\n\x1a\nencoded"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClientForEndpoint(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	words, provider, err := client.DetectWords(context.Background(), imagePath, "registered-model")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "kraken" || len(words) != 1 || words[0].Text != "café 世界" {
		t.Fatalf("segment result = %q, %#v", provider, words)
	}
	text, model, err := client.Transcribe(context.Background(), imagePath, "registered-model")
	if err != nil {
		t.Fatal(err)
	}
	if text != "café 世界" || model != "resolved-model" {
		t.Fatalf("transcription result = %q, %q", text, model)
	}
	result, err := client.Extract(context.Background(), providers.Request{
		Model: "registered-model",
		Image: providers.Image{Data: []byte("\x89PNG\r\n\x1a\nencoded"), MediaType: "image/png", Filename: "private-document-name.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "café 世界" || result.EffectiveModel != "resolved-model" {
		t.Fatalf("provider result = %#v", result)
	}
}

func TestProviderClientNormalizesUnsupportedSourceFormatsThroughTriplet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := request.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "normalized-jpeg" || header.Filename != "image.jpg" || header.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("normalized upload = filename %q, type %q, data %q", header.Filename, header.Header.Get("Content-Type"), data)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"provider": "kraken", "model": "registered-model", "text": "normalized"})
	}))
	defer server.Close()

	client, err := NewClientForEndpoint(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	client.images = fakeTripletImageClient{normalized: []byte("normalized-jpeg")}
	result, err := client.Extract(context.Background(), providers.Request{
		Model: "registered-model",
		Image: providers.Image{Data: []byte("source-tiff"), MediaType: "image/tiff", Filename: "document.tiff"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "normalized" {
		t.Fatalf("result = %#v", result)
	}
}

type fakeTripletImageClient struct {
	normalized []byte
}

func (f fakeTripletImageClient) Enabled() bool { return true }

func (f fakeTripletImageClient) FullJPEG(context.Context, string) ([]byte, error) {
	return append([]byte(nil), f.normalized...), nil
}

func (f fakeTripletImageClient) Normalize(context.Context, []byte, string) ([]byte, error) {
	return append([]byte(nil), f.normalized...), nil
}

func TestClientRejectsOffOriginOrPlaintextAudience(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		audience string
	}{
		{"https://service.example", "https://other.example"},
		{"http://service.example", "http://service.example"},
		{"https://service.example", "https://service.example/path"},
		{"https://service.example", "https://service.example/"},
	} {
		if _, err := NewClientForEndpoint(test.endpoint, test.audience); err == nil {
			t.Errorf("accepted endpoint %q audience %q", test.endpoint, test.audience)
		}
	}
}
