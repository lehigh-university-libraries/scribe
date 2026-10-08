package iiif_test

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTripletSQLReplicas exercises the actual pinned server against MySQL,
// including cross-replica CAS and persistence after both processes restart.
func TestTripletSQLReplicas(t *testing.T) {
	first, second := os.Getenv("TRIPLET_SQL_FIRST"), os.Getenv("TRIPLET_SQL_SECOND")
	if first == "" || second == "" {
		t.Skip("Triplet SQL replicas are not enabled")
	}
	const path = "/presentation/v3/items/sql-contract/manifest"
	const original = `{"@context":"http://iiif.io/api/presentation/3/context.json","id":"https://iiif.example.org/presentation/v3/items/sql-contract/manifest","type":"Manifest","label":{"en":["Original"]},"items":[]}`
	updated := strings.Replace(original, "Original", "Updated", 1)
	type result struct {
		status     int
		etag, body string
		err        error
	}
	request := func(origin, method, body, match, none string) result {
		req, err := http.NewRequest(method, origin+path, strings.NewReader(body))
		if err != nil {
			return result{err: err}
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv("TRIPLET_PRESENTATION_WRITE_TOKEN"))
		req.Header.Set("Content-Type", "application/ld+json")
		if match != "" {
			req.Header.Set("If-Match", match)
		}
		if none != "" {
			req.Header.Set("If-None-Match", none)
		}
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return result{err: err}
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		return result{response.StatusCode, response.Header.Get("ETag"), string(data), err}
	}
	require := func(r result, status int) {
		t.Helper()
		if r.err != nil || r.status != status {
			t.Fatalf("response = %d %q %v; want %d", r.status, r.body, r.err, status)
		}
	}
	if os.Getenv("TRIPLET_SQL_PHASE") == "restart" {
		for _, origin := range []string{first, second} {
			r := request(origin, http.MethodGet, "", "", "")
			require(r, http.StatusOK)
			if !strings.Contains(r.body, "Updated") || r.etag == "" {
				t.Fatalf("resource did not survive restart: %#v", r)
			}
		}
		return
	}
	require(request(first, http.MethodPut, original, "", "*"), http.StatusCreated)
	r := request(second, http.MethodGet, "", "", "")
	require(r, http.StatusOK)
	if r.etag == "" {
		t.Fatal("missing shared ETag")
	}
	results := make(chan result, 2)
	for _, origin := range []string{first, second} {
		go func(origin string) { results <- request(origin, http.MethodPut, updated, r.etag, "") }(origin)
	}
	statuses := map[int]int{}
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		statuses[r.status]++
	}
	if statuses[http.StatusNoContent] != 1 || statuses[http.StatusPreconditionFailed] != 1 {
		t.Fatalf("concurrent CAS responses: %v", statuses)
	}
	require(request(first, http.MethodPut, original, r.etag, ""), http.StatusPreconditionFailed)
}
