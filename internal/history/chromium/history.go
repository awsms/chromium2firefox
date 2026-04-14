package chromium

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type URL struct {
	ID            int64
	URL           string
	Title         string
	VisitCount    int
	LastVisitTime time.Time
	Hidden        bool
	TypedCount    int
}

type Visit struct {
	ID                  int64
	URLID               int64
	VisitTime           time.Time
	FromVisitID         int64
	ExternalReferrerURL string
	Transition          int
	VisitDuration       time.Duration
}

type Dataset struct {
	URLs   []URL
	Visits []Visit
}

func ReadHistory(ctx context.Context, historyPath string) (Dataset, error) {
	db, err := sql.Open("sqlite", historyPath)
	if err != nil {
		return Dataset{}, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	urls, err := readURLs(ctx, db)
	if err != nil {
		return Dataset{}, err
	}

	visits, err := readVisits(ctx, db)
	if err != nil {
		return Dataset{}, err
	}

	sort.Slice(visits, func(i, j int) bool {
		if visits[i].VisitTime.Equal(visits[j].VisitTime) {
			return visits[i].ID < visits[j].ID
		}
		return visits[i].VisitTime.Before(visits[j].VisitTime)
	})

	return Dataset{
		URLs:   urls,
		Visits: visits,
	}, nil
}

func readURLs(ctx context.Context, db *sql.DB) ([]URL, error) {
	const query = `
SELECT
	u.id,
	u.url,
	u.title,
	u.visit_count,
	u.last_visit_time,
	u.hidden,
	u.typed_count
FROM urls u
ORDER BY u.last_visit_time ASC
`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query urls: %w", err)
	}
	defer rows.Close()

	var urls []URL
	for rows.Next() {
		var (
			entry            URL
			lastVisitTimeRaw int64
			hidden           int
		)

		if err := rows.Scan(
			&entry.ID,
			&entry.URL,
			&entry.Title,
			&entry.VisitCount,
			&lastVisitTimeRaw,
			&hidden,
			&entry.TypedCount,
		); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}

		entry.LastVisitTime = chromiumMicrosToTime(lastVisitTimeRaw)
		entry.Hidden = hidden != 0
		urls = append(urls, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	return urls, nil
}

func readVisits(ctx context.Context, db *sql.DB) ([]Visit, error) {
	const query = `
SELECT
	v.id,
	v.url,
	v.visit_time,
	COALESCE(v.from_visit, 0),
	COALESCE(v.external_referrer_url, ''),
	v.transition,
	v.visit_duration
FROM visits v
ORDER BY v.visit_time ASC, v.id ASC
`

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query visits: %w", err)
	}
	defer rows.Close()

	var visits []Visit
	for rows.Next() {
		var (
			visit           Visit
			visitTimeRaw    int64
			visitDurationUS int64
		)

		if err := rows.Scan(
			&visit.ID,
			&visit.URLID,
			&visitTimeRaw,
			&visit.FromVisitID,
			&visit.ExternalReferrerURL,
			&visit.Transition,
			&visitDurationUS,
		); err != nil {
			return nil, fmt.Errorf("scan visit row: %w", err)
		}

		visit.VisitTime = chromiumMicrosToTime(visitTimeRaw)
		visit.VisitDuration = time.Duration(visitDurationUS) * time.Microsecond
		visits = append(visits, visit)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visits: %w", err)
	}

	return visits, nil
}

func chromiumMicrosToTime(value int64) time.Time {
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	unixMicros := value - chromiumEpochOffsetMicros
	return time.UnixMicro(unixMicros).UTC()
}
