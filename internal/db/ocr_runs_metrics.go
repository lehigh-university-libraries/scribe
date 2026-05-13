package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in ocr_run_metrics.sql.

import (
	"context"
	"database/sql"
)

type ContextOCRRunMetrics struct {
	TotalRuns              int64
	CorrectedRuns          int64
	AvgLevenshteinDistance float64
	AvgEditCount           float64
	AvgBoxChangeScore      float64
}

func (q *Queries) GetContextOCRRunMetrics(ctx context.Context, contextID uint64) (ContextOCRRunMetrics, error) {
	converted, err := compatUint64ToInt64(contextID)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	row, err := q.GetContextOCRRunMetricsManual(ctx, sql.NullInt64{Int64: converted, Valid: true})
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	correctedRuns, err := compatInt64(row.CorrectedRuns)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	avgLevenshteinDistance, err := compatFloat64(row.AvgLevenshteinDistance)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	avgEditCount, err := compatFloat64(row.AvgEditCount)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	avgBoxChangeScore, err := compatFloat64(row.AvgBoxChangeScore)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	return ContextOCRRunMetrics{
		TotalRuns:              row.TotalRuns,
		CorrectedRuns:          correctedRuns,
		AvgLevenshteinDistance: avgLevenshteinDistance,
		AvgEditCount:           avgEditCount,
		AvgBoxChangeScore:      avgBoxChangeScore,
	}, nil
}
