package main

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/app"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestParseArgumentsExposesOnlyOutputPath(t *testing.T) {
	request, err := parseArguments([]string{"--output", "/tmp/scribe-browser-session-run-42.json"})
	if err != nil || request.outputPath != "/tmp/scribe-browser-session-run-42.json" || request.cleanupPath != "" || request.cleanupAll {
		t.Fatalf("parseArguments valid output = %+v, %v", request, err)
	}
	request, err = parseArguments([]string{"--cleanup", "/tmp/scribe-browser-session-run-42.json"})
	if err != nil || request.cleanupPath == "" || request.outputPath != "" || request.cleanupAll {
		t.Fatalf("parseArguments cleanup = %+v, %v", request, err)
	}
	request, err = parseArguments([]string{"--cleanup-all"})
	if err != nil || !request.cleanupAll || request.outputPath != "" || request.cleanupPath != "" {
		t.Fatalf("parseArguments cleanup all = %+v, %v", request, err)
	}
	request, err = parseArguments([]string{"--reserve", "/tmp/scribe-browser-session-run-42.json"})
	if err != nil || request.reservePath == "" {
		t.Fatalf("parseArguments reserve = %+v, %v", request, err)
	}
	request, err = parseArguments([]string{"--export", "/tmp/scribe-browser-session-run-42.json"})
	if err != nil || request.exportPath == "" {
		t.Fatalf("parseArguments export = %+v, %v", request, err)
	}
	request, err = parseArguments([]string{"--reserved-output", "/tmp/scribe-browser-session-run-42.json"})
	if err != nil || request.reservedOutputPath == "" {
		t.Fatalf("parseArguments reserved output = %+v, %v", request, err)
	}

	for name, args := range map[string][]string{
		"missing output": nil,
		"positional identity": {
			"--output", "/tmp/scribe-browser-session-run-42.json", "1",
		},
		"user override": {
			"--output", "/tmp/scribe-browser-session-run-42.json", "--user-id", "2",
		},
		"TTL override": {
			"--output", "/tmp/scribe-browser-session-run-42.json", "--ttl", "1h",
		},
		"multiple operations": {
			"--output", "/tmp/scribe-browser-session-run-42.json", "--cleanup-all",
		},
		"multiple reserved operations": {
			"--reserve", "/tmp/scribe-browser-session-run-42.json", "--reserved-output", "/tmp/scribe-browser-session-run-42.json",
		},
		"export plus cleanup": {
			"--export", "/tmp/scribe-browser-session-run-42.json", "--cleanup", "/tmp/scribe-browser-session-run-42.json",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(args); err == nil {
				t.Fatal("parseArguments accepted an unsupported authority override")
			}
		})
	}
}

func TestRunMintsWithNarrowDependencies(t *testing.T) {
	const outputPath = "/tmp/scribe-browser-session-123-1.json"
	identities := store.NewIdentityStore(nil)
	for _, test := range []struct {
		name         string
		args         []string
		wantReserved bool
	}{
		{name: "new output", args: []string{"--output", outputPath}},
		{name: "reserved output", args: []string{"--reserved-output", outputPath}, wantReserved: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bootstrapCalls := 0
			mintCalls := 0
			mint := func(reserved bool) browserSessionMinter {
				return func(
					_ context.Context,
					gotIdentities *store.IdentityStore,
					publicBaseURL string,
					cookieName string,
					cookieDomain string,
					gotOutputPath string,
				) error {
					mintCalls++
					if reserved != test.wantReserved {
						t.Fatalf("reserved mint = %t, want %t", reserved, test.wantReserved)
					}
					if gotIdentities != identities ||
						publicBaseURL != "https://scribe.example" ||
						cookieName != "scribe_session" ||
						cookieDomain != ".scribe.example" ||
						gotOutputPath != outputPath {
						t.Fatalf("mint inputs = identities:%p origin:%q cookie:%q domain:%q output:%q",
							gotIdentities, publicBaseURL, cookieName, cookieDomain, gotOutputPath)
					}
					return nil
				}
			}
			runtime := commandRuntime{
				newDependencies: func(context.Context) (*app.BrowserSessionDependencies, error) {
					bootstrapCalls++
					return &app.BrowserSessionDependencies{
						PublicBaseURL: "https://scribe.example",
						CookieName:    "scribe_session",
						CookieDomain:  ".scribe.example",
						IdentityStore: identities,
					}, nil
				},
				mint:         mint(false),
				mintReserved: mint(true),
			}
			if err := runWithRuntime(context.Background(), test.args, runtime); err != nil {
				t.Fatalf("runWithRuntime returned error: %v", err)
			}
			if bootstrapCalls != 1 || mintCalls != 1 {
				t.Fatalf("calls = bootstrap:%d mint:%d, want 1/1", bootstrapCalls, mintCalls)
			}
		})
	}
}

func TestRunReserveExportAndCleanupDoNotBootstrapDependencies(t *testing.T) {
	const outputPath = "/tmp/scribe-browser-session-123-1.json"
	const exportedState = "private storage state"
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "reserve", args: []string{"--reserve", outputPath}, want: "reserve"},
		{name: "export", args: []string{"--export", outputPath}, want: "export"},
		{name: "cleanup", args: []string{"--cleanup", outputPath}, want: "cleanup"},
		{name: "cleanup all", args: []string{"--cleanup-all"}, want: "cleanup-all"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := ""
			var stdout bytes.Buffer
			runtime := commandRuntime{
				newDependencies: func(context.Context) (*app.BrowserSessionDependencies, error) {
					t.Fatal("non-mint operation bootstrapped application dependencies")
					return nil, nil
				},
				reserve: func(_ context.Context, path string) error {
					if path != outputPath {
						t.Fatalf("reserve path = %q", path)
					}
					called = "reserve"
					return nil
				},
				export: func(_ context.Context, path string, destination io.Writer) error {
					if path != outputPath {
						t.Fatalf("export path = %q", path)
					}
					called = "export"
					_, err := io.WriteString(destination, exportedState)
					return err
				},
				cleanup: func(_ context.Context, path string) error {
					if path != outputPath {
						t.Fatalf("cleanup path = %q", path)
					}
					called = "cleanup"
					return nil
				},
				cleanupAll: func(context.Context) error {
					called = "cleanup-all"
					return nil
				},
				stdout: &stdout,
			}
			if err := runWithRuntime(context.Background(), test.args, runtime); err != nil {
				t.Fatalf("runWithRuntime returned error: %v", err)
			}
			if called != test.want {
				t.Fatalf("operation = %q, want %q", called, test.want)
			}
			if test.want == "export" && stdout.String() != exportedState {
				t.Fatalf("export stdout = %q", stdout.String())
			}
		})
	}
}
