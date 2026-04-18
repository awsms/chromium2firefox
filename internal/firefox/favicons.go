package firefox

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"chromium2firefox/internal/chromium"
	"chromium2firefox/internal/progress"

	_ "modernc.org/sqlite"
)

func ImportFavicons(ctx context.Context, profileDir string, favicons []chromium.Favicon, sourceSize int64, reporter progress.Sink) error {
	if len(favicons) == 0 {
		return nil
	}

	faviconsPath := filepath.Join(profileDir, "favicons.sqlite")
	if err := ensureRegularFile(faviconsPath); err != nil {
		return err
	}

	if err := backupFile(faviconsPath, reporter); err != nil {
		return fmt.Errorf("backup favicons.sqlite: %w", err)
	}
	importSize, finalizeSize := progress.SplitStageSize(sourceSize, 95)
	if reporter != nil {
		reporter.StartStage("importing", faviconsPath, importSize)
	}
	progressor := progress.NewStageProgress(reporter, importSize, int64(len(favicons)))

	db, err := sql.Open("sqlite", faviconsPath)
	if err != nil {
		return fmt.Errorf("open firefox favicons database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin favicon transaction: %w", err)
	}
	defer tx.Rollback()

	pagesByURL, err := loadPagesWithIcons(ctx, tx)
	if err != nil {
		return err
	}
	iconsByKey, err := loadIcons(ctx, tx)
	if err != nil {
		return err
	}
	mappings, err := loadIconMappings(ctx, tx)
	if err != nil {
		return err
	}

	for _, item := range favicons {
		pageID, err := ensurePageWithIcon(ctx, tx, pagesByURL, item.PageURL)
		if err != nil {
			return err
		}
		iconID, err := ensureIcon(ctx, tx, iconsByKey, item)
		if err != nil {
			return err
		}
		if err := ensureIconMapping(ctx, tx, mappings, pageID, iconID); err != nil {
			return err
		}
		progressor.Step(1)
	}
	if reporter != nil {
		reporter.FinishStage("importing", faviconsPath, importSize)
		reporter.StartStage("finalizing", faviconsPath, finalizeSize)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit favicon transaction: %w", err)
	}
	if reporter != nil {
		reporter.FinishStage("finalizing", faviconsPath, finalizeSize)
	}

	return nil
}

func ReadFavicons(ctx context.Context, faviconsPath string) ([]chromium.Favicon, error) {
	db, err := sql.Open("sqlite", faviconsPath)
	if err != nil {
		return nil, fmt.Errorf("open firefox favicons database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
SELECT
	p.page_url,
	i.icon_url,
	i.width,
	i.expire_ms,
	i.data
FROM moz_pages_w_icons p
JOIN moz_icons_to_pages m ON m.page_id = p.id
JOIN moz_icons i ON i.id = m.icon_id
WHERE i.data IS NOT NULL
`)
	if err != nil {
		return nil, fmt.Errorf("query firefox favicons: %w", err)
	}
	defer rows.Close()

	var out []chromium.Favicon
	for rows.Next() {
		var item chromium.Favicon
		if err := rows.Scan(
			&item.PageURL,
			&item.IconURL,
			&item.Width,
			&item.LastUpdated,
			&item.ImageData,
		); err != nil {
			return nil, fmt.Errorf("scan firefox favicon: %w", err)
		}
		item.Height = item.Width
		out = append(out, item)
	}

	return out, rows.Err()
}

func loadPagesWithIcons(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, page_url
FROM moz_pages_w_icons
`)
	if err != nil {
		return nil, fmt.Errorf("load pages with icons: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var id int64
		var pageURL string
		if err := rows.Scan(&id, &pageURL); err != nil {
			return nil, fmt.Errorf("scan page with icon: %w", err)
		}
		out[pageURL] = id
	}
	return out, rows.Err()
}

func loadIcons(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, icon_url, width
FROM moz_icons
`)
	if err != nil {
		return nil, fmt.Errorf("load icons: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var (
			id      int64
			iconURL string
			width   int
		)
		if err := rows.Scan(&id, &iconURL, &width); err != nil {
			return nil, fmt.Errorf("scan icon: %w", err)
		}
		out[iconKey(iconURL, width)] = id
	}
	return out, rows.Err()
}

func loadIconMappings(ctx context.Context, tx *sql.Tx) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT page_id, icon_id
FROM moz_icons_to_pages
`)
	if err != nil {
		return nil, fmt.Errorf("load icon mappings: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var pageID, iconID int64
		if err := rows.Scan(&pageID, &iconID); err != nil {
			return nil, fmt.Errorf("scan icon mapping: %w", err)
		}
		out[mappingKey(pageID, iconID)] = struct{}{}
	}
	return out, rows.Err()
}

func ensurePageWithIcon(ctx context.Context, tx *sql.Tx, pagesByURL map[string]int64, pageURL string) (int64, error) {
	if id, ok := pagesByURL[pageURL]; ok {
		return id, nil
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_pages_w_icons (page_url, page_url_hash)
VALUES (?, ?)
`, pageURL, int64(hashURL(pageURL)))
	if err != nil {
		return 0, fmt.Errorf("insert page_with_icon %s: %w", pageURL, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get page_with_icon id for %s: %w", pageURL, err)
	}
	pagesByURL[pageURL] = id
	return id, nil
}

func ensureIcon(ctx context.Context, tx *sql.Tx, iconsByKey map[string]int64, item chromium.Favicon) (int64, error) {
	key := iconKey(item.IconURL, item.Width)
	if id, ok := iconsByKey[key]; ok {
		if _, err := tx.ExecContext(ctx, `
UPDATE moz_icons
SET data = ?, expire_ms = ?, root = ?, flags = 0
WHERE id = ?
`, item.ImageData, chromiumMillisToUnixMillis(item.LastUpdated), boolToInt(isRootIcon(item.IconURL)), id); err != nil {
			return 0, fmt.Errorf("update icon %s: %w", item.IconURL, err)
		}
		return id, nil
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_icons (
	icon_url, fixed_icon_url_hash, width, root, color, expire_ms, flags, data
)
VALUES (?, ?, ?, ?, NULL, ?, 0, ?)
`,
		item.IconURL,
		int64(hashURL(item.IconURL)),
		item.Width,
		boolToInt(isRootIcon(item.IconURL)),
		chromiumMillisToUnixMillis(item.LastUpdated),
		item.ImageData,
	)
	if err != nil {
		return 0, fmt.Errorf("insert icon %s: %w", item.IconURL, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get icon id for %s: %w", item.IconURL, err)
	}
	iconsByKey[key] = id
	return id, nil
}

func ensureIconMapping(ctx context.Context, tx *sql.Tx, mappings map[string]struct{}, pageID, iconID int64) error {
	key := mappingKey(pageID, iconID)
	if _, ok := mappings[key]; ok {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO moz_icons_to_pages (page_id, icon_id, expire_ms)
VALUES (?, ?, 0)
`, pageID, iconID); err != nil {
		return fmt.Errorf("insert icon mapping %d->%d: %w", pageID, iconID, err)
	}
	mappings[key] = struct{}{}
	return nil
}

func iconKey(iconURL string, width int) string {
	return fmt.Sprintf("%s\x00%d", iconURL, width)
}

func mappingKey(pageID, iconID int64) string {
	return fmt.Sprintf("%d:%d", pageID, iconID)
}

func isRootIcon(iconURL string) bool {
	return strings.HasSuffix(strings.ToLower(iconURL), "/favicon.ico")
}
