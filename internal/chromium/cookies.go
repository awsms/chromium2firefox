package chromium

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/awsms/chromium2firefox/internal/progress"

	_ "modernc.org/sqlite"
)

const (
	chromiumCookiePrefixV10 = "v10"
	chromiumCookiePrefixV11 = "v11"
)

type Cookie struct {
	HostKey              string
	TopFrameSiteKey      string
	Name                 string
	Value                string
	Path                 string
	ExpiresUnixMillis    int64
	LastAccessUnixMicros int64
	CreationUnixMicros   int64
	UpdateUnixMicros     int64
	IsSecure             bool
	IsHTTPOnly           bool
	SameSite             int
	SourceScheme         int
	IsPartitioned        bool
}

// ReadCookies extracts all cookies from a Chromium-based browser's database.
func ReadCookies(ctx context.Context, cookiesPath string) ([]Cookie, error) {
	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	// Check if this version of Chromium uses the domain-name hashing prefix (v130+)
	hasDomainHashPrefix, err := chromiumCookiesHaveDomainHashPrefix(ctx, db)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
SELECT
	host_key,
	top_frame_site_key,
	name,
	value,
	encrypted_value,
	path,
	expires_utc,
	last_access_utc,
	creation_utc,
	last_update_utc,
	is_secure,
	is_httponly,
	samesite,
	source_scheme,
	has_expires,
	is_persistent
FROM cookies
ORDER BY last_access_utc ASC
`)
	if err != nil {
		return nil, fmt.Errorf("query cookies: %w", err)
	}
	defer rows.Close()

	// Find the encryption key in the system keyring
	v11Password, v11PasswordErr := lookupV11Password(ctx, cookiesPath)

	var out []Cookie
	for rows.Next() {
		var (
			item           Cookie
			plaintextValue string
			encryptedValue []byte
			isSecure       int
			isHTTPOnly     int
			hasExpires     int
			isPersistent   int
		)
		if err := rows.Scan(
			&item.HostKey,
			&item.TopFrameSiteKey,
			&item.Name,
			&plaintextValue,
			&encryptedValue,
			&item.Path,
			&item.ExpiresUnixMillis,
			&item.LastAccessUnixMicros,
			&item.CreationUnixMicros,
			&item.UpdateUnixMicros,
			&isSecure,
			&isHTTPOnly,
			&item.SameSite,
			&item.SourceScheme,
			&hasExpires,
			&isPersistent,
		); err != nil {
			return nil, fmt.Errorf("scan cookie row: %w", err)
		}

		if plaintextValue != "" {
			item.Value = plaintextValue
		} else {
			// Cleverly auto-detect the encryption method (GCM vs CBC)
			value, err := decryptCookieValue(item.HostKey, encryptedValue, hasDomainHashPrefix, v11Password, v11PasswordErr)
			if err != nil {
				return nil, fmt.Errorf("decrypt cookie %s for %s: %w", item.Name, item.HostKey, err)
			}
			item.Value = value
		}

		item.ExpiresUnixMillis = chromiumMicrosToUnixMillis(item.ExpiresUnixMillis)
		item.LastAccessUnixMicros = chromiumMicrosToUnixMicros(item.LastAccessUnixMicros)
		item.CreationUnixMicros = chromiumMicrosToUnixMicros(item.CreationUnixMicros)
		item.UpdateUnixMicros = chromiumMicrosToUnixMicros(item.UpdateUnixMicros)
		item.IsSecure = isSecure != 0
		item.IsHTTPOnly = isHTTPOnly != 0
		item.IsPartitioned = item.TopFrameSiteKey != ""

		if hasExpires == 0 || isPersistent == 0 {
			item.ExpiresUnixMillis = 0
		}

		out = append(out, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cookies: %w", err)
	}

	return out, nil
}

// ImportCookies merges cookies into a Chromium-based browser's database.
func ImportCookies(ctx context.Context, cookiesPath string, cookies []Cookie, sourceSize int64, reporter progress.Sink) error {
	if len(cookies) == 0 {
		return nil
	}
	if err := ensureRegularFile(cookiesPath); err != nil {
		return err
	}
	if err := backupFile(cookiesPath, reporter); err != nil {
		return fmt.Errorf("backup chromium cookies database: %w", err)
	}
	importSize, finalizeSize := progress.SplitStageSize(sourceSize, 95)
	if reporter != nil {
		reporter.StartStage("importing", cookiesPath, importSize)
	}

	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		return fmt.Errorf("open chromium cookies database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}

	hasDomainHashPrefix, err := chromiumCookiesHaveDomainHashPrefix(ctx, db)
	if err != nil {
		return err
	}

	columns, err := getTableColumns(ctx, db, "cookies")
	if err != nil {
		return fmt.Errorf("get cookies table columns: %w", err)
	}

	v11Password, v11PasswordErr := lookupV11Password(ctx, cookiesPath)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chromium cookies transaction: %w", err)
	}
	defer tx.Rollback()

	progressor := progress.NewStageProgress(reporter, importSize, int64(len(cookies)))
	for _, cookie := range cookies {
		encrypted, err := encryptCookieValue(cookie.HostKey, cookie.Value, hasDomainHashPrefix, v11Password, v11PasswordErr)
		if err != nil {
			return fmt.Errorf("encrypt cookie %s: %w", cookie.Name, err)
		}

		hasExpires := 0
		if cookie.ExpiresUnixMillis > 0 {
			hasExpires = 1
		}

		// Cleverly detect existing cookies using the unique index columns
		checkQuery := "SELECT EXISTS(SELECT 1 FROM cookies WHERE host_key = ? AND name = ? AND path = ?"
		checkArgs := []any{cookie.HostKey, cookie.Name, cookie.Path}
		if columns["top_frame_site_key"] {
			checkQuery += " AND top_frame_site_key = ?"
			checkArgs = append(checkArgs, cookie.TopFrameSiteKey)
		}
		if columns["has_cross_site_ancestor"] {
			checkQuery += " AND has_cross_site_ancestor = ?"
			checkArgs = append(checkArgs, 0)
		}
		if columns["source_scheme"] {
			checkQuery += " AND source_scheme = ?"
			checkArgs = append(checkArgs, cookie.SourceScheme)
		}
		if columns["source_port"] {
			checkQuery += " AND source_port = ?"
			port := 80
			if cookie.IsSecure {
				port = 443
			}
			checkArgs = append(checkArgs, port)
		}
		checkQuery += ")"

		var exists bool
		err = tx.QueryRowContext(ctx, checkQuery, checkArgs...).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check cookie existence: %w", err)
		}

		if exists {
			query, args := buildUpdateCookieQuery(cookie, encrypted, hasExpires, columns)
			_, err = tx.ExecContext(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("update cookie %s: %w", cookie.Name, err)
			}
		} else {
			query, args := buildInsertCookieQuery(cookie, encrypted, hasExpires, columns)
			_, err = tx.ExecContext(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("insert cookie %s: %w", cookie.Name, err)
			}
		}
		progressor.Step(1)
	}

	if reporter != nil {
		reporter.FinishStage("importing", cookiesPath, importSize)
		reporter.StartStage("committing", cookiesPath, finalizeSize)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chromium cookies transaction: %w", err)
	}
	if reporter != nil {
		reporter.FinishStage("committing", cookiesPath, finalizeSize)
	}
	return nil
}

func buildInsertCookieQuery(cookie Cookie, encrypted []byte, hasExpires int, columns map[string]bool) (string, []any) {
	cols := []string{"host_key", "name", "value", "encrypted_value", "path"}
	args := []any{cookie.HostKey, cookie.Name, "", encrypted, cookie.Path}

	if columns["creation_utc"] {
		cols = append(cols, "creation_utc")
		args = append(args, unixMicrosToChromium(cookie.CreationUnixMicros))
	}
	if columns["top_frame_site_key"] {
		cols = append(cols, "top_frame_site_key")
		args = append(args, cookie.TopFrameSiteKey)
	}
	if columns["expires_utc"] {
		cols = append(cols, "expires_utc")
		args = append(args, unixMillisToChromium(cookie.ExpiresUnixMillis))
	}
	if columns["is_secure"] {
		cols = append(cols, "is_secure")
		args = append(args, boolToInt(cookie.IsSecure))
	}
	if columns["is_httponly"] {
		cols = append(cols, "is_httponly")
		args = append(args, boolToInt(cookie.IsHTTPOnly))
	}
	if columns["last_access_utc"] {
		cols = append(cols, "last_access_utc")
		args = append(args, unixMicrosToChromium(cookie.LastAccessUnixMicros))
	}
	if columns["has_expires"] {
		cols = append(cols, "has_expires")
		args = append(args, hasExpires)
	}
	if columns["is_persistent"] {
		cols = append(cols, "is_persistent")
		args = append(args, hasExpires)
	}
	if columns["samesite"] {
		cols = append(cols, "samesite")
		args = append(args, cookie.SameSite)
	}
	if columns["source_scheme"] {
		cols = append(cols, "source_scheme")
		args = append(args, cookie.SourceScheme)
	}
	if columns["last_update_utc"] {
		cols = append(cols, "last_update_utc")
		args = append(args, unixMicrosToChromium(cookie.UpdateUnixMicros))
	}
	if columns["source_port"] {
		cols = append(cols, "source_port")
		port := 80
		if cookie.IsSecure {
			port = 443
		}
		args = append(args, port)
	}
	if columns["is_public_suffix"] {
		cols = append(cols, "is_public_suffix")
		args = append(args, 0)
	}
	if columns["priority"] {
		cols = append(cols, "priority")
		args = append(args, 1)
	}
	if columns["source_type"] {
		cols = append(cols, "source_type")
		args = append(args, 0)
	}
	if columns["has_cross_site_ancestor"] {
		cols = append(cols, "has_cross_site_ancestor")
		args = append(args, 0)
	}

	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = "?"
	}

	return fmt.Sprintf("INSERT INTO cookies (%s) VALUES (%s)",
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", ")), args
}

func buildUpdateCookieQuery(cookie Cookie, encrypted []byte, hasExpires int, columns map[string]bool) (string, []any) {
	sets := []string{"encrypted_value = ?", "value = ''"}
	args := []any{encrypted}

	if columns["expires_utc"] {
		sets = append(sets, "expires_utc = ?")
		args = append(args, unixMillisToChromium(cookie.ExpiresUnixMillis))
	}
	if columns["is_secure"] {
		sets = append(sets, "is_secure = ?")
		args = append(args, boolToInt(cookie.IsSecure))
	}
	if columns["is_httponly"] {
		sets = append(sets, "is_httponly = ?")
		args = append(args, boolToInt(cookie.IsHTTPOnly))
	}
	if columns["last_access_utc"] {
		sets = append(sets, "last_access_utc = ?")
		args = append(args, unixMicrosToChromium(cookie.LastAccessUnixMicros))
	}
	if columns["has_expires"] {
		sets = append(sets, "has_expires = ?")
		args = append(args, hasExpires)
	}
	if columns["is_persistent"] {
		sets = append(sets, "is_persistent = ?")
		args = append(args, hasExpires)
	}
	if columns["samesite"] {
		sets = append(sets, "samesite = ?")
		args = append(args, cookie.SameSite)
	}
	if columns["source_scheme"] {
		sets = append(sets, "source_scheme = ?")
		args = append(args, cookie.SourceScheme)
	}
	if columns["last_update_utc"] {
		sets = append(sets, "last_update_utc = ?")
		args = append(args, unixMicrosToChromium(cookie.UpdateUnixMicros))
	}

	where := "WHERE host_key = ? AND name = ? AND path = ?"
	args = append(args, cookie.HostKey, cookie.Name, cookie.Path)
	if columns["top_frame_site_key"] {
		where += " AND top_frame_site_key = ?"
		args = append(args, cookie.TopFrameSiteKey)
	}
	if columns["has_cross_site_ancestor"] {
		where += " AND has_cross_site_ancestor = ?"
		args = append(args, 0)
	}
	if columns["source_scheme"] {
		where += " AND source_scheme = ?"
		args = append(args, cookie.SourceScheme)
	}
	if columns["source_port"] {
		where += " AND source_port = ?"
		port := 80
		if cookie.IsSecure {
			port = 443
		}
		args = append(args, port)
	}

	return fmt.Sprintf("UPDATE cookies SET %s %s", strings.Join(sets, ", "), where), args
}

func chromiumCookiesHaveDomainHashPrefix(ctx context.Context, db *sql.DB) (bool, error) {
	var version int
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'version'`).Scan(&version)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		if strings.Contains(err.Error(), "no such table: meta") {
			return false, nil
		}
		return false, fmt.Errorf("query cookies meta version: %w", err)
	}
	return version >= 24, nil
}

// decryptCookieValue automatically tries modern GCM and legacy CBC modes for v11 cookies.
func decryptCookieValue(hostKey string, ciphertext []byte, hasDomainHashPrefix bool, v11Password string, v11PasswordErr error) (string, error) {
	var plaintext []byte
	var err error
	switch {
	case bytes.HasPrefix(ciphertext, []byte(chromiumCookiePrefixV10)):
		// Legacy peanuts-based encryption
		plaintext, err = decryptCBC(ciphertext[len(chromiumCookiePrefixV10):], deriveLinuxCookieKey("peanuts", 16))
		if err != nil {
			return "", err
		}
	case bytes.HasPrefix(ciphertext, []byte(chromiumCookiePrefixV11)):
		if v11PasswordErr != nil {
			return "", v11PasswordErr
		}

		payload := ciphertext[len(chromiumCookiePrefixV11):]

		// 1. Try modern GCM (Brave and newer Chrome)
		plaintext, err = decryptGCM(payload, deriveLinuxCookieKey(v11Password, 32))
		if err != nil {
			// 2. Fallback to legacy CBC (standard Chromium)
			plaintext, err = decryptCBC(payload, deriveLinuxCookieKey(v11Password, 16))
			if err != nil {
				return "", fmt.Errorf("v11 decryption failed (tried GCM and CBC): %w", err)
			}
		}
	default:
		return string(ciphertext), nil
	}

	if hasDomainHashPrefix {
		plaintext, err = stripCookieDomainHash(hostKey, plaintext)
		if err != nil {
			return "", err
		}
	}

	return string(plaintext), nil
}

// encryptCookieValue uses the best available encryption for the target browser.
func encryptCookieValue(hostKey, plaintext string, hasDomainHashPrefix bool, v11Password string, v11PasswordErr error) ([]byte, error) {
	data := []byte(plaintext)
	if hasDomainHashPrefix {
		hash := sha256.Sum256([]byte(hostKey))
		data = append(hash[:], data...)
	}

	if v11Password != "" && v11PasswordErr == nil {
		// We use CBC for v11 to match standard Chromium's expectation on Linux.
		ciphertext, err := encryptCBC(data, deriveLinuxCookieKey(v11Password, 16))
		if err != nil {
			return nil, err
		}
		return append([]byte(chromiumCookiePrefixV11), ciphertext...), nil
	}

	// Fallback to v10
	ciphertext, err := encryptCBC(data, deriveLinuxCookieKey("peanuts", 16))
	if err != nil {
		return nil, err
	}
	return append([]byte(chromiumCookiePrefixV10), ciphertext...), nil
}

func stripCookieDomainHash(hostKey string, plaintext []byte) ([]byte, error) {
	if len(plaintext) < sha256.Size {
		return nil, fmt.Errorf("plaintext too short for domain hash")
	}
	expected := sha256.Sum256([]byte(hostKey))
	if !bytes.Equal(plaintext[:sha256.Size], expected[:]) {
		return nil, fmt.Errorf("domain hash mismatch")
	}
	return plaintext[sha256.Size:], nil
}

// lookupV11Password intelligently finds the safe storage key for the specific browser.
func lookupV11Password(ctx context.Context, cookiesPath string) (string, error) {
	// Try to find the correct application name based on path
	app := "chromium"
	abs, _ := filepath.Abs(cookiesPath)
	lower := strings.ToLower(abs)
	if strings.Contains(lower, "brave") {
		app = "brave"
	} else if strings.Contains(lower, "chrome") {
		app = "chrome"
	}

	// 1. Try targeted app (most specific)
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup", "application", app)
	output, err := cmd.Output()
	if err == nil && len(bytes.TrimSpace(output)) > 0 {
		return strings.TrimSpace(string(output)), nil
	}

	// 2. Try generic chromium fallback
	if app != "chromium" {
		cmd = exec.CommandContext(ctx, "secret-tool", "lookup", "application", "chromium")
		output, err = cmd.Output()
		if err == nil && len(bytes.TrimSpace(output)) > 0 {
			return strings.TrimSpace(string(output)), nil
		}
	}

	return "", fmt.Errorf("lookup safe storage password failed for %s", app)
}

func deriveLinuxCookieKey(password string, length int) []byte {
	return pbkdf2HMACSHA1([]byte(password), []byte("saltysalt"), 1, length)
}

func encryptCBC(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	return ciphertext, nil
}

func decryptCBC(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of %d", len(ciphertext), aes.BlockSize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	return pkcs7Unpad(plaintext, aes.BlockSize)
}

func encryptGCM(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

func decryptGCM(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	actualCiphertext := ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, actualCiphertext, nil)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded data length %d", len(data))
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("invalid padding length %d", pad)
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("invalid PKCS#7 padding")
		}
	}
	return data[:len(data)-pad], nil
}

func pbkdf2HMACSHA1(password, salt []byte, iterations, keyLen int) []byte {
	hLen := 20
	numBlocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, numBlocks*hLen)

	for block := 1; block <= numBlocks; block++ {
		u := hmacSHA1(password, append(salt, byte(block>>24), byte(block>>16), byte(block>>8), byte(block)))
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			u = hmacSHA1(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}

	return out[:keyLen]
}

func hmacSHA1(key, data []byte) []byte {
	const blockSize = 64
	if len(key) > blockSize {
		sum := sha1.Sum(key)
		key = sum[:]
	}
	k := make([]byte, blockSize)
	copy(k, key)

	oKeyPad := make([]byte, blockSize)
	iKeyPad := make([]byte, blockSize)
	for i := 0; i < blockSize; i++ {
		oKeyPad[i] = k[i] ^ 0x5c
		iKeyPad[i] = k[i] ^ 0x36
	}

	inner := sha1.New()
	inner.Write(iKeyPad)
	inner.Write(data)
	innerSum := inner.Sum(nil)

	outer := sha1.New()
	outer.Write(oKeyPad)
	outer.Write(innerSum)
	return outer.Sum(nil)
}

func unixMillisToChromium(ms int64) int64 {
	if ms == 0 {
		return 0
	}
	return unixMicrosToChromium(ms * 1000)
}

func unixMicrosToChromium(us int64) int64 {
	if us == 0 {
		return 0
	}
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	return us + chromiumEpochOffsetMicros
}

func chromiumMicrosToUnixMicros(value int64) int64 {
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	return value - chromiumEpochOffsetMicros
}

func chromiumMicrosToUnixMillis(value int64) int64 {
	if value == 0 {
		return 0
	}
	return chromiumMicrosToUnixMicros(value) / 1000
}
