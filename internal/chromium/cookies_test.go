package chromium

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportCookies(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	cookiesPath := filepath.Join(chromiumDir, "Cookies")

	createChromiumCookiesEmptyDB(t, cookiesPath)

	cookies := []Cookie{
		{
			HostKey: ".example.com",
			Name:    "test-cookie",
			Value:   "test-value",
			Path:    "/",
		},
	}

	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies() error = %v", err)
	}

	// Test update logic: changing value should not create duplicates
	cookies[0].Value = "updated-value"
	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies(Update) error = %v", err)
	}

	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM cookies WHERE host_key = '.example.com' AND name = 'test-cookie'").Scan(&count)
	if err != nil {
		t.Fatalf("query count error = %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 cookie row, got %d (duplication bug!)", count)
	}

	var encryptedValue []byte
	err = db.QueryRow("SELECT encrypted_value FROM cookies WHERE name = 'test-cookie'").Scan(&encryptedValue)
	if err != nil {
		t.Fatalf("query encrypted_value error = %v", err)
	}

	var sourcePort int
	err = db.QueryRow("SELECT source_port FROM cookies WHERE name = 'test-cookie'").Scan(&sourcePort)
	if err != nil {
		t.Fatalf("query source_port error = %v", err)
	}
	if sourcePort != chromiumCookieSourcePortUnspecified {
		t.Fatalf("source_port = %d, want %d", sourcePort, chromiumCookieSourcePortUnspecified)
	}

	var hasCrossSiteAncestor int
	err = db.QueryRow("SELECT has_cross_site_ancestor FROM cookies WHERE name = 'test-cookie'").Scan(&hasCrossSiteAncestor)
	if err != nil {
		t.Fatalf("query has_cross_site_ancestor error = %v", err)
	}
	if hasCrossSiteAncestor != 1 {
		t.Fatalf("has_cross_site_ancestor = %d, want 1 for unpartitioned cookie", hasCrossSiteAncestor)
	}

	// Try to decrypt it back to verify update
	v11Password, v11PasswordErr := lookupV11Password(ctx, cookiesPath)
	decrypted, err := decryptCookieValue(".example.com", encryptedValue, false, v11Password, v11PasswordErr)
	if err != nil {
		t.Fatalf("decrypt back error = %v", err)
	}

	if decrypted != "updated-value" {
		t.Errorf("decrypted value = %q, want %q", decrypted, "updated-value")
	}
}

func TestImportCookiesDeduplication(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	cookiesPath := filepath.Join(chromiumDir, "Cookies_Dedup")

	createChromiumCookiesEmptyDB(t, cookiesPath)

	// Simulate source data having duplicates
	cookies := []Cookie{
		{
			HostKey:              ".example.com",
			Name:                 "session",
			Value:                "old-value",
			Path:                 "/",
			LastAccessUnixMicros: 100,
		},
		{
			HostKey:              ".example.com",
			Name:                 "session",
			Value:                "new-value",
			Path:                 "/",
			LastAccessUnixMicros: 200, // Newer
		},
	}

	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies() error = %v", err)
	}

	db, _ := sql.Open("sqlite", cookiesPath)
	defer db.Close()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM cookies WHERE host_key = '.example.com' AND name = 'session'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after importing source duplicates, got %d", count)
	}

	var encryptedValue []byte
	db.QueryRow("SELECT encrypted_value FROM cookies WHERE name = 'session'").Scan(&encryptedValue)
	v11Password, v11PasswordErr := lookupV11Password(ctx, cookiesPath)
	decrypted, _ := decryptCookieValue(".example.com", encryptedValue, false, v11Password, v11PasswordErr)
	if decrypted != "new-value" {
		t.Errorf("expected newest source value 'new-value', got %q", decrypted)
	}
}

func TestImportCookiesPreservesDistinctChromiumCookieKeys(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	cookiesPath := filepath.Join(chromiumDir, "Cookies_DistinctKeys")

	createChromiumCookiesEmptyDB(t, cookiesPath)

	cookies := []Cookie{
		{
			HostKey:              ".distinct.com",
			Name:                 "session",
			Value:                "first-value",
			Path:                 "/",
			TopFrameSiteKey:      "https://first.example",
			HasCrossSiteAncestor: 0,
			SourceScheme:         1,
			SourcePort:           80,
		},
		{
			HostKey:              ".distinct.com",
			Name:                 "session",
			Value:                "second-value",
			Path:                 "/",
			TopFrameSiteKey:      "https://second.example",
			HasCrossSiteAncestor: 0,
			SourceScheme:         2,
			SourcePort:           443,
		},
	}

	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies() error = %v", err)
	}

	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM cookies WHERE host_key = '.distinct.com' AND name = 'session'").Scan(&count)
	if err != nil {
		t.Fatalf("query count error = %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 distinct cookie rows, got %d", count)
	}
}

func TestImportCookiesReplacesLegacyUnpartitionedCookieAlias(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	cookiesPath := filepath.Join(chromiumDir, "Cookies_LegacyAlias")

	createChromiumCookiesEmptyDB(t, cookiesPath)

	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.Exec(`INSERT INTO cookies (
		creation_utc, host_key, top_frame_site_key, name, value,
		encrypted_value, path, expires_utc, is_secure, is_httponly,
		last_access_utc, has_expires, is_persistent, priority, samesite,
		source_scheme, source_port, is_public_suffix, last_update_utc,
		source_type, has_cross_site_ancestor
	) VALUES (
		1, '.legacy.com', '', 'session', '', x'00', '/', 0, 0, 0,
		1, 0, 0, 1, -1, 2, -1, 0, 0, 0, 0
	)`)
	if err != nil {
		t.Fatalf("insert legacy alias cookie error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db error = %v", err)
	}

	cookies := []Cookie{
		{
			HostKey: ".legacy.com",
			Name:    "session",
			Value:   "new-value",
			Path:    "/",
		},
	}
	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies() error = %v", err)
	}

	db, err = sql.Open("sqlite", cookiesPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var count int
	var hasCrossSiteAncestor int
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(has_cross_site_ancestor), -1)
		FROM cookies WHERE host_key = '.legacy.com' AND name = 'session'`).Scan(&count, &hasCrossSiteAncestor)
	if err != nil {
		t.Fatalf("query imported alias cookie error = %v", err)
	}
	if count != 1 {
		t.Fatalf("cookie count = %d, want 1", count)
	}
	if hasCrossSiteAncestor != 1 {
		t.Fatalf("has_cross_site_ancestor = %d, want 1", hasCrossSiteAncestor)
	}
}

func TestImportCookiesDynamicColumns(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	cookiesPath := filepath.Join(chromiumDir, "Cookies_Minimal")

	// Create a minimal DB missing several common columns
	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.Exec(`CREATE TABLE cookies (
		host_key TEXT NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		encrypted_value BLOB NOT NULL,
		path TEXT NOT NULL,
		PRIMARY KEY (host_key, name, path)
	)`)
	db.Close()
	if err != nil {
		t.Fatalf("create minimal db error = %v", err)
	}

	cookies := []Cookie{
		{
			HostKey: ".dynamic.com",
			Name:    "minimal-cookie",
			Value:   "minimal-value",
			Path:    "/",
		},
	}

	// This should succeed even though many columns are missing
	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies(Minimal) error = %v", err)
	}

	db, _ = sql.Open("sqlite", cookiesPath)
	defer db.Close()
	var exists bool
	err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM cookies WHERE name = 'minimal-cookie')").Scan(&exists)
	if err != nil || !exists {
		t.Errorf("cookie was not imported into minimal db")
	}
}

func TestV11GCMEncryption(t *testing.T) {
	password := "test-pass"
	key := deriveLinuxCookieKey(password, 32)
	plaintext := []byte("secret-cookie-data")

	ciphertext, err := encryptGCM(plaintext, key)
	if err != nil {
		t.Fatalf("encryptGCM error: %v", err)
	}

	decrypted, err := decryptGCM(ciphertext, key)
	if err != nil {
		t.Fatalf("decryptGCM error: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted = %q, want %q", string(decrypted), string(plaintext))
	}
}

func createChromiumCookiesEmptyDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE cookies (
			creation_utc INTEGER NOT NULL,
			host_key TEXT NOT NULL,
			top_frame_site_key TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			encrypted_value BLOB NOT NULL,
			path TEXT NOT NULL,
			expires_utc INTEGER NOT NULL,
			is_secure INTEGER NOT NULL,
			is_httponly INTEGER NOT NULL,
			last_access_utc INTEGER NOT NULL,
			has_expires INTEGER NOT NULL,
			is_persistent INTEGER NOT NULL,
			priority INTEGER NOT NULL DEFAULT 1,
			samesite INTEGER NOT NULL DEFAULT -1,
			source_scheme INTEGER NOT NULL DEFAULT 0,
			source_port INTEGER NOT NULL DEFAULT -1,
			is_public_suffix INTEGER NOT NULL DEFAULT 0,
			last_update_utc INTEGER NOT NULL DEFAULT 0,
			source_type INTEGER NOT NULL DEFAULT 0,
			has_cross_site_ancestor INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (host_key, name, path, top_frame_site_key)
		)`,
		`CREATE TABLE meta (key LONGVARCHAR NOT NULL UNIQUE, value LONGVARCHAR)`,
		`INSERT INTO meta (key, value) VALUES ('version', '18')`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}
