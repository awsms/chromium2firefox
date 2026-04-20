package chromium

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/awsms/chromium2firefox/internal/progress"

	_ "modernc.org/sqlite"
)

type Favicon struct {
	PageURL     string
	IconURL     string
	Width       int
	Height      int
	LastUpdated int64
	ImageData   []byte
}

func ReadFavicons(ctx context.Context, faviconsPath string) ([]Favicon, error) {
	db, err := sql.Open("sqlite", faviconsPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
SELECT
	m.page_url,
	f.url,
	COALESCE(b.width, 0),
	COALESCE(b.height, 0),
	COALESCE(b.last_updated, 0),
	b.image_data
FROM icon_mapping m
JOIN favicons f ON f.id = m.icon_id
JOIN favicon_bitmaps b ON b.icon_id = f.id
WHERE b.image_data IS NOT NULL AND length(b.image_data) > 0
ORDER BY m.page_url ASC, f.url ASC, b.width DESC, b.last_updated DESC, b.id DESC
`)
	if err != nil {
		return nil, fmt.Errorf("query favicons: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]struct{})
	var out []Favicon
	for rows.Next() {
		var item Favicon
		if err := rows.Scan(
			&item.PageURL,
			&item.IconURL,
			&item.Width,
			&item.Height,
			&item.LastUpdated,
			&item.ImageData,
		); err != nil {
			return nil, fmt.Errorf("scan favicon row: %w", err)
		}

		key := fmt.Sprintf("%s\x00%s\x00%d", item.PageURL, item.IconURL, item.Width)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate favicon rows: %w", err)
	}

	return out, nil
}

func ImportFavicons(ctx context.Context, faviconsPath string, favicons []Favicon, sourceSize int64, reporter progress.Sink) error {
	if len(favicons) == 0 {
		return nil
	}
	if err := ensureRegularFile(faviconsPath); err != nil {
		return err
	}
	if err := backupFile(faviconsPath, reporter); err != nil {
		return fmt.Errorf("backup chromium favicons database: %w", err)
	}
	importSize, finalizeSize := progress.SplitStageSize(sourceSize, 95)
	if reporter != nil {
		reporter.StartStage("importing", faviconsPath, importSize)
	}

	db, err := sql.Open("sqlite", faviconsPath)
	if err != nil {
		return fmt.Errorf("open chromium favicons database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous = NORMAL"); err != nil {
		return fmt.Errorf("set synchronous normal: %w", err)
	}

	mappingColumns, err := getTableColumns(ctx, db, "icon_mapping")
	if err != nil {
		return err
	}
	bitmapColumns, err := getTableColumns(ctx, db, "favicon_bitmaps")
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chromium favicons transaction: %w", err)
	}
	defer tx.Rollback()

	// Cache for icon URLs to IDs
	iconCache := make(map[string]int64)
	rows, err := tx.QueryContext(ctx, "SELECT url, id FROM favicons")
	if err == nil {
		for rows.Next() {
			var url string
			var id int64
			if err := rows.Scan(&url, &id); err == nil {
				iconCache[url] = id
			}
		}
		rows.Close()
	}

	progressor := progress.NewStageProgress(reporter, importSize, int64(len(favicons)))
	for _, fav := range favicons {
		// 1. Ensure icon in favicons table
		iconID, ok := iconCache[fav.IconURL]
		if !ok {
			_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO favicons (url, icon_type) VALUES (?, 1)", fav.IconURL)
			if err != nil {
				return fmt.Errorf("insert favicon %s: %w", fav.IconURL, err)
			}
			err = tx.QueryRowContext(ctx, "SELECT id FROM favicons WHERE url = ?", fav.IconURL).Scan(&iconID)
			if err != nil {
				return fmt.Errorf("query favicon %s after insert: %w", fav.IconURL, err)
			}
			iconCache[fav.IconURL] = iconID
		}

		// 2. Ensure bitmap in favicon_bitmaps table
		cols := []string{"icon_id", "last_updated", "image_data", "width", "height"}
		args := []any{iconID, fav.LastUpdated, fav.ImageData, fav.Width, fav.Height}
		if bitmapColumns["last_requested"] {
			cols = append(cols, "last_requested")
			args = append(args, 0)
		}
		placeholders := make([]string, len(cols))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		// Using REPLACE (or INSERT OR REPLACE) ensures we update existing bitmaps
		query := fmt.Sprintf("INSERT OR REPLACE INTO favicon_bitmaps (%s) VALUES (%s)", strings.Join(cols, ", "), strings.Join(placeholders, ", "))
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("insert/replace bitmap for %s: %w", fav.IconURL, err)
		}

		// 3. Ensure mapping in icon_mapping table
		cols = []string{"page_url", "icon_id"}
		args = []any{fav.PageURL, iconID}
		if mappingColumns["page_url_type"] {
			cols = append(cols, "page_url_type")
			args = append(args, 0)
		}
		placeholders = make([]string, len(cols))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		query = fmt.Sprintf("INSERT OR IGNORE INTO icon_mapping (%s) VALUES (%s)", strings.Join(cols, ", "), strings.Join(placeholders, ", "))
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("insert mapping %s -> %s: %w", fav.PageURL, fav.IconURL, err)
		}
		progressor.Step(1)
	}

	if reporter != nil {
		reporter.FinishStage("importing", faviconsPath, importSize)
		reporter.StartStage("committing", faviconsPath, finalizeSize)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chromium favicons transaction: %w", err)
	}
	if reporter != nil {
		reporter.FinishStage("committing", faviconsPath, finalizeSize)
	}
	return nil
}
