package p_rows

import (
	"context"
	"database/sql"
)

func iterateRows(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT 1")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return err
		}
	}

	return rows.Err()
}

func iterateRow(ctx context.Context, db *sql.DB) error {
	row := db.QueryRowContext(ctx, "SELECT 1")
	var v int
	return row.Scan(&v)
}
