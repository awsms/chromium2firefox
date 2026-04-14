package firefox

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"chromium2firefox/internal/history/chromium"

	_ "modernc.org/sqlite"
)

func TestImportHistoryMergesPlacesAndVisits(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	profileDir := t.TempDir()
	placesPath := filepath.Join(profileDir, "places.sqlite")
	faviconsPath := filepath.Join(profileDir, "favicons.sqlite")
	chromiumPath := filepath.Join(t.TempDir(), "History")
	chromiumFaviconsPath := filepath.Join(t.TempDir(), "Favicons")

	createFirefoxTestDB(t, placesPath)
	createFirefoxFaviconsTestDB(t, faviconsPath)
	createChromiumTestDB(t, chromiumPath)
	createChromiumFaviconsTestDB(t, chromiumFaviconsPath)

	dataset, err := chromium.ReadHistory(ctx, chromiumPath)
	if err != nil {
		t.Fatalf("ReadHistory() error = %v", err)
	}

	if err := ImportHistory(ctx, profileDir, dataset); err != nil {
		t.Fatalf("ImportHistory() error = %v", err)
	}
	faviconDataset, err := chromium.ReadFavicons(ctx, chromiumFaviconsPath)
	if err != nil {
		t.Fatalf("ReadFavicons() error = %v", err)
	}
	if err := ImportFavicons(ctx, profileDir, faviconDataset); err != nil {
		t.Fatalf("ImportFavicons() error = %v", err)
	}

	db := openTestDB(t, placesPath)
	defer db.Close()

	var example struct {
		title         string
		visitCount    int
		typed         int
		lastVisitDate int64
	}
	if err := db.QueryRow(`
SELECT COALESCE(title, ''), visit_count, typed, COALESCE(last_visit_date, 0)
FROM moz_places
WHERE url = 'https://example.com/'
`).Scan(&example.title, &example.visitCount, &example.typed, &example.lastVisitDate); err != nil {
		t.Fatalf("query example place: %v", err)
	}

	if example.title != "Example Imported" {
		t.Fatalf("example title = %q, want %q", example.title, "Example Imported")
	}
	if example.visitCount != 2 {
		t.Fatalf("example visit_count = %d, want 2", example.visitCount)
	}
	if example.typed != 1 {
		t.Fatalf("example typed = %d, want 1", example.typed)
	}

	var mozillaCount int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM moz_places
WHERE url = 'https://mozilla.org/'
`).Scan(&mozillaCount); err != nil {
		t.Fatalf("query mozilla place: %v", err)
	}
	if mozillaCount != 1 {
		t.Fatalf("mozilla place count = %d, want 1", mozillaCount)
	}

	var visitCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM moz_historyvisits`).Scan(&visitCount); err != nil {
		t.Fatalf("query visit count: %v", err)
	}
	if visitCount != 3 {
		t.Fatalf("visit count = %d, want 3", visitCount)
	}

	var inputUseCount int
	if err := db.QueryRow(`
SELECT use_count
FROM moz_inputhistory ih
JOIN moz_places p ON p.id = ih.place_id
WHERE p.url = 'https://example.com/' AND ih.input = ''
`).Scan(&inputUseCount); err != nil {
		t.Fatalf("query input history: %v", err)
	}
	if inputUseCount != 2 {
		t.Fatalf("inputhistory use_count = %d, want 2", inputUseCount)
	}

	var backupCount int
	pattern := placesPath + ".chromium2firefox.*.bak"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Glob(%q) error = %v", pattern, err)
	}
	backupCount = len(matches)
	if backupCount != 1 {
		t.Fatalf("backup count = %d, want 1", backupCount)
	}

	favDB := openTestDB(t, faviconsPath)
	defer favDB.Close()

	var faviconLinkCount int
	if err := favDB.QueryRow(`
SELECT COUNT(*)
FROM moz_icons_to_pages ip
JOIN moz_pages_w_icons p ON p.id = ip.page_id
WHERE p.page_url = 'https://example.com/'
`).Scan(&faviconLinkCount); err != nil {
		t.Fatalf("query favicon link count: %v", err)
	}
	if faviconLinkCount != 1 {
		t.Fatalf("favicon link count = %d, want 1", faviconLinkCount)
	}

	var icon struct {
		iconURL string
		width   int
		dataLen int
	}
	if err := favDB.QueryRow(`
SELECT i.icon_url, i.width, length(i.data)
FROM moz_icons i
JOIN moz_icons_to_pages ip ON ip.icon_id = i.id
JOIN moz_pages_w_icons p ON p.id = ip.page_id
WHERE p.page_url = 'https://example.com/'
`).Scan(&icon.iconURL, &icon.width, &icon.dataLen); err != nil {
		t.Fatalf("query imported icon: %v", err)
	}
	if icon.iconURL != "https://example.com/favicon.ico" {
		t.Fatalf("icon_url = %q, want %q", icon.iconURL, "https://example.com/favicon.ico")
	}
	if icon.width != 16 {
		t.Fatalf("icon width = %d, want 16", icon.width)
	}
	if icon.dataLen == 0 {
		t.Fatal("icon data is empty")
	}
}

func createFirefoxTestDB(t *testing.T, path string) {
	t.Helper()

	db := openTestDB(t, path)
	defer db.Close()

	stmts := []string{
		`CREATE TABLE moz_origins (
			id INTEGER PRIMARY KEY,
			prefix TEXT NOT NULL,
			host TEXT NOT NULL,
			frecency INTEGER NOT NULL,
			recalc_frecency INTEGER NOT NULL DEFAULT 0,
			alt_frecency INTEGER,
			recalc_alt_frecency INTEGER NOT NULL DEFAULT 0,
			UNIQUE (prefix, host)
		)`,
		`CREATE TABLE moz_places (
			id INTEGER PRIMARY KEY,
			url LONGVARCHAR,
			title LONGVARCHAR,
			rev_host LONGVARCHAR,
			visit_count INTEGER DEFAULT 0,
			hidden INTEGER DEFAULT 0 NOT NULL,
			typed INTEGER DEFAULT 0 NOT NULL,
			frecency INTEGER DEFAULT -1 NOT NULL,
			last_visit_date INTEGER,
			guid TEXT,
			foreign_count INTEGER DEFAULT 0 NOT NULL,
			url_hash INTEGER DEFAULT 0 NOT NULL,
			description TEXT,
			preview_image_url TEXT,
			site_name TEXT,
			origin_id INTEGER REFERENCES moz_origins(id),
			recalc_frecency INTEGER NOT NULL DEFAULT 0,
			alt_frecency INTEGER,
			recalc_alt_frecency INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX moz_places_guid_uniqueindex ON moz_places (guid)`,
		`CREATE TABLE moz_historyvisits (
			id INTEGER PRIMARY KEY,
			from_visit INTEGER,
			place_id INTEGER,
			visit_date INTEGER,
			visit_type INTEGER,
			session INTEGER,
			source INTEGER DEFAULT 0 NOT NULL,
			triggeringPlaceId INTEGER
		)`,
		`CREATE TABLE moz_inputhistory (
			place_id INTEGER NOT NULL,
			input LONGVARCHAR NOT NULL,
			use_count INTEGER,
			PRIMARY KEY (place_id, input)
		)`,
		`INSERT INTO moz_origins (id, prefix, host, frecency, recalc_frecency, recalc_alt_frecency)
		 VALUES (1, 'https://', 'example.com', 1, 0, 0)`,
		`INSERT INTO moz_places (
			id, url, title, rev_host, visit_count, hidden, typed, frecency, last_visit_date,
			guid, foreign_count, url_hash, origin_id, recalc_frecency, recalc_alt_frecency
		)
		VALUES (
			1, 'https://example.com/', 'Example Existing', 'moc.elpmaxe.', 1, 0, 0, -1, 1710000000000000,
			'EXISTINGGUID1', 0, 111, 1, 0, 0
		)`,
		`INSERT INTO moz_historyvisits (
			id, from_visit, place_id, visit_date, visit_type, session, source, triggeringPlaceId
		)
		VALUES (1, NULL, 1, 1710000000000000, 1, 0, 0, NULL)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}

func createChromiumTestDB(t *testing.T, path string) {
	t.Helper()

	db := openTestDB(t, path)
	defer db.Close()

	stmts := []string{
		`CREATE TABLE urls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url LONGVARCHAR,
			title LONGVARCHAR,
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
			visit_duration INTEGER DEFAULT 0 NOT NULL,
			incremented_omnibox_typed_score BOOLEAN DEFAULT FALSE NOT NULL,
			opener_visit INTEGER,
			originator_cache_guid TEXT,
			originator_visit_id INTEGER,
			originator_from_visit INTEGER,
			originator_opener_visit INTEGER,
			is_known_to_sync BOOLEAN DEFAULT FALSE NOT NULL,
			consider_for_ntp_most_visited BOOLEAN DEFAULT FALSE NOT NULL,
			visited_link_id INTEGER DEFAULT 0 NOT NULL,
			app_id TEXT
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}

	baseTime := time.UnixMicro(1710000000000000).UTC()
	laterTime := baseTime.Add(10 * time.Minute)
	mozillaTime := baseTime.Add(20 * time.Minute)

	insertURL := `INSERT INTO urls (id, url, title, visit_count, typed_count, last_visit_time, hidden) VALUES (?, ?, ?, ?, ?, ?, ?)`
	insertVisit := `INSERT INTO visits (id, url, visit_time, from_visit, external_referrer_url, transition, visit_duration) VALUES (?, ?, ?, ?, ?, ?, ?)`

	rows := []struct {
		id            int
		url           string
		title         string
		visitCount    int
		typedCount    int
		lastVisitTime int64
		hidden        int
	}{
		{1, "https://example.com/", "Example Imported", 2, 2, unixToChromiumMicros(laterTime.UnixMicro()), 0},
		{2, "https://mozilla.org/", "Mozilla", 1, 0, unixToChromiumMicros(mozillaTime.UnixMicro()), 0},
	}
	for _, row := range rows {
		if _, err := db.Exec(insertURL, row.id, row.url, row.title, row.visitCount, row.typedCount, row.lastVisitTime, row.hidden); err != nil {
			t.Fatalf("insert chromium url: %v", err)
		}
	}

	visits := []struct {
		id         int
		urlID      int
		visitTime  int64
		fromVisit  int
		referrer   string
		transition int
		duration   int64
	}{
		{1, 1, unixToChromiumMicros(baseTime.UnixMicro()), 0, "", 0, 0},
		{2, 1, unixToChromiumMicros(laterTime.UnixMicro()), 1, "", 1, int64((2 * time.Second).Microseconds())},
		{3, 2, unixToChromiumMicros(mozillaTime.UnixMicro()), 2, "https://example.com/", 0, int64((3 * time.Second).Microseconds())},
	}
	for _, visit := range visits {
		if _, err := db.Exec(insertVisit, visit.id, visit.urlID, visit.visitTime, nullInt(visit.fromVisit), nullString(visit.referrer), visit.transition, visit.duration); err != nil {
			t.Fatalf("insert chromium visit: %v", err)
		}
	}
}

func createFirefoxFaviconsTestDB(t *testing.T, path string) {
	t.Helper()

	db := openTestDB(t, path)
	defer db.Close()

	stmts := []string{
		`CREATE TABLE moz_icons (
			id INTEGER PRIMARY KEY,
			icon_url TEXT NOT NULL,
			fixed_icon_url_hash INTEGER NOT NULL,
			width INTEGER NOT NULL DEFAULT 0,
			root INTEGER NOT NULL DEFAULT 0,
			color INTEGER,
			expire_ms INTEGER NOT NULL DEFAULT 0,
			flags INTEGER NOT NULL DEFAULT 0,
			data BLOB
		)`,
		`CREATE TABLE moz_pages_w_icons (
			id INTEGER PRIMARY KEY,
			page_url TEXT NOT NULL,
			page_url_hash INTEGER NOT NULL
		)`,
		`CREATE TABLE moz_icons_to_pages (
			page_id INTEGER NOT NULL,
			icon_id INTEGER NOT NULL,
			expire_ms INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (page_id, icon_id)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}

func createChromiumFaviconsTestDB(t *testing.T, path string) {
	t.Helper()

	db := openTestDB(t, path)
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
		`INSERT INTO favicons (id, url, icon_type)
		 VALUES (1, 'https://example.com/favicon.ico', 1)`,
		`INSERT INTO icon_mapping (id, page_url, icon_id)
		 VALUES (1, 'https://example.com/', 1)`,
		`INSERT INTO favicon_bitmaps (id, icon_id, last_updated, image_data, width, height)
		 VALUES (1, 1, 13344473600000000, x'89504E470D0A1A0A', 16, 16)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	return db
}

func unixToChromiumMicros(unixMicros int64) int64 {
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	return unixMicros + chromiumEpochOffsetMicros
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
