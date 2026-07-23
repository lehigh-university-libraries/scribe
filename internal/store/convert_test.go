package store

import (
	"math"
	"testing"
)

func TestAttemptNumberFromIntRejectsValuesOutsideDatabaseRange(t *testing.T) {
	tests := []struct {
		name    string
		value   int
		want    uint32
		wantErr bool
	}{
		{name: "negative", value: -1, wantErr: true},
		{name: "zero", value: 0, wantErr: true},
		{name: "one", value: 1, want: 1},
		{name: "maximum", value: math.MaxInt32, want: math.MaxInt32},
		{name: "overflow", value: math.MaxInt32 + 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := attemptNumberFromInt(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("attemptNumberFromInt(%d) error = %v; wantErr %v", test.value, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("attemptNumberFromInt(%d) = %d; want %d", test.value, got, test.want)
			}
		})
	}
}

func TestInt32FromUint32RejectsOverflow(t *testing.T) {
	tests := []struct {
		name    string
		value   uint32
		want    int32
		wantErr bool
	}{
		{name: "zero", value: 0},
		{name: "maximum", value: math.MaxInt32, want: math.MaxInt32},
		{name: "overflow", value: math.MaxInt32 + 1, wantErr: true},
		{name: "uint32 maximum", value: math.MaxUint32, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := int32FromUint32(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("int32FromUint32(%d) error = %v; wantErr %v", test.value, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("int32FromUint32(%d) = %d; want %d", test.value, got, test.want)
			}
		})
	}
}

func TestFenceRejectsAttemptCounterOverflow(t *testing.T) {
	job := TranscriptionJob{
		ID:            1,
		AttemptCount:  math.MaxInt32 + 1,
		InputRevision: 1,
		LeaseToken:    "lease-token",
	}
	if _, err := job.Fence(); err != ErrTranscriptionJobFence {
		t.Fatalf("Fence() error = %v; want %v", err, ErrTranscriptionJobFence)
	}
}
