package chromium

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/awsms/chromium2firefox/internal/progress"

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

func ImportHistory(ctx context.Context, historyPath string, dataset Dataset, sourceSize int64, reporter progress.Sink) error {
	if len(dataset.URLs) == 0 && len(dataset.Visits) == 0 {
		return nil
	}
	if err := ensureRegularFile(historyPath); err != nil {
		return err
	}
	if err := backupFile(historyPath, reporter); err != nil {
		return fmt.Errorf("backup chromium history database: %w", err)
	}
	importSize, finalizeSize := progress.SplitStageSize(sourceSize, 92)
	if reporter != nil {
		reporter.StartStage("importing", historyPath, importSize)
	}

	db, err := sql.Open("sqlite", historyPath)
	if err != nil {
		return fmt.Errorf("open chromium history database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	visitColumns, err := getTableColumns(ctx, db, "visits")
	if err != nil {
		return fmt.Errorf("get visits table columns: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chromium history transaction: %w", err)
	}
	defer tx.Rollback()

	// Clear existing or just upsert? Usually importers clear or merge.
	// Chromium History is quite complex with many tables.
	// For now, let's just upsert URLs and Visits.

	insertStmt, err := tx.PrepareContext(ctx, `
INSERT INTO urls (url, title, visit_count, typed_count, last_visit_time, hidden)
VALUES (?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare url insert: %w", err)
	}
	defer insertStmt.Close()

	updateStmt, err := tx.PrepareContext(ctx, `
UPDATE urls SET
	title = ?,
	visit_count = MAX(visit_count, ?),
	typed_count = MAX(typed_count, ?),
	last_visit_time = MAX(last_visit_time, ?),
	hidden = MIN(hidden, ?)
WHERE url = ?
`)
	if err != nil {
		return fmt.Errorf("prepare url update: %w", err)
	}
	defer updateStmt.Close()

	visitInsertColumns := []string{
		"url",
		"visit_time",
		"from_visit",
		"transition",
		"segment_id",
		"visit_duration",
		"external_referrer_url",
	}
	visitInsertValues := []string{"?", "?", "?", "?", "?", "?", "?"}
	if visitColumns["consider_for_ntp_most_visited"] {
		visitInsertColumns = append(visitInsertColumns, "consider_for_ntp_most_visited")
		visitInsertValues = append(visitInsertValues, "1")
	}
	visitStmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
INSERT INTO visits (%s)
VALUES (%s)
`, strings.Join(visitInsertColumns, ", "), strings.Join(visitInsertValues, ", ")))
	if err != nil {
		return fmt.Errorf("prepare visit insert: %w", err)
	}
	defer visitStmt.Close()

	segmentInsertStmt, err := tx.PrepareContext(ctx, `
INSERT INTO segments (name, url_id)
VALUES (?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare segment insert: %w", err)
	}
	defer segmentInsertStmt.Close()

	segmentUpdateStmt, err := tx.PrepareContext(ctx, `
UPDATE segments SET url_id = ? WHERE name = ?
`)
	if err != nil {
		return fmt.Errorf("prepare segment update: %w", err)
	}
	defer segmentUpdateStmt.Close()

	segmentUsageInsertStmt, err := tx.PrepareContext(ctx, `
INSERT INTO segment_usage (segment_id, time_slot, visit_count)
VALUES (?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare segment_usage insert: %w", err)
	}
	defer segmentUsageInsertStmt.Close()

	segmentUsageUpdateStmt, err := tx.PrepareContext(ctx, `
UPDATE segment_usage SET visit_count = visit_count + ? WHERE segment_id = ? AND time_slot = ?
`)
	if err != nil {
		return fmt.Errorf("prepare segment_usage update: %w", err)
	}
	defer segmentUsageUpdateStmt.Close()

	visitSourceStmt, err := tx.PrepareContext(ctx, `
INSERT INTO visit_source (id, source) VALUES (?, 0)
ON CONFLICT(id) DO NOTHING
`)
	if err != nil {
		// Table might be missing in very old versions, but we'll try
	} else {
		defer visitSourceStmt.Close()
	}

	// Try to prepare keyword_search_terms, but don't fail if table missing
	keywordSearchStmt, _ := tx.PrepareContext(ctx, `
INSERT INTO keyword_search_terms (keyword_id, url_id, term, normalized_term)
VALUES (?, ?, ?, ?)
	`)

	urlIDByOriginalID := make(map[int64]int64)
	segmentsByUrlID := make(map[int64]int64)
	progressor := progress.NewStageProgress(reporter, importSize, int64(len(dataset.URLs)+len(dataset.Visits)))
	for _, url := range dataset.URLs {
		var existingID int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM urls WHERE url = ?", url.URL).Scan(&existingID)
		if err == sql.ErrNoRows {
			res, err := insertStmt.ExecContext(ctx,
				url.URL,
				url.Title,
				url.VisitCount,
				url.TypedCount,
				timeToChromiumMicros(url.LastVisitTime),
				boolToInt(url.Hidden),
			)
			if err != nil {
				return fmt.Errorf("insert url %s: %w", url.URL, err)
			}
			existingID, _ = res.LastInsertId()
		} else if err != nil {
			return fmt.Errorf("check url existence %s: %w", url.URL, err)
		} else {
			_, err = updateStmt.ExecContext(ctx,
				url.Title,
				url.VisitCount,
				url.TypedCount,
				timeToChromiumMicros(url.LastVisitTime),
				boolToInt(url.Hidden),
				url.URL,
			)
			if err != nil {
				return fmt.Errorf("update url %s: %w", url.URL, err)
			}
		}
		urlIDByOriginalID[url.ID] = existingID

		// Ensure segment exists for this URL
		var segmentID int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM segments WHERE name = ?", url.URL).Scan(&segmentID)
		if err == sql.ErrNoRows {
			res, err := segmentInsertStmt.ExecContext(ctx, url.URL, existingID)
			if err != nil {
				return fmt.Errorf("insert segment for %s: %w", url.URL, err)
			}
			segmentID, _ = res.LastInsertId()
		} else if err != nil {
			return fmt.Errorf("check segment existence %s: %w", url.URL, err)
		} else {
			_, err = segmentUpdateStmt.ExecContext(ctx, existingID, url.URL)
			if err != nil {
				return fmt.Errorf("update segment for %s: %w", url.URL, err)
			}
		}
		segmentsByUrlID[existingID] = segmentID

		if keywordSearchStmt != nil {
			if term := extractSearchTerm(url.URL); term != "" {
				_, _ = keywordSearchStmt.ExecContext(ctx, 0, existingID, term, strings.ToLower(term))
			}
		}
		progressor.Step(1)
	}

	for _, visit := range dataset.Visits {
		newURLID, ok := urlIDByOriginalID[visit.URLID]
		if !ok {
			continue
		}

		segmentID := segmentsByUrlID[newURLID]
		visitTime := timeToChromiumMicros(visit.VisitTime)

		// Use 0x30000000 (CHAIN_START | CHAIN_END) + core transition
		transition := 0x30000000 | (visit.Transition & 0xFF)

		res, err := visitStmt.ExecContext(ctx,
			newURLID,
			visitTime,
			0,
			transition,
			segmentID,
			int64(visit.VisitDuration.Microseconds()),
			visit.ExternalReferrerURL,
		)
		if err != nil {
			return fmt.Errorf("insert visit for url %d: %w", newURLID, err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("get inserted visit id for url %d: %w", newURLID, err)
		}

		if visitSourceStmt != nil {
			_, _ = visitSourceStmt.ExecContext(ctx, id)
		}

		// Correctly bucket to start of day in CHROMIUM time

		startOfDay := time.Unix((visit.VisitTime.Unix()/86400)*86400, 0).UTC()
		timeSlot := timeToChromiumMicros(startOfDay)

		var usageExists bool
		_ = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM segment_usage WHERE segment_id = ? AND time_slot = ?)", segmentID, timeSlot).Scan(&usageExists)
		if usageExists {
			_, _ = segmentUsageUpdateStmt.ExecContext(ctx, 1, segmentID, timeSlot)
		} else {
			_, _ = segmentUsageInsertStmt.ExecContext(ctx, segmentID, timeSlot, 1)
		}
		progressor.Step(1)
	}
	if keywordSearchStmt != nil {
		keywordSearchStmt.Close()
	}

	if reporter != nil {
		reporter.FinishStage("importing", historyPath, importSize)
		reporter.StartStage("committing", historyPath, finalizeSize)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chromium history transaction: %w", err)
	}
	if reporter != nil {
		reporter.FinishStage("committing", historyPath, finalizeSize)
	}
	return nil
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

func extractSearchTerm(rawURL string) string {
	if !strings.Contains(rawURL, "q=") && !strings.Contains(rawURL, "query=") {
		return ""
	}
	// Very basic extraction for common search patterns
	if strings.Contains(rawURL, "google.com/search") || strings.Contains(rawURL, "duckduckgo.com") || strings.Contains(rawURL, "bing.com/search") {
		// Just a heuristic, in a real tool we'd use proper URL parsing
		parts := strings.Split(rawURL, "q=")
		if len(parts) < 2 {
			parts = strings.Split(rawURL, "query=")
		}
		if len(parts) >= 2 {
			term := strings.Split(parts[1], "&")[0]
			if t, err := url.PathUnescape(term); err == nil {
				return t
			}
			return term
		}
	}
	return ""
}
