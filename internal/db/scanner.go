package db

import (
	"context"
	"database/sql"
	"fmt"
)

// GetLastBlock returns the last processed block number from the database.
func GetLastBlock(ctx context.Context, db *sql.DB) (int64, error) {
	var block int64
	err := db.QueryRowContext(ctx,
		`SELECT last_block FROM scanner_state WHERE id = 1`,
	).Scan(&block)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get last block: %w", err)
	}
	return block, nil
}

// SetLastBlock updates the last processed block number in the database.
func SetLastBlock(ctx context.Context, db *sql.DB, block int64) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO scanner_state (id, last_block, updated_at)
		 VALUES (1, ?, NOW())
		 ON DUPLICATE KEY UPDATE
		   last_block = VALUES(last_block),
		   updated_at = NOW()`,
		block,
	)
	if err != nil {
		return fmt.Errorf("set last block: %w", err)
	}
	return nil
}
