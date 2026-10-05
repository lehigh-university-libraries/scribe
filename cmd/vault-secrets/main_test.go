package main

import (
	"context"
	"strings"
	"testing"
)

type memoryStore map[string]map[string]string

func (m memoryStore) Read(_ context.Context, path string) (map[string]string, error) {
	data := map[string]string{}
	for key, value := range m[path] {
		data[key] = value
	}
	return data, nil
}

func (m memoryStore) Write(_ context.Context, path string, data map[string]string) error {
	m[path] = data
	return nil
}

func answers(values map[string]string) prompter {
	return func(label string, _ bool) (string, error) {
		for prefix, value := range values {
			if strings.HasPrefix(label, prefix) {
				return value, nil
			}
		}
		return "", nil
	}
}

func TestUpdateWritesOnlyChangedSecretsAndKeepsOtherFields(t *testing.T) {
	store := memoryStore{
		"scribe/prod/google_oauth": {"client_id": "old-id", "client_secret": "old-secret"},
		"scribe/prod/openai":       {"api_key": "old-openai"},
	}
	var out strings.Builder
	err := update(context.Background(), store, "scribe/prod", answers(map[string]string{
		"Google OAuth client secret": "  new-secret\n",
		"Gemini API key":             "gemini-key",
	}), &out)
	if err != nil {
		t.Fatal(err)
	}

	if got := store["scribe/prod/google_oauth"]; got["client_id"] != "old-id" || got["client_secret"] != "new-secret" {
		t.Errorf("google_oauth = %v", got)
	}
	if got := store["scribe/prod/gemini"]["api_key"]; got != "gemini-key" {
		t.Errorf("gemini api_key = %q", got)
	}
	if _, written := store["scribe/prod/database/app"]; written {
		t.Error("database secret was written without a new value")
	}
	for _, line := range []string{"Updated scribe/prod/google_oauth", "Kept scribe/prod/openai", "Updated scribe/prod/gemini", "Kept scribe/prod/database/app"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q:\n%s", line, out.String())
		}
	}
}

func TestShowPrintsEverySecretPath(t *testing.T) {
	store := memoryStore{"scribe/dev/openai": {"api_key": "k"}}
	var out strings.Builder
	if err := show(context.Background(), store, "scribe/dev", &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"== scribe/dev/google_oauth ==", "== scribe/dev/openai ==\napi_key=k", "== scribe/dev/database/app =="} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q:\n%s", line, out.String())
		}
	}
}

func TestRunRejectsUnknownWorkspace(t *testing.T) {
	if err := run(context.Background(), []string{"-workspace", "pr-1"}, &strings.Builder{}); err == nil {
		t.Fatal("pr workspace was accepted")
	}
}
