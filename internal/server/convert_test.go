package server

import "testing"

func TestUint64FromNonNegativeInt64(t *testing.T) {
	tests := []struct {
		name    string
		value   int64
		want    uint64
		wantErr bool
	}{
		{name: "negative", value: -1, wantErr: true},
		{name: "zero", value: 0},
		{name: "positive", value: 42, want: 42},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := uint64FromNonNegativeInt64(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("uint64FromNonNegativeInt64(%d) error = %v; wantErr %v", test.value, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("uint64FromNonNegativeInt64(%d) = %d; want %d", test.value, got, test.want)
			}
		})
	}
}
