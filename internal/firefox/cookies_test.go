package firefox

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/awsms/chromium2firefox/internal/chromium"

	_ "modernc.org/sqlite"
)

func TestImportCookiesMergesAndPreservesPartitioning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	profileDir := t.TempDir()
	cookiesPath := filepath.Join(profileDir, "cookies.sqlite")
	chromiumCookiesPath := filepath.Join(t.TempDir(), "Cookies")

	createFirefoxCookiesTestDB(t, cookiesPath)
	createChromiumCookiesTestDB(t, chromiumCookiesPath)

	cookies, err := chromium.ReadCookies(ctx, chromiumCookiesPath)
	if err != nil {
		t.Fatalf("ReadCookies() error = %v", err)
	}

	if err := ImportCookies(ctx, profileDir, cookies, 1024, nil); err != nil {
		t.Fatalf("ImportCookies() error = %v", err)
	}

	db := openCookieTestDB(t, cookiesPath)
	defer db.Close()

	var existing struct {
		value                     string
		expiry                    int64
		sameSite                  int
		schemeMap                 int
		isPartitionedAttributeSet int
	}
	if err := db.QueryRow(`
SELECT value, expiry, sameSite, schemeMap, isPartitionedAttributeSet
FROM moz_cookies
WHERE host = '.example.com' AND name = 'sessionid' AND path = '/' AND originAttributes = ''
`).Scan(
		&existing.value,
		&existing.expiry,
		&existing.sameSite,
		&existing.schemeMap,
		&existing.isPartitionedAttributeSet,
	); err != nil {
		t.Fatalf("query imported existing cookie: %v", err)
	}

	if existing.value != "newvalue" {
		t.Fatalf("existing cookie value = %q, want %q", existing.value, "newvalue")
	}
	if existing.expiry != 1710003600000 {
		t.Fatalf("existing cookie expiry = %d, want 1710003600000", existing.expiry)
	}
	if existing.sameSite != firefoxSameSiteLax {
		t.Fatalf("existing cookie sameSite = %d, want %d", existing.sameSite, firefoxSameSiteLax)
	}
	if existing.schemeMap != firefoxSchemeHTTPS {
		t.Fatalf("existing cookie schemeMap = %d, want %d", existing.schemeMap, firefoxSchemeHTTPS)
	}
	if existing.isPartitionedAttributeSet != 0 {
		t.Fatalf("existing cookie partition flag = %d, want 0", existing.isPartitionedAttributeSet)
	}

	var partitioned struct {
		value            string
		originAttributes string
		sameSite         int
		schemeMap        int
		partitionFlag    int
	}
	if err := db.QueryRow(`
SELECT value, originAttributes, sameSite, schemeMap, isPartitionedAttributeSet
FROM moz_cookies
WHERE host = '.youtube.com' AND name = 'PREF'
`).Scan(
		&partitioned.value,
		&partitioned.originAttributes,
		&partitioned.sameSite,
		&partitioned.schemeMap,
		&partitioned.partitionFlag,
	); err != nil {
		t.Fatalf("query partitioned cookie: %v", err)
	}

	if partitioned.value != "youtube-pref" {
		t.Fatalf("partitioned cookie value = %q, want %q", partitioned.value, "youtube-pref")
	}
	if partitioned.originAttributes != "^partitionKey=%28https%2Cyoutube.com%29" {
		t.Fatalf("partitioned cookie originAttributes = %q", partitioned.originAttributes)
	}
	if partitioned.partitionFlag != 1 {
		t.Fatalf("partitioned cookie flag = %d, want 1", partitioned.partitionFlag)
	}
	if partitioned.sameSite != firefoxSameSiteNone {
		t.Fatalf("partitioned cookie sameSite = %d, want %d", partitioned.sameSite, firefoxSameSiteNone)
	}
	if partitioned.schemeMap != firefoxSchemeHTTPS {
		t.Fatalf("partitioned cookie schemeMap = %d, want %d", partitioned.schemeMap, firefoxSchemeHTTPS)
	}

	var filePartition struct {
		value            string
		originAttributes string
		partitionFlag    int
	}
	if err := db.QueryRow(`
SELECT value, originAttributes, isPartitionedAttributeSet
FROM moz_cookies
WHERE host = '.example.org' AND name = 'filecookie'
`).Scan(
		&filePartition.value,
		&filePartition.originAttributes,
		&filePartition.partitionFlag,
	); err != nil {
		t.Fatalf("query file partitioned cookie: %v", err)
	}

	if filePartition.value != "file-scope" {
		t.Fatalf("file cookie value = %q, want %q", filePartition.value, "file-scope")
	}
	if filePartition.originAttributes != "^partitionKey=%28file%2C%29" {
		t.Fatalf("file cookie originAttributes = %q", filePartition.originAttributes)
	}
	if filePartition.partitionFlag != 1 {
		t.Fatalf("file cookie partition flag = %d, want 1", filePartition.partitionFlag)
	}

	var unsetSameSite int
	if err := db.QueryRow(`
SELECT sameSite
FROM moz_cookies
WHERE host = '.unset.example' AND name = 'unsetcookie'
`).Scan(&unsetSameSite); err != nil {
		t.Fatalf("query unset sameSite cookie: %v", err)
	}
	if unsetSameSite != firefoxSameSiteUnset {
		t.Fatalf("unset sameSite = %d, want %d", unsetSameSite, firefoxSameSiteUnset)
	}

	backups, err := filepath.Glob(cookiesPath + ".chromium2firefox.*.bak")
	if err != nil {
		t.Fatalf("Glob(backups): %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1", len(backups))
	}
}

func TestReadCookies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	profileDir := t.TempDir()
	cookiesPath := filepath.Join(profileDir, "cookies.sqlite")

	createFirefoxCookiesTestDB(t, cookiesPath)

	cookies, err := ReadCookies(ctx, cookiesPath)
	if err != nil {
		t.Fatalf("ReadCookies() error = %v", err)
	}

	if len(cookies) == 0 {
		t.Fatal("expected at least one cookie")
	}

	found := false
	for _, c := range cookies {
		if c.Name == "sessionid" {
			found = true
			// 1700000000 seconds -> 1700000000000 ms
			if c.ExpiresUnixMillis != 1700000000000 {
				t.Errorf("expiry = %d, want %d", c.ExpiresUnixMillis, 1700000000000)
			}
			break
		}
	}
	if !found {
		t.Error("sessionid cookie not found")
	}
}

func createFirefoxCookiesTestDB(t *testing.T, path string) {
	t.Helper()

	db := openCookieTestDB(t, path)
	defer db.Close()

	stmts := []string{
		`CREATE TABLE moz_cookies (
			id INTEGER PRIMARY KEY,
			originAttributes TEXT NOT NULL DEFAULT '',
			name TEXT,
			value TEXT,
			host TEXT,
			path TEXT,
			expiry INTEGER,
			lastAccessed INTEGER,
			creationTime INTEGER,
			isSecure INTEGER,
			isHttpOnly INTEGER,
			inBrowserElement INTEGER DEFAULT 0,
			sameSite INTEGER DEFAULT 0,
			schemeMap INTEGER DEFAULT 0,
			isPartitionedAttributeSet INTEGER DEFAULT 0,
			updateTime INTEGER,
			CONSTRAINT moz_uniqueid UNIQUE (name, host, path, originAttributes)
		)`,
		`INSERT INTO moz_cookies (
			id, originAttributes, name, value, host, path, expiry, lastAccessed, creationTime,
			isSecure, isHttpOnly, sameSite, schemeMap, isPartitionedAttributeSet, updateTime
		) VALUES (
			1, '', 'sessionid', 'oldvalue', '.example.com', '/', 1700000000, 1700000000000000, 1700000000000000,
			1, 1, 0, 2, 0, 1700000000000000
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}
}

func createChromiumCookiesTestDB(t *testing.T, path string) {
	t.Helper()

	db := openCookieTestDB(t, path)
	defer db.Close()

	if _, err := db.Exec(`
CREATE TABLE cookies(
	creation_utc INTEGER NOT NULL,
	host_key TEXT NOT NULL,
	top_frame_site_key TEXT NOT NULL,
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
	priority INTEGER NOT NULL,
	samesite INTEGER NOT NULL,
	source_scheme INTEGER NOT NULL,
	source_port INTEGER NOT NULL,
	last_update_utc INTEGER NOT NULL,
	source_type INTEGER NOT NULL,
	has_cross_site_ancestor INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create chromium cookies table: %v", err)
	}

	insert := `
INSERT INTO cookies (
	creation_utc, host_key, top_frame_site_key, name, value, encrypted_value, path, expires_utc,
	is_secure, is_httponly, last_access_utc, has_expires, is_persistent, priority,
	samesite, source_scheme, source_port, last_update_utc, source_type, has_cross_site_ancestor
) VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 443, ?, 0, 0)
`

	rows := []struct {
		creationUTC   int64
		hostKey       string
		topFrame      string
		name          string
		value         string
		path          string
		expiresUTC    int64
		isSecure      int
		isHTTPOnly    int
		lastAccessUTC int64
		hasExpires    int
		isPersistent  int
		sameSite      int
		sourceScheme  int
		lastUpdateUTC int64
	}{
		{
			creationUTC:   unixToChromiumMicros(1710000000000000),
			hostKey:       ".example.com",
			topFrame:      "",
			name:          "sessionid",
			value:         "newvalue",
			path:          "/",
			expiresUTC:    unixToChromiumMicros(1710003600000000),
			isSecure:      1,
			isHTTPOnly:    1,
			lastAccessUTC: unixToChromiumMicros(1710000100000000),
			hasExpires:    1,
			isPersistent:  1,
			sameSite:      1,
			sourceScheme:  2,
			lastUpdateUTC: unixToChromiumMicros(1710000005000000),
		},
		{
			creationUTC:   unixToChromiumMicros(1710000200000000),
			hostKey:       ".youtube.com",
			topFrame:      "https://youtube.com",
			name:          "PREF",
			value:         "youtube-pref",
			path:          "/",
			expiresUTC:    unixToChromiumMicros(1710007200000000),
			isSecure:      1,
			isHTTPOnly:    0,
			lastAccessUTC: unixToChromiumMicros(1710000300000000),
			hasExpires:    1,
			isPersistent:  1,
			sameSite:      0,
			sourceScheme:  2,
			lastUpdateUTC: unixToChromiumMicros(1710000250000000),
		},
		{
			creationUTC:   unixToChromiumMicros(1710000400000000),
			hostKey:       ".example.org",
			topFrame:      "file://",
			name:          "filecookie",
			value:         "file-scope",
			path:          "/",
			expiresUTC:    unixToChromiumMicros(1710008200000000),
			isSecure:      0,
			isHTTPOnly:    0,
			lastAccessUTC: unixToChromiumMicros(1710000500000000),
			hasExpires:    1,
			isPersistent:  1,
			sameSite:      0,
			sourceScheme:  1,
			lastUpdateUTC: unixToChromiumMicros(1710000450000000),
		},
		{
			creationUTC:   unixToChromiumMicros(1710000600000000),
			hostKey:       ".unset.example",
			topFrame:      "",
			name:          "unsetcookie",
			value:         "unset-samesite",
			path:          "/",
			expiresUTC:    unixToChromiumMicros(1710009200000000),
			isSecure:      1,
			isHTTPOnly:    0,
			lastAccessUTC: unixToChromiumMicros(1710000700000000),
			hasExpires:    1,
			isPersistent:  1,
			sameSite:      -1,
			sourceScheme:  2,
			lastUpdateUTC: unixToChromiumMicros(1710000650000000),
		},
	}

	for _, row := range rows {
		encrypted, err := encryptChromiumV10Cookie(row.value)
		if err != nil {
			t.Fatalf("encryptChromiumV10Cookie(%q): %v", row.value, err)
		}
		if _, err := db.Exec(
			insert,
			row.creationUTC,
			row.hostKey,
			row.topFrame,
			row.name,
			encrypted,
			row.path,
			row.expiresUTC,
			row.isSecure,
			row.isHTTPOnly,
			row.lastAccessUTC,
			row.hasExpires,
			row.isPersistent,
			row.sameSite,
			row.sourceScheme,
			row.lastUpdateUTC,
		); err != nil {
			t.Fatalf("insert chromium cookie: %v", err)
		}
	}
}

func openCookieTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v", path, err)
	}
	return db
}

func encryptChromiumV10Cookie(value string) ([]byte, error) {
	key := []byte{
		0xfd, 0x62, 0x1f, 0xe5, 0xa2, 0xb4, 0x02, 0x53,
		0x9d, 0xfa, 0x14, 0x7c, 0xa9, 0x27, 0x27, 0x78,
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	padded := pkcs7Pad([]byte(value), aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return append([]byte("v10"), ciphertext...), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	if pad == 0 {
		pad = blockSize
	}
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}
