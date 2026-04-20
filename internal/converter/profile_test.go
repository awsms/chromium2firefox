package converter

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestConvertProfileBidirectional(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	chromiumDir := filepath.Join(tmpDir, "chromium")
	firefoxDir := filepath.Join(tmpDir, "firefox")

	if err := os.MkdirAll(chromiumDir, 0755); err != nil {
		t.Fatalf("MkdirAll(chromiumDir) error = %v", err)
	}
	if err := os.MkdirAll(firefoxDir, 0755); err != nil {
		t.Fatalf("MkdirAll(firefoxDir) error = %v", err)
	}

	chromiumHistory := filepath.Join(chromiumDir, "History")
	chromiumWebData := filepath.Join(chromiumDir, "Web Data")
	firefoxPlaces := filepath.Join(firefoxDir, "places.sqlite")
	firefoxFavicons := filepath.Join(firefoxDir, "favicons.sqlite")

	createChromiumHistoryDB(t, chromiumHistory)
	// Create dummy web data for detection
	createDummyDB(t, chromiumWebData)
	createFirefoxPlacesDB(t, firefoxPlaces)
	// Create dummy favicons for detection
	createDummyDB(t, firefoxFavicons)

	// Test Chromium to Firefox
	options := DefaultOptions()
	if err := ConvertProfile(ctx, chromiumDir, firefoxDir, options); err != nil {
		t.Fatalf("ConvertProfile(C2F) error = %v", err)
	}

	// Verify Firefox has imported data
	db, err := sql.Open("sqlite", firefoxPlaces)
	if err != nil {
		t.Fatalf("sql.Open(firefox) error = %v", err)
	}
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM moz_places WHERE url = 'https://chromium.org/'").Scan(&count)
	db.Close()
	if err != nil {
		t.Fatalf("query firefox error = %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 url in firefox, got %d", count)
	}

	// Test Firefox to Chromium (Reverse)
	// Let's add something unique to Firefox first
	db, _ = sql.Open("sqlite", firefoxPlaces)
	db.Exec("INSERT INTO moz_places (url, title, rev_host, visit_count, guid, url_hash) VALUES ('https://firefox.com/', 'Firefox', 'moc.xoferif.', 1, 'fxfx', 999)")
	db.Close()

	if err := ConvertProfile(ctx, firefoxDir, chromiumDir, options); err != nil {
		t.Fatalf("ConvertProfile(F2C) error = %v", err)
	}

	// Verify Chromium has imported data
	db, err = sql.Open("sqlite", chromiumHistory)
	if err != nil {
		t.Fatalf("sql.Open(chromium) error = %v", err)
	}
	defer db.Close()
	err = db.QueryRow("SELECT COUNT(*) FROM urls WHERE url = 'https://firefox.com/'").Scan(&count)
	if err != nil {
		t.Fatalf("query chromium error = %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 url in chromium, got %d", count)
	}
}

func TestConvertChromiumToChromium(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	sourceDir := filepath.Join(tmpDir, "source")
	targetDir := filepath.Join(tmpDir, "target")

	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("MkdirAll(sourceDir) error = %v", err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("MkdirAll(targetDir) error = %v", err)
	}

	sourceHistory := filepath.Join(sourceDir, "History")
	sourceWebData := filepath.Join(sourceDir, "Web Data")
	targetHistory := filepath.Join(targetDir, "History")
	targetWebData := filepath.Join(targetDir, "Web Data")

	createChromiumHistoryDB(t, sourceHistory)
	createDummyDB(t, sourceWebData)
	createChromiumHistoryDB(t, targetHistory)
	createDummyDB(t, targetWebData)

	// Add unique URL to source
	db, err := sql.Open("sqlite", sourceHistory)
	if err != nil {
		t.Fatalf("sql.Open(source) error = %v", err)
	}
	db.Exec("INSERT INTO urls (url, title, visit_count, last_visit_time) VALUES ('https://source.com/', 'Source', 1, 13344473600000001)")
	db.Close()

	options := DefaultOptions()
	if err := ConvertProfile(ctx, sourceDir, targetDir, options); err != nil {
		t.Fatalf("ConvertProfile(C2C) error = %v", err)
	}

	// Verify target has imported data
	db, err = sql.Open("sqlite", targetHistory)
	if err != nil {
		t.Fatalf("sql.Open(target) error = %v", err)
	}
	defer db.Close()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM urls WHERE url = 'https://source.com/'").Scan(&count)
	if err != nil {
		t.Fatalf("query target error = %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 url in target chromium, got %d", count)
	}
}

func TestConvertChromiumToChromiumCopiesExtensionIndexedDB(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	sourceDir := filepath.Join(tmpDir, "source")
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sourceDir) error = %v", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(targetDir) error = %v", err)
	}

	const extensionID = "kehjnamedhlpcgomlpcpnebpgmmcdbie"
	writePreferencesWithExtension(t, filepath.Join(sourceDir, "Preferences"), extensionID)
	writePreferencesWithExtension(t, filepath.Join(targetDir, "Preferences"), extensionID)

	sourceLevelDB := filepath.Join(sourceDir, "IndexedDB", "chrome-extension_"+extensionID+"_0.indexeddb.leveldb")
	sourceBlob := filepath.Join(sourceDir, "IndexedDB", "chrome-extension_"+extensionID+"_0.indexeddb.blob")
	if err := os.MkdirAll(sourceLevelDB, 0o755); err != nil {
		t.Fatalf("MkdirAll(sourceLevelDB) error = %v", err)
	}
	if err := os.MkdirAll(sourceBlob, 0o755); err != nil {
		t.Fatalf("MkdirAll(sourceBlob) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceLevelDB, "CURRENT"), []byte("source-leveldb"), 0o644); err != nil {
		t.Fatalf("WriteFile(source CURRENT) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceBlob, "1"), []byte("source-blob"), 0o644); err != nil {
		t.Fatalf("WriteFile(source blob) error = %v", err)
	}

	targetLevelDB := filepath.Join(targetDir, "IndexedDB", "chrome-extension_"+extensionID+"_0.indexeddb.leveldb")
	if err := os.MkdirAll(targetLevelDB, 0o755); err != nil {
		t.Fatalf("MkdirAll(targetLevelDB) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetLevelDB, "stale"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(target stale) error = %v", err)
	}

	options := Options{Extensions: true, Merge: true}
	if err := ConvertProfile(ctx, sourceDir, targetDir, options); err != nil {
		t.Fatalf("ConvertProfile(C2C extensions) error = %v", err)
	}

	currentContent, err := os.ReadFile(filepath.Join(targetLevelDB, "CURRENT"))
	if err != nil {
		t.Fatalf("ReadFile(target CURRENT) error = %v", err)
	}
	if string(currentContent) != "source-leveldb" {
		t.Fatalf("expected copied CURRENT file, got %q", string(currentContent))
	}
	if _, err := os.Stat(filepath.Join(targetLevelDB, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale leveldb file to be replaced, err=%v", err)
	}
	blobContent, err := os.ReadFile(filepath.Join(targetDir, "IndexedDB", "chrome-extension_"+extensionID+"_0.indexeddb.blob", "1"))
	if err != nil {
		t.Fatalf("ReadFile(target blob) error = %v", err)
	}
	if string(blobContent) != "source-blob" {
		t.Fatalf("expected copied blob file, got %q", string(blobContent))
	}
}

func createDummyDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	db.Close()
}

func writePreferencesWithExtension(t *testing.T, path, extensionID string) {
	t.Helper()

	content := []byte(`{
  "extensions": {
    "settings": {
      "` + extensionID + `": {}
    }
  }
}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func createChromiumHistoryDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE urls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT,
			title TEXT,
			visit_count INTEGER DEFAULT 0 NOT NULL,
			typed_count INTEGER DEFAULT 0 NOT NULL,
			last_visit_time INTEGER NOT NULL,
			hidden INTEGER DEFAULT 0 NOT NULL
		)`,
		`CREATE TABLE visits (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url INTEGER NOT NULL,
			visit_time INTEGER NOT NULL,
			from_visit INTEGER,
			external_referrer_url TEXT,
			transition INTEGER DEFAULT 0 NOT NULL,
			segment_id INTEGER,
			visit_duration INTEGER DEFAULT 0 NOT NULL
		)`,
		`CREATE TABLE segments (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, url_id INTEGER)`,
		`CREATE TABLE segment_usage (id INTEGER PRIMARY KEY AUTOINCREMENT, segment_id INTEGER, time_slot INTEGER, visit_count INTEGER)`,
		`CREATE UNIQUE INDEX urls_url_index ON urls (url)`,
		`INSERT INTO urls (url, title, visit_count, last_visit_time) VALUES ('https://chromium.org/', 'Chromium', 1, 13344473600000000)`,
		`INSERT INTO visits (url, visit_time) VALUES (1, 13344473600000000)`,
	}
	for _, stmt := range stmts {
		db.Exec(stmt)
	}
}

func createFirefoxPlacesDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE moz_origins (id INTEGER PRIMARY KEY, prefix TEXT, host TEXT, frecency INTEGER, recalc_frecency INTEGER, alt_frecency INTEGER, recalc_alt_frecency INTEGER, UNIQUE(prefix, host))`,
		`CREATE TABLE moz_places (
			id INTEGER PRIMARY KEY,
			url TEXT,
			title TEXT,
			rev_host TEXT,
			visit_count INTEGER,
			hidden INTEGER DEFAULT 0 NOT NULL,
			typed INTEGER DEFAULT 0 NOT NULL,
			frecency INTEGER DEFAULT -1 NOT NULL,
			last_visit_date INTEGER,
			guid TEXT,
			foreign_count INTEGER DEFAULT 0 NOT NULL,
			url_hash INTEGER DEFAULT 0 NOT NULL,
			origin_id INTEGER,
			recalc_frecency INTEGER NOT NULL DEFAULT 0,
			alt_frecency INTEGER,
			recalc_alt_frecency INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE moz_historyvisits (id INTEGER PRIMARY KEY, from_visit INTEGER, place_id INTEGER, visit_date INTEGER, visit_type INTEGER, session INTEGER, source INTEGER DEFAULT 0 NOT NULL, triggeringPlaceId INTEGER)`,
		`CREATE TABLE moz_inputhistory (place_id INTEGER, input TEXT, use_count INTEGER, PRIMARY KEY(place_id, input))`,
		`CREATE TABLE meta (key TEXT, value TEXT)`,
		`INSERT INTO meta (key, value) VALUES ('version', '100')`,
	}
	for _, stmt := range stmts {
		db.Exec(stmt)
	}
}
