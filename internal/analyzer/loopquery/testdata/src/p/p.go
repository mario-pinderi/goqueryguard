package p

import (
	"context"
	"database/sql"
)

func direct(db *sql.DB, xs []int) {
	for range xs {
		_, _ = db.Query("SELECT 1") // want `query-in-loop \[definite\]`
	}
}

func helper(db *sql.DB) {
	_, _ = db.Query("SELECT 1")
}

func indirect(db *sql.DB, xs []int) {
	for range xs {
		helper(db) // want `query-in-loop \[definite\]`
	}
}

type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func possible(q Querier, xs []int) {
	for range xs {
		_, _ = q.Query("SELECT 1") // want `query-in-loop \[possible\]`
	}
}

func rowsIterationNotAQuery(db *sql.DB, xs []int) {
	rows, _ := db.Query("SELECT 1")
	defer rows.Close()
	for range xs {
		_ = rows.Next()
		var v int
		_ = rows.Scan(&v)
	}
}

func queryRowAndScan(db *sql.DB, xs []int) {
	for range xs {
		row := db.QueryRowContext(context.Background(), "SELECT 1") // want `query-in-loop \[definite\]`
		var v int
		_ = row.Scan(&v)
	}
}

type XOStyleDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func definiteXOStyle(ctx context.Context, db XOStyleDB, xs []int) {
	for range xs {
		_, _ = db.ExecContext(ctx, "UPDATE t SET v = 1") // want `query-in-loop \[definite\]`
	}
}

func suppressed(db *sql.DB, xs []int) {
	//goqueryguard:ignore query-in-loop -- legacy cleanup
	for range xs {
		_, _ = db.Query("SELECT 1")
	}
}

func missingReason(db *sql.DB, xs []int) {
	//goqueryguard:ignore query-in-loop // want `suppression for query-in-loop requires a reason`
	for range xs {
		_, _ = db.Query("SELECT 1") // want `query-in-loop \[definite\]`
	}
}

func unusedSuppression() {
	//goqueryguard:ignore query-in-loop -- no longer needed // want `unused suppression for query-in-loop`
	v := 1
	_ = v
}
