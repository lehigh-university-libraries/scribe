package db

// Store query adapters in this file are the sole mapping boundary from
// domain-shaped metric values to sqlc-generated queries in ocr_run_metrics.sql.

import (
	"context"
	"database/sql"
)

type ContextOCRRunMetrics struct {
	TotalRuns              int64
	CorrectedRuns          int64
	AvgLevenshteinDistance float64
}

func (q *Queries) GetContextOCRRunMetrics(ctx context.Context, workspaceID, contextID uint64) (ContextOCRRunMetrics, error) {
	converted, err := uint64ToInt64(contextID)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	row, err := q.GetContextOCRRunMetricsManual(ctx, GetContextOCRRunMetricsManualParams{
		ContextID:   sql.NullInt64{Int64: converted, Valid: true},
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	correctedRuns, err := scanInt64(row.CorrectedRuns)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	avgLevenshteinDistance, err := scanFloat64(row.AvgLevenshteinDistance)
	if err != nil {
		return ContextOCRRunMetrics{}, err
	}
	return ContextOCRRunMetrics{
		TotalRuns:              row.TotalRuns,
		CorrectedRuns:          correctedRuns,
		AvgLevenshteinDistance: avgLevenshteinDistance,
	}, nil
}
