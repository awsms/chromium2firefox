package chromium

import (
	"context"
	"database/sql"
	"fmt"

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
