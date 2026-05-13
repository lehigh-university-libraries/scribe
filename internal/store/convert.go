package store

import (
	"database/sql"
	"fmt"
	"math"
)

func int32FromInt(value int) (int32, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, fmt.Errorf("int value %d exceeds int32 range", value)
	}
	return int32(value), nil
}

func uint64FromNonNegativeInt64(value int64) (uint64, bool) {
	if value < 0 {
		return 0, false
	}
	return uint64(value), true
}

func uint64PtrFromNullInt64(value sql.NullInt64) (*uint64, bool) {
	if !value.Valid {
		return nil, false
	}
	converted, ok := uint64FromNonNegativeInt64(value.Int64)
	if !ok {
		return nil, false
	}
	return &converted, true
}

func uint64ValueToNullInt64(value uint64) (sql.NullInt64, error) {
	if value > math.MaxInt64 {
		return sql.NullInt64{}, fmt.Errorf("uint64 value %d exceeds int64 range", value)
	}
	return sql.NullInt64{Int64: int64(value), Valid: true}, nil
}
