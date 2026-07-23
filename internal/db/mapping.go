package db

// This file is the shared value-mapping boundary between Scribe's domain-
// oriented query adapters and sqlc's driver-shaped generated parameters.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value.UTC(), Valid: true}
}

func nullUint64(value *uint64) (sql.NullInt64, error) {
	if value == nil {
		return sql.NullInt64{}, nil
	}
	converted, err := uint64ToInt64(*value)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: converted, Valid: true}, nil
}

func rawJSON(value string) json.RawMessage {
	if strings.TrimSpace(value) == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(value)
}

func rawJSONToNullString(value json.RawMessage) sql.NullString {
	if len(value) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(value), Valid: true}
}

func scanInt64(value any) (int64, error) {
	switch v := value.(type) {
	case nil:
		return 0, nil
	case int64:
		return v, nil
	case int32:
		return int64(v), nil
	case int:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("uint64 value %d exceeds int64 range", v)
		}
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported int64 value type %T", value)
	}
}

func uint64ToInt64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("uint64 value %d exceeds int64 range", value)
	}
	return int64(value), nil
}

func uint64FromInt64(value int64) (uint64, error) {
	if value < 0 {
		return 0, fmt.Errorf("negative int64 value %d cannot convert to uint64", value)
	}
	return uint64(value), nil
}

func lastInsertID(res sql.Result) (uint64, error) {
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64FromInt64(id)
}

func intToInt32(value int) (int32, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, fmt.Errorf("int value %d exceeds int32 range", value)
	}
	return int32(value), nil
}

func scanFloat64(value any) (float64, error) {
	switch v := value.(type) {
	case nil:
		return 0, nil
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case []byte:
		return strconv.ParseFloat(string(v), 64)
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("unsupported float64 value type %T", value)
	}
}
