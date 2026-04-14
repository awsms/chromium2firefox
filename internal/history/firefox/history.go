package firefox

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chromium2firefox/internal/history/chromium"

	_ "modernc.org/sqlite"
)

const (
	firefoxTransitionLink         = 1
	firefoxTransitionTyped        = 2
	firefoxTransitionBookmark     = 3
	firefoxTransitionEmbed        = 4
	firefoxTransitionRedirectPerm = 5
	firefoxTransitionRedirectTemp = 6
	firefoxTransitionDownload     = 7
	firefoxTransitionFramedLink   = 8
	firefoxTransitionReload       = 9

	chromiumTransitionCoreMask              = 0xFF
	chromiumTransitionClientRedirect        = 0x40000000
	chromiumTransitionServerRedirect        = 0x80000000
	chromiumTransitionChainStart            = 0x10000000
	chromiumTransitionChainEnd              = 0x20000000
	maxCharsToHash                          = 1500
	firefoxGUIDLength                       = 12
	goldenRatio32                    uint32 = 0x9E3779B9
)

type originKey struct {
	prefix string
	host   string
}

type placeState struct {
	id            int64
	url           string
	title         string
	lastVisitDate int64
	hidden        bool
	typed         bool
}

type visitState struct {
	id       int64
	placeID  int64
	date     int64
	typeCode int
}

type iconState struct {
	id      int64
	iconURL string
	width   int
}

type pageIconState struct {
	id      int64
	pageURL string
}

func ImportHistory(ctx context.Context, profileDir string, dataset chromium.Dataset) error {
	placesPath := filepath.Join(profileDir, "places.sqlite")
	if err := ensurePlacesWritable(placesPath); err != nil {
		return err
	}

	if err := backupFile(placesPath); err != nil {
		return fmt.Errorf("backup places.sqlite: %w", err)
	}

	db, err := sql.Open("sqlite", placesPath)
	if err != nil {
		return fmt.Errorf("open firefox places database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	originIDs, err := loadOrigins(ctx, tx)
	if err != nil {
		return err
	}

	placesByURL, err := loadPlaces(ctx, tx)
	if err != nil {
		return err
	}

	visitsByKey, err := loadVisits(ctx, tx)
	if err != nil {
		return err
	}

	typedInputs := make(map[int64]int)
	placeIDsByChromiumURL := make(map[int64]int64, len(dataset.URLs))
	for _, page := range dataset.URLs {
		placeID, err := ensurePlace(ctx, tx, originIDs, placesByURL, page)
		if err != nil {
			return err
		}
		placeIDsByChromiumURL[page.ID] = placeID
		if page.TypedCount > 0 {
			typedInputs[placeID] += page.TypedCount
		}
	}

	insertedVisitIDs := make(map[int64]int64, len(dataset.Visits))
	for _, visit := range dataset.Visits {
		placeID, ok := placeIDsByChromiumURL[visit.URLID]
		if !ok {
			continue
		}

		typeCode := chromiumTransitionToFirefox(visit.Transition)
		visitDate := visit.VisitTime.UTC().UnixMicro()
		key := visitKey(placeID, visitDate, typeCode)
		if existing, ok := visitsByKey[key]; ok {
			insertedVisitIDs[visit.ID] = existing.id
			continue
		}

		fromVisitID := int64(0)
		if visit.FromVisitID != 0 {
			fromVisitID = insertedVisitIDs[visit.FromVisitID]
		}

		triggeringPlaceID := int64(0)
		if visit.ExternalReferrerURL != "" {
			triggeringPlaceID, err = ensureSyntheticPlace(ctx, tx, originIDs, placesByURL, visit.ExternalReferrerURL)
			if err != nil {
				return err
			}
		}

		id, err := insertVisit(ctx, tx, fromVisitID, placeID, visitDate, typeCode, triggeringPlaceID)
		if err != nil {
			return err
		}

		insertedVisitIDs[visit.ID] = id
		visitsByKey[key] = visitState{
			id:       id,
			placeID:  placeID,
			date:     visitDate,
			typeCode: typeCode,
		}
	}

	if err := reconcilePlaces(ctx, tx, placesByURL); err != nil {
		return err
	}

	if err := reconcileInputHistory(ctx, tx, typedInputs); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func ImportFavicons(ctx context.Context, profileDir string, favicons []chromium.Favicon) error {
	if len(favicons) == 0 {
		return nil
	}

	faviconsPath := filepath.Join(profileDir, "favicons.sqlite")
	if err := ensurePlacesWritable(faviconsPath); err != nil {
		return err
	}

	if err := backupFile(faviconsPath); err != nil {
		return fmt.Errorf("backup favicons.sqlite: %w", err)
	}

	db, err := sql.Open("sqlite", faviconsPath)
	if err != nil {
		return fmt.Errorf("open firefox favicons database: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin favicon transaction: %w", err)
	}
	defer tx.Rollback()

	pagesByURL, err := loadPagesWithIcons(ctx, tx)
	if err != nil {
		return err
	}
	iconsByKey, err := loadIcons(ctx, tx)
	if err != nil {
		return err
	}
	mappings, err := loadIconMappings(ctx, tx)
	if err != nil {
		return err
	}

	for _, item := range favicons {
		pageID, err := ensurePageWithIcon(ctx, tx, pagesByURL, item.PageURL)
		if err != nil {
			return err
		}
		iconID, err := ensureIcon(ctx, tx, iconsByKey, item)
		if err != nil {
			return err
		}
		if err := ensureIconMapping(ctx, tx, mappings, pageID, iconID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit favicon transaction: %w", err)
	}

	return nil
}

func ensurePlacesWritable(placesPath string) error {
	info, err := os.Stat(placesPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", placesPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", placesPath)
	}
	return nil
}

func backupFile(path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	backupPath := fmt.Sprintf("%s.chromium2firefox.%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	dst, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}

	return dst.Sync()
}

func loadOrigins(ctx context.Context, tx *sql.Tx) (map[originKey]int64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, prefix, host
FROM moz_origins
`)
	if err != nil {
		return nil, fmt.Errorf("load origins: %w", err)
	}
	defer rows.Close()

	out := make(map[originKey]int64)
	for rows.Next() {
		var (
			id     int64
			prefix string
			host   string
		)
		if err := rows.Scan(&id, &prefix, &host); err != nil {
			return nil, fmt.Errorf("scan origin: %w", err)
		}
		out[originKey{prefix: prefix, host: host}] = id
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate origins: %w", err)
	}

	return out, nil
}

func loadPlaces(ctx context.Context, tx *sql.Tx) (map[string]*placeState, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, url, COALESCE(title, ''), COALESCE(last_visit_date, 0), hidden, typed
FROM moz_places
`)
	if err != nil {
		return nil, fmt.Errorf("load places: %w", err)
	}
	defer rows.Close()

	out := make(map[string]*placeState)
	for rows.Next() {
		var (
			state  placeState
			hidden int
			typed  int
		)
		if err := rows.Scan(&state.id, &state.url, &state.title, &state.lastVisitDate, &hidden, &typed); err != nil {
			return nil, fmt.Errorf("scan place: %w", err)
		}
		state.hidden = hidden != 0
		state.typed = typed != 0
		out[state.url] = &state
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate places: %w", err)
	}

	return out, nil
}

func loadVisits(ctx context.Context, tx *sql.Tx) (map[string]visitState, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, place_id, visit_date, visit_type
FROM moz_historyvisits
`)
	if err != nil {
		return nil, fmt.Errorf("load existing visits: %w", err)
	}
	defer rows.Close()

	out := make(map[string]visitState)
	for rows.Next() {
		var state visitState
		if err := rows.Scan(&state.id, &state.placeID, &state.date, &state.typeCode); err != nil {
			return nil, fmt.Errorf("scan visit: %w", err)
		}
		out[visitKey(state.placeID, state.date, state.typeCode)] = state
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visits: %w", err)
	}

	return out, nil
}

func loadPagesWithIcons(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, page_url
FROM moz_pages_w_icons
`)
	if err != nil {
		return nil, fmt.Errorf("load pages with icons: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var id int64
		var pageURL string
		if err := rows.Scan(&id, &pageURL); err != nil {
			return nil, fmt.Errorf("scan page with icon: %w", err)
		}
		out[pageURL] = id
	}
	return out, rows.Err()
}

func loadIcons(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, icon_url, width
FROM moz_icons
`)
	if err != nil {
		return nil, fmt.Errorf("load icons: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var (
			id      int64
			iconURL string
			width   int
		)
		if err := rows.Scan(&id, &iconURL, &width); err != nil {
			return nil, fmt.Errorf("scan icon: %w", err)
		}
		out[iconKey(iconURL, width)] = id
	}
	return out, rows.Err()
}

func loadIconMappings(ctx context.Context, tx *sql.Tx) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT page_id, icon_id
FROM moz_icons_to_pages
`)
	if err != nil {
		return nil, fmt.Errorf("load icon mappings: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var pageID, iconID int64
		if err := rows.Scan(&pageID, &iconID); err != nil {
			return nil, fmt.Errorf("scan icon mapping: %w", err)
		}
		out[mappingKey(pageID, iconID)] = struct{}{}
	}
	return out, rows.Err()
}

func ensurePlace(
	ctx context.Context,
	tx *sql.Tx,
	origins map[originKey]int64,
	placesByURL map[string]*placeState,
	page chromium.URL,
) (int64, error) {
	if state, ok := placesByURL[page.URL]; ok {
		if page.Title != "" && page.LastVisitTime.UTC().UnixMicro() >= state.lastVisitDate {
			state.title = page.Title
		}
		if page.LastVisitTime.UTC().UnixMicro() > state.lastVisitDate {
			state.lastVisitDate = page.LastVisitTime.UTC().UnixMicro()
		}
		state.hidden = state.hidden && page.Hidden
		state.typed = state.typed || page.TypedCount > 0
		return state.id, nil
	}

	originID, revHost, err := ensureOriginForURL(ctx, tx, origins, page.URL)
	if err != nil {
		return 0, err
	}

	guid, err := generateGUID()
	if err != nil {
		return 0, fmt.Errorf("generate guid for %s: %w", page.URL, err)
	}

	id, err := insertPlace(ctx, tx, placeInsert{
		URL:           page.URL,
		Title:         page.Title,
		RevHost:       revHost,
		VisitCount:    page.VisitCount,
		Hidden:        page.Hidden,
		Typed:         page.TypedCount > 0,
		Frecency:      -1,
		LastVisitDate: page.LastVisitTime.UTC().UnixMicro(),
		GUID:          guid,
		URLHash:       hashURL(page.URL),
		OriginID:      originID,
	})
	if err != nil {
		return 0, err
	}

	placesByURL[page.URL] = &placeState{
		id:            id,
		url:           page.URL,
		title:         page.Title,
		lastVisitDate: page.LastVisitTime.UTC().UnixMicro(),
		hidden:        page.Hidden,
		typed:         page.TypedCount > 0,
	}

	return id, nil
}

func ensureSyntheticPlace(
	ctx context.Context,
	tx *sql.Tx,
	origins map[originKey]int64,
	placesByURL map[string]*placeState,
	rawURL string,
) (int64, error) {
	if state, ok := placesByURL[rawURL]; ok {
		return state.id, nil
	}

	originID, revHost, err := ensureOriginForURL(ctx, tx, origins, rawURL)
	if err != nil {
		return 0, nil
	}

	guid, err := generateGUID()
	if err != nil {
		return 0, err
	}

	id, err := insertPlace(ctx, tx, placeInsert{
		URL:           rawURL,
		RevHost:       revHost,
		Frecency:      -1,
		LastVisitDate: 0,
		GUID:          guid,
		URLHash:       hashURL(rawURL),
		OriginID:      originID,
	})
	if err != nil {
		return 0, err
	}

	placesByURL[rawURL] = &placeState{id: id, url: rawURL}
	return id, nil
}

func ensurePageWithIcon(ctx context.Context, tx *sql.Tx, pagesByURL map[string]int64, pageURL string) (int64, error) {
	if id, ok := pagesByURL[pageURL]; ok {
		return id, nil
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_pages_w_icons (page_url, page_url_hash)
VALUES (?, ?)
`, pageURL, int64(hashURL(pageURL)))
	if err != nil {
		return 0, fmt.Errorf("insert page_with_icon %s: %w", pageURL, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get page_with_icon id for %s: %w", pageURL, err)
	}
	pagesByURL[pageURL] = id
	return id, nil
}

func ensureIcon(ctx context.Context, tx *sql.Tx, iconsByKey map[string]int64, item chromium.Favicon) (int64, error) {
	key := iconKey(item.IconURL, item.Width)
	if id, ok := iconsByKey[key]; ok {
		if _, err := tx.ExecContext(ctx, `
UPDATE moz_icons
SET data = ?, expire_ms = ?, root = ?, flags = 0
WHERE id = ?
`, item.ImageData, chromiumMillisToUnixMillis(item.LastUpdated), boolToInt(isRootIcon(item.IconURL)), id); err != nil {
			return 0, fmt.Errorf("update icon %s: %w", item.IconURL, err)
		}
		return id, nil
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_icons (
	icon_url, fixed_icon_url_hash, width, root, color, expire_ms, flags, data
)
VALUES (?, ?, ?, ?, NULL, ?, 0, ?)
`,
		item.IconURL,
		int64(hashURL(item.IconURL)),
		item.Width,
		boolToInt(isRootIcon(item.IconURL)),
		chromiumMillisToUnixMillis(item.LastUpdated),
		item.ImageData,
	)
	if err != nil {
		return 0, fmt.Errorf("insert icon %s: %w", item.IconURL, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get icon id for %s: %w", item.IconURL, err)
	}
	iconsByKey[key] = id
	return id, nil
}

func ensureIconMapping(ctx context.Context, tx *sql.Tx, mappings map[string]struct{}, pageID, iconID int64) error {
	key := mappingKey(pageID, iconID)
	if _, ok := mappings[key]; ok {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO moz_icons_to_pages (page_id, icon_id, expire_ms)
VALUES (?, ?, 0)
`, pageID, iconID); err != nil {
		return fmt.Errorf("insert icon mapping %d->%d: %w", pageID, iconID, err)
	}
	mappings[key] = struct{}{}
	return nil
}

func ensureOriginForURL(ctx context.Context, tx *sql.Tx, origins map[originKey]int64, rawURL string) (int64, string, error) {
	prefix, host, revHost, err := splitURL(rawURL)
	if err != nil {
		return 0, "", fmt.Errorf("parse %s: %w", rawURL, err)
	}

	key := originKey{prefix: prefix, host: host}
	if id, ok := origins[key]; ok {
		return id, revHost, nil
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_origins (prefix, host, frecency, recalc_frecency, alt_frecency, recalc_alt_frecency)
VALUES (?, ?, 0, 1, NULL, 1)
`, prefix, host)
	if err != nil {
		return 0, "", fmt.Errorf("insert origin for %s: %w", rawURL, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, "", fmt.Errorf("get origin id for %s: %w", rawURL, err)
	}

	origins[key] = id
	return id, revHost, nil
}

type placeInsert struct {
	URL           string
	Title         string
	RevHost       string
	VisitCount    int
	Hidden        bool
	Typed         bool
	Frecency      int
	LastVisitDate int64
	GUID          string
	URLHash       uint64
	OriginID      int64
}

func insertPlace(ctx context.Context, tx *sql.Tx, item placeInsert) (int64, error) {
	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_places (
	url, title, rev_host, visit_count, hidden, typed, frecency,
	last_visit_date, guid, foreign_count, url_hash, origin_id,
	recalc_frecency, alt_frecency, recalc_alt_frecency
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 1, NULL, 1)
`,
		item.URL,
		nullIfEmpty(item.Title),
		nullIfEmpty(item.RevHost),
		item.VisitCount,
		boolToInt(item.Hidden),
		boolToInt(item.Typed),
		item.Frecency,
		nullIfZero(item.LastVisitDate),
		item.GUID,
		int64(item.URLHash),
		item.OriginID,
	)
	if err != nil {
		return 0, fmt.Errorf("insert place %s: %w", item.URL, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get place id for %s: %w", item.URL, err)
	}

	return id, nil
}

func insertVisit(ctx context.Context, tx *sql.Tx, fromVisitID, placeID, visitDate int64, visitType int, triggeringPlaceID int64) (int64, error) {
	result, err := tx.ExecContext(ctx, `
INSERT INTO moz_historyvisits (
	from_visit, place_id, visit_date, visit_type, session, source, triggeringPlaceId
)
VALUES (?, ?, ?, ?, 0, 0, ?)
`,
		nullIfZero(fromVisitID),
		placeID,
		visitDate,
		visitType,
		nullIfZero(triggeringPlaceID),
	)
	if err != nil {
		return 0, fmt.Errorf("insert visit for place %d: %w", placeID, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get inserted visit id for place %d: %w", placeID, err)
	}

	return id, nil
}

func reconcilePlaces(ctx context.Context, tx *sql.Tx, placesByURL map[string]*placeState) error {
	stmt, err := tx.PrepareContext(ctx, `
UPDATE moz_places
SET
	title = ?,
	hidden = ?,
	typed = ?,
	last_visit_date = (
		SELECT MAX(visit_date)
		FROM moz_historyvisits
		WHERE place_id = moz_places.id
	),
	visit_count = (
		SELECT COUNT(*)
		FROM moz_historyvisits
		WHERE place_id = moz_places.id
	),
	frecency = -1,
	recalc_frecency = 1,
	recalc_alt_frecency = 1
WHERE id = ?
`)
	if err != nil {
		return fmt.Errorf("prepare place reconciliation: %w", err)
	}
	defer stmt.Close()

	for _, place := range placesByURL {
		if _, err := stmt.ExecContext(
			ctx,
			nullIfEmpty(place.title),
			boolToInt(place.hidden),
			boolToInt(place.typed),
			place.id,
		); err != nil {
			return fmt.Errorf("reconcile place %s: %w", place.url, err)
		}
	}

	return nil
}

func reconcileInputHistory(ctx context.Context, tx *sql.Tx, typedInputs map[int64]int) error {
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO moz_inputhistory (place_id, input, use_count)
VALUES (?, ?, ?)
ON CONFLICT(place_id, input) DO UPDATE SET
	use_count = moz_inputhistory.use_count + excluded.use_count
`)
	if err != nil {
		return fmt.Errorf("prepare inputhistory reconciliation: %w", err)
	}
	defer stmt.Close()

	for placeID, count := range typedInputs {
		if count <= 0 {
			continue
		}
		if _, err := stmt.ExecContext(ctx, placeID, "", count); err != nil {
			return fmt.Errorf("reconcile inputhistory for place %d: %w", placeID, err)
		}
	}

	return nil
}

func visitKey(placeID, visitDate int64, visitType int) string {
	return fmt.Sprintf("%d:%d:%d", placeID, visitDate, visitType)
}

func splitURL(rawURL string) (prefix, host, revHost string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", err
	}
	if parsed.Scheme == "" {
		return "", "", "", errors.New("missing URL scheme")
	}

	host = strings.ToLower(parsed.Hostname())
	switch {
	case parsed.Opaque != "" && parsed.Host == "":
		prefix = parsed.Scheme + ":"
	case parsed.Host != "" || parsed.Scheme == "file":
		prefix = parsed.Scheme + "://"
	default:
		prefix = parsed.Scheme + ":"
	}

	if host != "" {
		revHost = reverseString(host) + "."
	}

	return prefix, host, revHost, nil
}

func reverseString(value string) string {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func chromiumTransitionToFirefox(transition int) int {
	if transition&chromiumTransitionServerRedirect != 0 || transition&chromiumTransitionClientRedirect != 0 {
		return firefoxTransitionRedirectTemp
	}

	switch transition & chromiumTransitionCoreMask {
	case 0:
		return firefoxTransitionLink
	case 1:
		return firefoxTransitionTyped
	case 2:
		return firefoxTransitionBookmark
	case 3:
		return firefoxTransitionEmbed
	case 4:
		return firefoxTransitionFramedLink
	case 8:
		return firefoxTransitionReload
	case 5, 6, 7, 9, 10:
		return firefoxTransitionLink
	default:
		return firefoxTransitionLink
	}
}

func chromiumMillisToUnixMillis(value int64) int64 {
	if value == 0 {
		return 0
	}
	return chromiumMicrosToUnixMicros(value) / 1000
}

func chromiumMicrosToUnixMicros(value int64) int64 {
	const chromiumEpochOffsetMicros = int64(11644473600) * 1_000_000
	return value - chromiumEpochOffsetMicros
}

func generateGUID() (string, error) {
	buf := make([]byte, firefoxGUIDLength/4*3)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashURL(spec string) uint64 {
	specHead := spec
	if len(specHead) > maxCharsToHash {
		specHead = specHead[:maxCharsToHash]
	}

	strHash := hashString(specHead)
	prefixHash := uint64(0)
	if prefix := schemePrefix(spec); prefix != "" {
		prefixHash = uint64(hashString(prefix) & 0x0000FFFF)
	}

	if prefixHash == 0 {
		return uint64(strHash)
	}
	return (prefixHash << 32) + uint64(strHash)
}

func iconKey(iconURL string, width int) string {
	return fmt.Sprintf("%s\x00%d", iconURL, width)
}

func mappingKey(pageID, iconID int64) string {
	return fmt.Sprintf("%d:%d", pageID, iconID)
}

func isRootIcon(iconURL string) bool {
	return strings.HasSuffix(strings.ToLower(iconURL), "/favicon.ico")
}

func schemePrefix(spec string) string {
	head := spec
	if len(head) > 50 {
		head = head[:50]
	}
	index := strings.IndexByte(head, ':')
	if index <= 0 {
		return ""
	}
	return head[:index]
}

func hashString(value string) uint32 {
	var hash uint32
	for i := 0; i < len(value); i++ {
		hash = addToHash(hash, uint32(value[i]))
	}
	return hash
}

func addToHash(hash, value uint32) uint32 {
	return goldenRatio32 * (bitsRotateLeft32(hash, 5) ^ value)
}

func bitsRotateLeft32(value uint32, amount int) uint32 {
	return (value << amount) | (value >> (32 - amount))
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullIfZero[T ~int64](value T) any {
	if value == 0 {
		return nil
	}
	return int64(value)
}
