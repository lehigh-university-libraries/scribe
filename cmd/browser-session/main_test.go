package main

import "testing"

func TestParseArgumentsExposesOnlyOutputPath(t *testing.T) {
	output, err := parseArguments([]string{"--output", "/tmp/scribe-browser-session-run-42.json"})
	if err != nil || output != "/tmp/scribe-browser-session-run-42.json" {
		t.Fatalf("parseArguments valid output = %q, %v", output, err)
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
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(args); err == nil {
				t.Fatal("parseArguments accepted an unsupported authority override")
			}
		})
	}
}
