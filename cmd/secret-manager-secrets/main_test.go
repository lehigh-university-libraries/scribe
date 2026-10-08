package main

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
)

type memorySecrets struct {
	values map[string]map[string]string
	writes int
	fail   bool
}

func (s *memorySecrets) Read(_ context.Context, path string) (map[string]string, error) {
	if s.fail {
		return nil, errors.New("secret-value-must-not-escape")
	}
	return maps.Clone(s.values[path]), nil
}
func (s *memorySecrets) Write(_ context.Context, path string, values map[string]string) error {
	s.writes++
	s.values[path] = maps.Clone(values)
	return nil
}

func TestCopyIsVerifiedIdempotentAndDoesNotPrintValues(t *testing.T) {
	source := &memorySecrets{values: map[string]map[string]string{
		"scribe/dev/google_oauth": {"client_id": "private-id", "client_secret": "private-oauth"},
		"scribe/dev/openai":       {"api_key": "private-openai"},
		"scribe/dev/gemini":       {"api_key": "private-gemini"},
		"scribe/dev/database/app": {"password": "private-password"},
	}}
	destination := &memorySecrets{values: map[string]map[string]string{}}
	var output bytes.Buffer
	for range 2 {
		if err := copySecrets(context.Background(), source, destination, "scribe/dev", &output); err != nil {
			t.Fatal(err)
		}
	}
	if destination.writes != 4 {
		t.Fatalf("writes = %d, expected only one per credential", destination.writes)
	}
	if strings.Contains(output.String(), "private-") {
		t.Fatal("credential leaked to output")
	}
	source.fail = true
	if err := copySecrets(context.Background(), source, destination, "scribe/dev", &output); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("source error was not redacted")
	}
}
