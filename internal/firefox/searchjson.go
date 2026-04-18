package firefox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"chromium2firefox/internal/chromium"
	"chromium2firefox/internal/progress"
	"github.com/google/uuid"
)

const (
	searchMZLZ4File             = "search.json.mozlz4"
	defaultMozLZ4Binary         = "mozlz4"
	firefoxSearchSuggestionType = "application/x-suggestions+json"
	firefoxSearchLoadPath       = "[user]"
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

func ImportSearchEngines(ctx context.Context, profileDir string, engines []chromium.Engine, sourceSize int64, reporter progress.Sink) error {
	candidates := filterImportableEngines(engines)
	if len(candidates) == 0 {
		return nil
	}

	settingsPath := filepath.Join(profileDir, searchMZLZ4File)
	if err := ensureRegularFile(settingsPath); err != nil {
		return err
	}
	if err := backupFile(settingsPath, reporter); err != nil {
		return fmt.Errorf("backup %s: %w", searchMZLZ4File, err)
	}
	importSize, finalizeSize := progress.SplitStageSize(sourceSize, 90)
	if reporter != nil {
		reporter.StartStage("importing", settingsPath, importSize)
	}

	settings, err := readSearchSettings(ctx, settingsPath)
	if err != nil {
		return err
	}

	existingIDs := make(map[string]struct{})
	existingNames := make(map[string]struct{})
	for idx, raw := range settings.Engines {
		var existing persistedEngine
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("decode existing engine: %w", err)
		}
		if existing.ID != "" {
			existingIDs[existing.ID] = struct{}{}
		}
		if existing.Name != "" {
			nameKey := strings.ToLower(existing.Name)
			existingNames[nameKey] = struct{}{}
			_ = idx
		}
	}

	progressor := progress.NewStageProgress(reporter, importSize, int64(len(candidates)))
	for _, engine := range candidates {
		persisted := toPersistedEngine(engine)
		nameKey := strings.ToLower(persisted.Name)
		if _, ok := existingIDs[persisted.ID]; ok {
			continue
		}
		if _, ok := existingNames[nameKey]; ok {
			continue
		}

		raw, err := json.Marshal(persisted)
		if err != nil {
			return fmt.Errorf("encode engine %s: %w", engine.Name, err)
		}
		settings.Engines = append(settings.Engines, raw)
		existingIDs[persisted.ID] = struct{}{}
		existingNames[nameKey] = struct{}{}
		progressor.Step(1)
	}

	if len(settings.Engines) == 0 {
		return fmt.Errorf("cannot write %s without any engines", searchMZLZ4File)
	}

	if reporter != nil {
		reporter.FinishStage("importing", settingsPath, importSize)
		reporter.StartStage("finalizing", settingsPath, finalizeSize)
	}
	if err := writeSearchSettings(ctx, settingsPath, settings); err != nil {
		return err
	}
	if reporter != nil {
		reporter.FinishStage("finalizing", settingsPath, finalizeSize)
	}
	return nil
}

func ReadSearchEngines(ctx context.Context, settingsPath string) ([]chromium.Engine, error) {
	settings, err := readSearchSettings(ctx, settingsPath)
	if err != nil {
		return nil, err
	}

	var out []chromium.Engine
	for _, raw := range settings.Engines {
		var engine persistedEngine
		if err := json.Unmarshal(raw, &engine); err != nil {
			return nil, fmt.Errorf("decode firefox engine: %w", err)
		}

		if len(engine.URLs) == 0 {
			continue
		}

		item := chromium.Engine{
			Name:     engine.Name,
			IsActive: true,
		}

		if alias, ok := engine.MetaData["alias"].(string); ok {
			item.Keyword = alias
		}

		for _, u := range engine.URLs {
			if u.Type == "" || u.Type == "text/html" {
				item.SearchURL = u.Template
			} else if u.Type == firefoxSearchSuggestionType {
				item.SuggestURL = u.Template
			}
		}

		if iconURL, ok := engine.IconMap["16"]; ok {
			item.FaviconURL = iconURL
		}

		if item.SearchURL != "" {
			out = append(out, item)
		}
	}

	return out, nil
}

func filterImportableEngines(engines []chromium.Engine) []chromium.Engine {
	var out []chromium.Engine
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

func toPersistedEngine(engine chromium.Engine) persistedEngine {
	out := persistedEngine{
		ID:       firefoxEngineID(),
		Name:     strings.TrimSpace(engine.Name),
		LoadPath: firefoxSearchLoadPath,
		MetaData: map[string]any{},
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
		out.MetaData["alias"] = engine.Keyword
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

func firefoxEngineID() string {
	return uuid.NewString()
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
