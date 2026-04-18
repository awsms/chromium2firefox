package chromium

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportWebData(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	webDataPath := filepath.Join(chromiumDir, "Web Data")

	createChromiumWebDataEmptyDB(t, webDataPath)

	engines := []Engine{
		{
			Name:      "Test Search",
			Keyword:   "test",
			SearchURL: "https://example.com/search?q={searchTerms}",
			IsActive:  true,
		},
	}

	if err := ImportWebData(ctx, webDataPath, engines, 1024, nil); err != nil {
		t.Fatalf("ImportWebData() error = %v", err)
	}

	// Test update logic
	engines[0].Name = "Updated Search"
	if err := ImportWebData(ctx, webDataPath, engines, 1024, nil); err != nil {
		t.Fatalf("ImportWebData(Update) error = %v", err)
	}

	db, err := sql.Open("sqlite", webDataPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var name string
	err = db.QueryRow("SELECT short_name FROM keywords WHERE keyword = 'test'").Scan(&name)
	if err != nil {
		t.Fatalf("query short_name error = %v", err)
	}
	if name != "Updated Search" {
		t.Errorf("expected updated name, got %q", name)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM keywords").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 keyword record, got %d", count)
	}
}

func createChromiumWebDataEmptyDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE keywords (
			id INTEGER PRIMARY KEY,
			short_name TEXT NOT NULL,
			keyword TEXT NOT NULL,
			url TEXT NOT NULL,
			suggest_url TEXT,
			favicon_url TEXT,
			input_encodings TEXT,
			safe_for_autoreplace INTEGER,
			prepopulate_id INTEGER,
			is_active INTEGER,
			date_created INTEGER,
			last_modified INTEGER,
			usage_count INTEGER DEFAULT 0
		)`,
		// We omit UNIQUE constraints to verify our manual check logic works in all environments
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}
