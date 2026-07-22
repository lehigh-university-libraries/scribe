package store

import (
	"database/sql"
	"math"
	"testing"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

func TestAnnotationPageUserIDConversionRoundTrip(t *testing.T) {
	userID := uint64(math.MaxInt64)

	row, err := annotationPageToRow(AnnotationPage{UpdatedByUserID: &userID})
	if err != nil {
		t.Fatalf("convert page to row: %v", err)
	}
	if !row.UpdatedByUserID.Valid || row.UpdatedByUserID.Int64 != math.MaxInt64 {
		t.Fatalf("unexpected database user ID: %#v", row.UpdatedByUserID)
	}

	page, err := annotationPageFromRow(row, nil)
	if err != nil {
		t.Fatalf("convert row to page: %v", err)
	}
	if page.UpdatedByUserID == nil || *page.UpdatedByUserID != userID {
		t.Fatalf("unexpected application user ID: %#v", page.UpdatedByUserID)
	}
}

func TestAnnotationPageToRowRejectsUserIDOutsideSignedRange(t *testing.T) {
	userID := uint64(math.MaxInt64) + 1

	if _, err := annotationPageToRow(AnnotationPage{UpdatedByUserID: &userID}); err == nil {
		t.Fatal("expected an out-of-range user ID to fail")
	}
}

func TestAnnotationPageFromRowRejectsNegativeUserID(t *testing.T) {
	row := db.AnnotationPage{
		UpdatedByUserID: sql.NullInt64{Int64: -1, Valid: true},
	}

	if _, err := annotationPageFromRow(row, nil); err == nil {
		t.Fatal("expected a negative database user ID to fail")
	}
}
