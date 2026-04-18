package chromium

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func getTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			dtype     string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &dtype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, nil
}

func timeToChromiumMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	return t.UTC().UnixMicro() + chromiumEpochOffsetMicros
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
