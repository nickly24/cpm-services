package exams

import (
	"context"
	"database/sql"
	"time"
)

type RetentionResult struct {
	ImportsDeleted  int64
	ReceiptsRetired int64
}

// CleanupRetention discards bounded expired preview payloads and old receipt
// responses. The compact receipt key/hash tombstone remains permanently, so an
// expired command cannot become a fresh write after cleanup or process restart.
func CleanupRetention(ctx context.Context, db *sql.DB, now time.Time, limit int) (RetentionResult, error) {
	result := RetentionResult{}
	if limit < 1 || limit > 1000 || now.IsZero() {
		return result, Bad("invalid_cleanup_limit")
	}
	deleted, err := db.ExecContext(ctx, "DELETE FROM exams_imports_v1 WHERE expires_at<=? ORDER BY expires_at,id LIMIT ?", now.UTC(), limit)
	if err != nil {
		return result, err
	}
	result.ImportsDeleted, err = deleted.RowsAffected()
	if err != nil {
		return result, err
	}
	retired, err := db.ExecContext(ctx, "UPDATE exams_receipts_v1 SET response=NULL,status_code=410 WHERE status_code IN(200,201) AND response IS NOT NULL AND created_at<=? ORDER BY created_at LIMIT ?", now.UTC().Add(-ReceiptRetention), limit)
	if err != nil {
		return result, err
	}
	result.ReceiptsRetired, err = retired.RowsAffected()
	return result, err
}
