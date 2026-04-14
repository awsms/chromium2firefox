package chromium

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
