package p_explain

import "database/sql"

func direct(db *sql.DB, xs []int) {
	for range xs {
		_, _ = db.Query("SELECT 1") // want `query-in-loop \[definite\].*Explain: reason=direct static call to matched query function`
	}
}
