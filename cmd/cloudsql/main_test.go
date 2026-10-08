package main

import (
	"context"
	"testing"
)

func TestMigrationCommandsAndProxyFailure(t *testing.T) {
	for _, mode := range []string{"scribe", "triplet"} {
		if _, err := commandFor(mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"", "/bin/sh", "scribe;echo unsafe"} {
		if _, err := commandFor(mode); err == nil {
			t.Fatalf("accepted arbitrary command %q", mode)
		}
	}
	exited := make(chan struct{})
	close(exited)
	if err := waitForProxy(context.Background(), exited); err == nil {
		t.Fatal("dead proxy reported ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForProxy(ctx, make(chan struct{})); err != context.Canceled {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestProxyConnectionNameRejectsFlagsAndShellSyntax(t *testing.T) {
	if !validInstance("test-project:us-east5:scribe-mysql") {
		t.Fatal("valid connection name rejected")
	}
	for _, name := range []string{"", "--help", "test-project:us-east5:scribe;echo", "test-project:us-east5:scribe\n--debug", "test-project:us-east5:scribe --debug"} {
		if validInstance(name) {
			t.Fatalf("accepted unsafe connection name %q", name)
		}
	}
}
