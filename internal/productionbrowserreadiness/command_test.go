package productionbrowserreadiness

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCappedStreamWriterKeepsOnlyMaximumAndSentinel(t *testing.T) {
	for _, test := range []struct {
		name         string
		size         int
		wantWritten  int
		wantExceeded bool
	}{
		{name: "within maximum", size: maximumStateBytes, wantWritten: maximumStateBytes},
		{name: "sentinel", size: maximumStateBytes + 1, wantWritten: maximumStateBytes + 1, wantExceeded: true},
		{name: "bounded excess", size: maximumStateBytes + 100, wantWritten: maximumStateBytes + 1, wantExceeded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var destination bytes.Buffer
			writer := newCappedStreamWriter(&destination, maximumStateBytes)
			input := bytes.Repeat([]byte("x"), test.size)
			written, err := writer.Write(input)
			if err != nil || written != len(input) {
				t.Fatalf("Write() = %d, %v", written, err)
			}
			if destination.Len() != test.wantWritten || writer.exceeded != test.wantExceeded {
				t.Fatalf("stream = bytes:%d exceeded:%t, want bytes:%d exceeded:%t",
					destination.Len(), writer.exceeded, test.wantWritten, test.wantExceeded)
			}
		})
	}
}

func TestRunStreamWritesWithoutRetainingCredentialOutput(t *testing.T) {
	credential := strings.Repeat("private-state", 32)
	var destination bytes.Buffer
	result := (osCommandRunner{}).RunStream(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=^TestRunStreamHelper$"},
		map[string]string{"SCRIBE_TEST_STREAM_OUTPUT": credential},
		5*time.Second,
		time.Second,
		&destination,
		maximumStateBytes,
	)
	if result.err != nil || result.exitCode != 0 {
		t.Fatalf("RunStream() = %+v", result)
	}
	if destination.String() != credential {
		t.Fatalf("streamed bytes = %d, want %d", destination.Len(), len(credential))
	}
	if len(result.stdout) != 0 {
		t.Fatal("RunStream retained credential output")
	}
}

func TestRunStreamRejectsAndDoesNotRetainOversizedOutput(t *testing.T) {
	credential := strings.Repeat("x", maximumStateBytes+2)
	var destination bytes.Buffer
	result := (osCommandRunner{}).RunStream(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=^TestRunStreamHelper$"},
		map[string]string{"SCRIBE_TEST_STREAM_OUTPUT": credential},
		5*time.Second,
		time.Second,
		&destination,
		maximumStateBytes,
	)
	if result.err != errOutputLimit || result.exitCode != 125 {
		t.Fatalf("RunStream() = %+v", result)
	}
	if destination.Len() != maximumStateBytes+1 {
		t.Fatalf("bounded stream bytes = %d, want %d", destination.Len(), maximumStateBytes+1)
	}
	if len(result.stdout) != 0 {
		t.Fatal("oversized RunStream retained credential output")
	}
}

func TestRunStreamHelper(t *testing.T) {
	output := os.Getenv("SCRIBE_TEST_STREAM_OUTPUT")
	if output == "" {
		return
	}
	if _, err := io.WriteString(os.Stdout, output); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
