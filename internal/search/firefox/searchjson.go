package firefox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	chromiumsearch "chromium2firefox/internal/search/chromium"
)

const (
	searchMZLZ4File             = "search.json.mozlz4"
	defaultMozLZ4Binary         = "mozlz4"
	firefoxSearchSuggestionType = "application/x-suggestions+json"
	firefoxSearchLoadPath       = "[other]/chromium2firefox"
)

type settingsFile struct {
	Version  int               `json:"version"`
	Engines  []json.RawMessage `json:"engines"`
	MetaData map[string]any    `json:"metaData"`
}

type persistedEngine struct {
	ID             string               `json:"id"`
	Name           string               `json:"_name"`
	LoadPath       string               `json:"_loadPath,omitempty"`
	IconMap        map[string]string    `json:"_iconMapObj,omitempty"`
	MetaData       map[string]any       `json:"_metaData,omitempty"`
	URLs           []persistedEngineURL `json:"_urls,omitempty"`
	DefinedAliases []string             `json:"_definedAliases,omitempty"`
	QueryCharset   string               `json:"queryCharset,omitempty"`
	IsConfigEngine bool                 `json:"_isConfigEngine,omitempty"`
}

type persistedEngineURL struct {
	Template string                 `json:"template"`
	Params   []persistedEngineParam `json:"params"`
	Rels     []string               `json:"rels"`
	Type     string                 `json:"type,omitempty"`
	Method   string                 `json:"method,omitempty"`
}

type persistedEngineParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func ImportSearchEngines(ctx context.Context, profileDir string, engines []chromiumsearch.Engine) error {
	candidates := filterImportableEngines(engines)
	if len(candidates) == 0 {
		return nil
	}

	settingsPath := filepath.Join(profileDir, searchMZLZ4File)
	if err := ensureSettingsWritable(settingsPath); err != nil {
		return err
	}
	if err := backupSettingsFile(settingsPath); err != nil {
		return fmt.Errorf("backup %s: %w", searchMZLZ4File, err)
	}

	settings, err := readSearchSettings(ctx, settingsPath)
	if err != nil {
		return err
	}

	existingIDs := make(map[string]struct{})
	existingNames := make(map[string]struct{})
	for _, raw := range settings.Engines {
		var existing persistedEngine
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("decode existing engine: %w", err)
		}
		if existing.ID != "" {
			existingIDs[existing.ID] = struct{}{}
		}
		if existing.Name != "" {
			existingNames[strings.ToLower(existing.Name)] = struct{}{}
		}
	}

	for _, engine := range candidates {
		persisted := toPersistedEngine(engine)
		if _, ok := existingIDs[persisted.ID]; ok {
			continue
		}
		if _, ok := existingNames[strings.ToLower(persisted.Name)]; ok {
			continue
		}

		raw, err := json.Marshal(persisted)
		if err != nil {
			return fmt.Errorf("encode engine %s: %w", engine.Name, err)
		}
		settings.Engines = append(settings.Engines, raw)
		existingIDs[persisted.ID] = struct{}{}
		existingNames[strings.ToLower(persisted.Name)] = struct{}{}
	}

	if len(settings.Engines) == 0 {
		return fmt.Errorf("cannot write %s without any engines", searchMZLZ4File)
	}

	return writeSearchSettings(ctx, settingsPath, settings)
}

func ensureSettingsWritable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

func backupSettingsFile(path string) error {
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

func filterImportableEngines(engines []chromiumsearch.Engine) []chromiumsearch.Engine {
	var out []chromiumsearch.Engine
	for _, engine := range engines {
		if !engine.IsActive {
			continue
		}
		if engine.Name == "" || engine.SearchURL == "" {
			continue
		}
		if !strings.Contains(engine.SearchURL, "{searchTerms}") {
			continue
		}
		if engine.SearchURLPostParams != "" || engine.SuggestURLPostParams != "" {
			continue
		}
		out = append(out, engine)
	}
	return out
}

func toPersistedEngine(engine chromiumsearch.Engine) persistedEngine {
	out := persistedEngine{
		ID:       firefoxEngineID(engine),
		Name:     strings.TrimSpace(engine.Name),
		LoadPath: firefoxSearchLoadPath,
		MetaData: map[string]any{
			"user-installed": true,
		},
		URLs: []persistedEngineURL{
			{
				Template: engine.SearchURL,
				Params:   []persistedEngineParam{},
				Rels:     []string{},
			},
		},
	}

	if len(engine.InputEncodings) > 0 {
		out.QueryCharset = engine.InputEncodings[0]
	}
	if engine.Keyword != "" {
		out.DefinedAliases = []string{engine.Keyword}
	}
	if engine.FaviconURL != "" {
		out.IconMap = map[string]string{"16": engine.FaviconURL}
	}
	if engine.SuggestURL != "" && strings.Contains(engine.SuggestURL, "{searchTerms}") {
		out.URLs = append(out.URLs, persistedEngineURL{
			Template: engine.SuggestURL,
			Params:   []persistedEngineParam{},
			Rels:     []string{},
			Type:     firefoxSearchSuggestionType,
		})
	}

	return out
}

func firefoxEngineID(engine chromiumsearch.Engine) string {
	return "chromium-" + strconv.FormatInt(engine.ID, 10)
}

func readSearchSettings(ctx context.Context, path string) (settingsFile, error) {
	decodedFile, err := os.CreateTemp("", "chromium2firefox-search-read-*.json")
	if err != nil {
		return settingsFile{}, fmt.Errorf("create temp decoded %s: %w", searchMZLZ4File, err)
	}
	decodedPath := decodedFile.Name()
	decodedFile.Close()
	defer os.Remove(decodedPath)

	if err := runMozLZ4(ctx, true, path, decodedPath); err != nil {
		return settingsFile{}, fmt.Errorf("decompress %s: %w", searchMZLZ4File, err)
	}

	data, err := os.ReadFile(decodedPath)
	if err != nil {
		return settingsFile{}, fmt.Errorf("read decoded %s: %w", searchMZLZ4File, err)
	}

	var settings settingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		return settingsFile{}, fmt.Errorf("decode %s json: %w", searchMZLZ4File, err)
	}
	if settings.MetaData == nil {
		settings.MetaData = map[string]any{}
	}
	return settings, nil
}

func writeSearchSettings(ctx context.Context, path string, settings settingsFile) error {
	decodedFile, err := os.CreateTemp("", "chromium2firefox-search-write-*.json")
	if err != nil {
		return fmt.Errorf("create temp encoded %s: %w", searchMZLZ4File, err)
	}
	decodedPath := decodedFile.Name()
	decodedFile.Close()
	defer os.Remove(decodedPath)

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode %s json: %w", searchMZLZ4File, err)
	}
	if err := os.WriteFile(decodedPath, data, 0o600); err != nil {
		return fmt.Errorf("write decoded %s: %w", searchMZLZ4File, err)
	}

	if err := runMozLZ4(ctx, false, decodedPath, path); err != nil {
		return fmt.Errorf("compress %s: %w", searchMZLZ4File, err)
	}
	return nil
}

func runMozLZ4(ctx context.Context, decompress bool, inputPath, outputPath string) error {
	binary := os.Getenv("MOZLZ4_BIN")
	if binary == "" {
		binary = defaultMozLZ4Binary
	}

	mode := "-z"
	if decompress {
		mode = "-x"
	}

	cmd := exec.CommandContext(ctx, binary, mode, inputPath, outputPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s %s: %w: %s", binary, mode, inputPath, err, strings.TrimSpace(string(output)))
	}
	return nil
}
