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
)

func TestClientDelegatesSegmentProtocolToHTR(t *testing.T) {
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

}

type fakeTripletImageClient struct {
	normalized []byte
}

func (f fakeTripletImageClient) Enabled() bool { return true }

func (f fakeTripletImageClient) FullJPEG(context.Context, string) ([]byte, error) {
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

func TestClientNormalizesSourceTIFFThroughTriplet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = request.MultipartForm.RemoveAll() }()
		file, header, err := request.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "normalized-jpeg" || header.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("upload = %q, %q", data, header.Header.Get("Content-Type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"provider": "kraken", "words": []any{map[string]any{"X": 1, "Y": 2, "Width": 3, "Height": 4, "Confidence": 0.9}}})
	}))
	defer server.Close()
	client, err := NewClientForEndpoint(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	client.images = fakeTripletImageClient{normalized: []byte("normalized-jpeg")}
	boxes, provider, err := client.DetectWords(context.Background(), "document.tiff", "kraken")
	if err != nil || provider != "kraken" || len(boxes) != 1 {
		t.Fatalf("segment normalized TIFF = %+v, %q, %v", boxes, provider, err)
	}
}
