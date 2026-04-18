package chromium

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"chromium2firefox/internal/progress"

	_ "modernc.org/sqlite"
)

type Engine struct {
	ID                   int64
	Name                 string
	Keyword              string
	SearchURL            string
	SuggestURL           string
	FaviconURL           string
	InputEncodings       []string
	SafeForAutoreplace   bool
	PrepopulateID        int
	IsActive             bool
	SearchURLPostParams  string
	SuggestURLPostParams string
}

func ReadWebData(ctx context.Context, webDataPath string) ([]Engine, error) {
	db, err := sql.Open("sqlite", webDataPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
SELECT
	id,
	short_name,
	keyword,
	url,
	COALESCE(suggest_url, ''),
	COALESCE(favicon_url, ''),
	COALESCE(input_encodings, ''),
	safe_for_autoreplace,
	prepopulate_id,
	is_active,
	COALESCE(search_url_post_params, ''),
	COALESCE(suggest_url_post_params, '')
FROM keywords
ORDER BY is_active DESC, usage_count DESC, id DESC
`)
	if err != nil {
		return nil, fmt.Errorf("query keywords: %w", err)
	}
	defer rows.Close()

	var out []Engine
	for rows.Next() {
		var (
			item               Engine
			safeForAutoreplace int
			isActive           int
			inputEncodings     string
		)
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.Keyword,
			&item.SearchURL,
			&item.SuggestURL,
			&item.FaviconURL,
			&inputEncodings,
			&safeForAutoreplace,
			&item.PrepopulateID,
			&isActive,
			&item.SearchURLPostParams,
			&item.SuggestURLPostParams,
		); err != nil {
			return nil, fmt.Errorf("scan keyword row: %w", err)
		}

		item.InputEncodings = splitCSV(inputEncodings)
		item.SafeForAutoreplace = safeForAutoreplace != 0
		item.IsActive = isActive != 0
		out = append(out, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate keyword rows: %w", err)
	}

	return out, nil
}

func ImportWebData(ctx context.Context, webDataPath string, engines []Engine, sourceSize int64, reporter progress.Sink) error {
	if len(engines) == 0 {
		return nil
	}
	if err := ensureRegularFile(webDataPath); err != nil {
		return err
	}
	if err := backupFile(webDataPath, reporter); err != nil {
		return fmt.Errorf("backup chromium web data database: %w", err)
	}
	importSize, finalizeSize := progress.SplitStageSize(sourceSize, 95)
	if reporter != nil {
		reporter.StartStage("importing", webDataPath, importSize)
	}

	db, err := sql.Open("sqlite", webDataPath)
	if err != nil {
		return fmt.Errorf("open chromium web data database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chromium web data transaction: %w", err)
	}
	defer tx.Rollback()

	insertStmt, err := tx.PrepareContext(ctx, `
INSERT INTO keywords (
	short_name, keyword, url, suggest_url, favicon_url, input_encodings,
	safe_for_autoreplace, prepopulate_id, is_active, date_created, last_modified
)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare keyword insert: %w", err)
	}
	defer insertStmt.Close()

	updateStmt, err := tx.PrepareContext(ctx, `
UPDATE keywords SET
	short_name = ?,
	url = ?,
	suggest_url = ?,
	favicon_url = ?,
	input_encodings = ?,
	last_modified = ?
WHERE keyword = ?
`)
	if err != nil {
		return fmt.Errorf("prepare keyword update: %w", err)
	}
	defer updateStmt.Close()

	now := timeToChromiumMicros(time.Now())
	progressor := progress.NewStageProgress(reporter, importSize, int64(len(engines)))
	for _, engine := range engines {
		if engine.Keyword == "" {
			progressor.Step(1)
			continue
		}

		var exists bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM keywords WHERE keyword = ?)", engine.Keyword).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check keyword existence: %w", err)
		}

		if exists {
			_, err = updateStmt.ExecContext(ctx,
				engine.Name,
				engine.SearchURL,
				engine.SuggestURL,
				engine.FaviconURL,
				strings.Join(engine.InputEncodings, ";"),
				now,
				engine.Keyword,
			)
			if err != nil {
				return fmt.Errorf("update keyword %s: %w", engine.Keyword, err)
			}
		} else {
			_, err = insertStmt.ExecContext(ctx,
				engine.Name,
				engine.Keyword,
				engine.SearchURL,
				engine.SuggestURL,
				engine.FaviconURL,
				strings.Join(engine.InputEncodings, ";"),
				boolToInt(engine.SafeForAutoreplace),
				boolToInt(engine.IsActive),
				now,
				now,
			)
			if err != nil {
				return fmt.Errorf("insert keyword %s: %w", engine.Keyword, err)
			}
		}
		progressor.Step(1)
	}

	if reporter != nil {
		reporter.FinishStage("importing", webDataPath, importSize)
		reporter.StartStage("committing", webDataPath, finalizeSize)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chromium web data transaction: %w", err)
	}
	if reporter != nil {
		reporter.FinishStage("committing", webDataPath, finalizeSize)
	}
	return nil
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
