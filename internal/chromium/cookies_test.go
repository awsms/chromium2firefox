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

func TestCookieUpsertScenario(t *testing.T) {
	ctx := context.Background()
	chromiumDir := t.TempDir()
	cookiesPath := filepath.Join(chromiumDir, "Cookies_Upsert")

	createChromiumCookiesEmptyDB(t, cookiesPath)

	// 1. Manually insert an initial cookie with different secondary flags
	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.Exec(`INSERT INTO cookies (host_key, name, value, encrypted_value, path, creation_utc, expires_utc, last_access_utc, is_secure, is_httponly, has_expires, is_persistent, source_port, has_cross_site_ancestor) 
		VALUES ('.upsert.com', 'session', 'old-value', x'00', '/', 0, 0, 0, 0, 0, 0, 0, 80, 1)`)
	db.Close()
	if err != nil {
		t.Fatalf("manual insert error = %v", err)
	}

	// 2. Import the same cookie with different flags (ancestor=0, port=443)
	cookies := []Cookie{
		{
			HostKey:               ".upsert.com",
			Name:                  "session",
			Value:                 "new-value",
			Path:                  "/",
			HasCrossSiteAncestor: 0,
			SourcePort:            443,
			IsSecure:              true,
		},
	}

	if err := ImportCookies(ctx, cookiesPath, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies() error = %v", err)
	}

	// 3. Verify it was cleaned up and only the new one exists
	db, _ = sql.Open("sqlite", cookiesPath)
	defer db.Close()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM cookies WHERE host_key = '.upsert.com' AND name = 'session'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 cookie row, got %d (cleanup failed!)", count)
	}

	var port int
	db.QueryRow("SELECT source_port FROM cookies WHERE name = 'session'").Scan(&port)
	if port != 443 {
		t.Errorf("expected port 443 from new cookie, got %d", port)
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
