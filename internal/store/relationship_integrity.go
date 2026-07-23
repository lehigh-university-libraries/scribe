package store

import (
	"context"
	"fmt"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

// RelationshipIntegrityViolation reports an application-owned relationship
// whose members no longer share the same authoritative parent. Scribe keeps
// these invariants at repository transaction boundaries instead of relying on
// database foreign keys, so operators can run this audit after recovery and in
// release acceptance tests.
type RelationshipIntegrityViolation struct {
	Relationship string
	Count        uint64
}

// AuditRelationshipIntegrity returns only relationships with violations.
func AuditRelationshipIntegrity(ctx context.Context, database db.DBTX) ([]RelationshipIntegrityViolation, error) {
	rows, err := db.New(database).AuditRelationshipIntegrityManual(ctx)
	if err != nil {
		return nil, fmt.Errorf("audit relationship integrity: %w", err)
	}
	violations := make([]RelationshipIntegrityViolation, 0, len(rows))
	for _, row := range rows {
		if row.ViolationCount < 0 {
			return nil, fmt.Errorf("audit relationship integrity: %s returned a negative count", row.RelationshipName)
		}
		if row.ViolationCount == 0 {
			continue
		}
		violations = append(violations, RelationshipIntegrityViolation{
			Relationship: row.RelationshipName,
			Count:        uint64(row.ViolationCount),
		})
	}
	return violations, nil
}
