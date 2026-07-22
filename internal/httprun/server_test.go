package httprun

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestServeStopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	server := &http.Server{
		Addr:              "127.0.0.1:0",
		Handler:           http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ReadHeaderTimeout: time.Second,
	}
	if err := Serve(ctx, server, time.Second); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServeValidatesLifecycleInputs(t *testing.T) {
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	var nilContext context.Context
	if err := Serve(nilContext, server, time.Second); err == nil {
		t.Fatal("Serve accepted a nil context")
	}
	if err := Serve(context.Background(), nil, time.Second); err == nil {
		t.Fatal("Serve accepted a nil server")
	}
	if err := Serve(context.Background(), server, 0); err == nil {
		t.Fatal("Serve accepted an unbounded shutdown timeout")
	}
}
