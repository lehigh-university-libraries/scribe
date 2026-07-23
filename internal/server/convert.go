package server

import (
	"fmt"
	"math"
)

func int32FromIntBounded(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

func uint64FromNonNegativeInt64(value int64) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("negative database count %d", value)
	}
	return uint64(value), nil
}
