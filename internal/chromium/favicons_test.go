package chromium

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportFavicons(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	faviconsPath := filepath.Join(chromiumDir, "Favicons")

	createChromiumFaviconsEmptyDB(t, faviconsPath)

	favicons := []Favicon{
		{
			PageURL:     "https://example.com/",
			IconURL:     "https://example.com/favicon.ico",
			Width:       16,
			Height:      16,
			LastUpdated: 1710000000,
			ImageData:   []byte("test-image"),
		},
	}

	if err := ImportFavicons(ctx, faviconsPath, favicons, 1024, nil); err != nil {
		t.Fatalf("ImportFavicons() error = %v", err)
	}

	// Test update logic
	favicons[0].ImageData = []byte("updated-image")
	if err := ImportFavicons(ctx, faviconsPath, favicons, 1024, nil); err != nil {
		t.Fatalf("ImportFavicons(Update) error = %v", err)
	}

	db, err := sql.Open("sqlite", faviconsPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var data []byte
	err = db.QueryRow("SELECT image_data FROM favicon_bitmaps WHERE width = 16").Scan(&data)
	if err != nil {
		t.Fatalf("query image_data error = %v", err)
	}
	if string(data) != "updated-image" {
		t.Errorf("expected updated image data, got %q", string(data))
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM favicon_bitmaps").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 bitmap record, got %d", count)
	}
}

func createChromiumFaviconsEmptyDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE favicons (
			id INTEGER PRIMARY KEY,
			url LONGVARCHAR NOT NULL,
			icon_type INTEGER DEFAULT 1 NOT NULL
		)`,
		`CREATE TABLE icon_mapping (
			id INTEGER PRIMARY KEY,
			page_url LONGVARCHAR NOT NULL,
			icon_id INTEGER NOT NULL
		)`,
		`CREATE TABLE favicon_bitmaps (
			id INTEGER PRIMARY KEY,
			icon_id INTEGER NOT NULL,
			last_updated INTEGER DEFAULT 0 NOT NULL,
			image_data BLOB NOT NULL,
			width INTEGER DEFAULT 0 NOT NULL,
			height INTEGER DEFAULT 0 NOT NULL
		)`,
		// We omit UNIQUE constraints to verify our manual check logic works in all environments
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}
