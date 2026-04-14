package firefox

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	"chromium2firefox/internal/chromium"
	"chromium2firefox/internal/progress"

	_ "modernc.org/sqlite"
)

const (
	firefoxSameSiteUnset  = 256
	firefoxSameSiteLax    = 1
	firefoxSameSiteStrict = 2
	firefoxSameSiteNone   = 0

	firefoxSchemeUnset = 0
	firefoxSchemeHTTP  = 1
	firefoxSchemeHTTPS = 2
)

func ImportCookies(ctx context.Context, profileDir string, cookies []chromium.Cookie, sourceSize int64, reporter progress.Sink) error {
	if len(cookies) == 0 {
		return nil
	}

	cookiesPath := filepath.Join(profileDir, "cookies.sqlite")
	if err := ensurePlacesWritable(cookiesPath); err != nil {
		return err
	}

	if err := backupFile(cookiesPath, reporter); err != nil {
		return fmt.Errorf("backup cookies.sqlite: %w", err)
	}
	importSize, finalizeSize := splitStageSize(sourceSize, 95)
	if reporter != nil {
		reporter.StartStage("importing", cookiesPath, importSize)
	}

	db, err := sql.Open("sqlite", cookiesPath)
	if err != nil {
		return fmt.Errorf("open firefox cookies database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cookie transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO moz_cookies (
	originAttributes, name, value, host, path, expiry, lastAccessed, creationTime,
	isSecure, isHttpOnly, sameSite, schemeMap, isPartitionedAttributeSet, updateTime
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(name, host, path, originAttributes) DO UPDATE SET
	value = excluded.value,
	expiry = excluded.expiry,
	lastAccessed = excluded.lastAccessed,
	creationTime = excluded.creationTime,
	isSecure = excluded.isSecure,
	isHttpOnly = excluded.isHttpOnly,
	sameSite = excluded.sameSite,
	schemeMap = excluded.schemeMap,
	isPartitionedAttributeSet = excluded.isPartitionedAttributeSet,
	updateTime = excluded.updateTime
`)
	if err != nil {
		return fmt.Errorf("prepare cookie upsert: %w", err)
	}
	defer stmt.Close()
	progressor := newStageProgress(reporter, importSize, int64(len(cookies)))

	for _, cookie := range cookies {
		originAttrs, err := firefoxOriginAttributes(cookie.TopFrameSiteKey)
		if err != nil {
			return fmt.Errorf("cookie %s for %s: %w", cookie.Name, cookie.HostKey, err)
		}
		isPartitioned := originAttrs != ""

		expiryMillis := int64(0)
		if cookie.ExpiresUnixMillis > 0 {
			expiryMillis = cookie.ExpiresUnixMillis
		}

		if _, err := stmt.ExecContext(
			ctx,
			originAttrs,
			cookie.Name,
			cookie.Value,
			cookie.HostKey,
			cookie.Path,
			expiryMillis,
			cookie.LastAccessUnixMicros,
			cookie.CreationUnixMicros,
			boolToInt(cookie.IsSecure),
			boolToInt(cookie.IsHTTPOnly),
			firefoxCookieSameSite(cookie.SameSite),
			firefoxCookieSchemeMap(cookie.SourceScheme),
			boolToInt(isPartitioned),
			cookie.UpdateUnixMicros,
		); err != nil {
			return fmt.Errorf("upsert cookie %s for %s: %w", cookie.Name, cookie.HostKey, err)
		}
		progressor.Step(1)
	}
	if reporter != nil {
		reporter.FinishStage("importing", cookiesPath, importSize)
		reporter.StartStage("finalizing", cookiesPath, finalizeSize)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cookie transaction: %w", err)
	}
	if reporter != nil {
		reporter.FinishStage("finalizing", cookiesPath, finalizeSize)
	}
	return nil
}

func firefoxOriginAttributes(topFrameSiteKey string) (string, error) {
	if topFrameSiteKey == "" {
		return "", nil
	}

	parsed, err := url.Parse(topFrameSiteKey)
	if err != nil {
		return "", fmt.Errorf("parse top frame site key %q: %w", topFrameSiteKey, err)
	}

	host := parsed.Hostname()
	if parsed.Scheme == "" {
		return "", fmt.Errorf("unsupported top frame site key %q", topFrameSiteKey)
	}

	if parsed.Scheme == "file" && host == "" {
		partitionKey := "(file,)"
		return "^partitionKey=" + url.QueryEscape(partitionKey), nil
	}

	if host == "" {
		return "", nil
	}

	partitionKey := fmt.Sprintf("(%s,%s)", parsed.Scheme, host)
	return "^partitionKey=" + url.QueryEscape(partitionKey), nil
}

func firefoxCookieSameSite(chromiumSameSite int) int {
	switch chromiumSameSite {
	case 1:
		return firefoxSameSiteLax
	case 2:
		return firefoxSameSiteStrict
	case 0:
		return firefoxSameSiteNone
	default:
		return firefoxSameSiteUnset
	}
}

func firefoxCookieSchemeMap(sourceScheme int) int {
	switch sourceScheme {
	case 1:
		return firefoxSchemeHTTP
	case 2:
		return firefoxSchemeHTTPS
	default:
		return firefoxSchemeUnset
	}
}
