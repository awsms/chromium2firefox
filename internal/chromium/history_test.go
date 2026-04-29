package chromium

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestImportHistory(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	historyPath := filepath.Join(chromiumDir, "History")

	createChromiumEmptyDB(t, historyPath)

	now := time.Now().Round(time.Second)
	dataset := Dataset{
		URLs: []URL{
			{ID: 1, URL: "https://example.com/", Title: "Example", VisitCount: 1, LastVisitTime: now, Hidden: false},
		},
		Visits: []Visit{
			{ID: 1, URLID: 1, VisitTime: now, Transition: 1},
		},
	}

	if err := ImportHistory(ctx, historyPath, dataset, 1024, nil); err != nil {
		t.Fatalf("ImportHistory() error = %v", err)
	}

	// Now import again with updated data to test manual UPSERT logic
	dataset.URLs[0].Title = "Updated Example"
	dataset.URLs[0].VisitCount = 5
	if err := ImportHistory(ctx, historyPath, dataset, 1024, nil); err != nil {
		t.Fatalf("ImportHistory(Update) error = %v", err)
	}

	db, err := sql.Open("sqlite", historyPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var url struct {
		title      string
		visitCount int
	}
	err = db.QueryRow("SELECT title, visit_count FROM urls WHERE url = 'https://example.com/'").Scan(&url.title, &url.visitCount)
	if err != nil {
		t.Fatalf("query urls error = %v", err)
	}
	if url.title != "Updated Example" {
		t.Errorf("expected title 'Updated Example', got %q", url.title)
	}
	if url.visitCount != 5 {
		t.Errorf("expected visit count 5, got %d", url.visitCount)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM urls").Scan(&count)
	if count != 1 {
		t.Errorf("expected only 1 url record (correctly merged), got %d", count)
	}
}

func TestImportHistoryPreservesTransitionAndReferrerChain(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	historyPath := filepath.Join(chromiumDir, "History")

	createChromiumEmptyDB(t, historyPath)

	now := time.Now().Round(time.Second)
	dataset := Dataset{
		URLs: []URL{
			{ID: 1, URL: "https://example.com/", Title: "Example", VisitCount: 2, LastVisitTime: now, Hidden: false},
		},
		Visits: []Visit{
			{ID: 11, URLID: 1, VisitTime: now, Transition: 0x10000001},
			{ID: 12, URLID: 1, VisitTime: now.Add(time.Second), FromVisitID: 11, Transition: 0xC0000000},
		},
	}

	if err := ImportHistory(ctx, historyPath, dataset, 1024, nil); err != nil {
		t.Fatalf("ImportHistory() error = %v", err)
	}

	db, err := sql.Open("sqlite", historyPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, from_visit, transition FROM visits ORDER BY visit_time ASC, id ASC LIMIT 2")
	if err != nil {
		t.Fatalf("query visits error = %v", err)
	}
	defer rows.Close()

	type row struct {
		id         int64
		fromVisit  int64
		transition int
	}
	var visits []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.fromVisit, &item.transition); err != nil {
			t.Fatalf("scan visit error = %v", err)
		}
		visits = append(visits, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate visits error = %v", err)
	}
	if len(visits) != 2 {
		t.Fatalf("expected 2 imported visits, got %d", len(visits))
	}
	if visits[0].transition != 0x10000001 {
		t.Fatalf("first transition = %#x, want %#x", visits[0].transition, 0x10000001)
	}
	if visits[1].transition != 0xC0000000 {
		t.Fatalf("second transition = %#x, want %#x", visits[1].transition, 0xC0000000)
	}
	if visits[1].fromVisit != visits[0].id {
		t.Fatalf("second from_visit = %d, want %d", visits[1].fromVisit, visits[0].id)
	}
}

func createChromiumEmptyDB(t *testing.T, path string) {
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
		`CREATE TABLE segments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT,
			url_id INTEGER
		)`,
		`CREATE UNIQUE INDEX segments_name_index ON segments (name)`,
		`CREATE TABLE segment_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			segment_id INTEGER NOT NULL,
			time_slot INTEGER NOT NULL,
			visit_count INTEGER DEFAULT 0 NOT NULL
		)`,
		`CREATE UNIQUE INDEX segment_usage_index ON segment_usage (segment_id, time_slot)`,
		// Note: We intentionally omit UNIQUE index on urls to test our manual check logic
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}
