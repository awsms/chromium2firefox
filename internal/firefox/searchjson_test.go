package firefox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"chromium2firefox/internal/chromium"
)

func TestImportSearchEnginesMergesCustomChromiumEngines(t *testing.T) {
	profileDir := t.TempDir()
	settingsPath := filepath.Join(profileDir, searchMZLZ4File)
	writeFile(t, settingsPath, `{"version":13,"engines":[{"id":"google","_name":"Google","_isConfigEngine":true,"_metaData":{}}],"metaData":{"locale":"en-US","region":"FR","appDefaultEngineId":"google","useSavedOrder":false}}`)

	mozlz4Path := filepath.Join(t.TempDir(), "mozlz4")
	writeFile(t, mozlz4Path, "#!/bin/sh\nset -eu\nmode=\"$1\"\nin=\"$2\"\nout=\"$3\"\ncp \"$in\" \"$out\"\n")
	if err := os.Chmod(mozlz4Path, 0o755); err != nil {
		t.Fatalf("Chmod(%q): %v", mozlz4Path, err)
	}
	t.Setenv("MOZLZ4_BIN", mozlz4Path)

	engines := []chromium.Engine{
		{
			ID:         1,
			Name:       "Discogs",
			Keyword:    "dis",
			SearchURL:  "https://www.discogs.com/search/?type=all&q={searchTerms}",
			FaviconURL: "https://www.discogs.com/favicon.ico",
			IsActive:   true,
		},
		{
			ID:                  2,
			Name:                "Post Engine",
			Keyword:             "pe",
			SearchURL:           "https://example.com/search",
			IsActive:            true,
			SearchURLPostParams: "q={searchTerms}",
		},
		{
			ID:        3,
			Name:      "Google",
			Keyword:   "g",
			SearchURL: "https://www.google.com/search?q={searchTerms}",
			IsActive:  true,
		},
	}

	if err := ImportSearchEngines(context.Background(), profileDir, engines, 1024, nil); err != nil {
		t.Fatalf("ImportSearchEngines() error = %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", settingsPath, err)
	}

	var settings struct {
		Version  int               `json:"version"`
		Engines  []persistedEngine `json:"engines"`
		MetaData map[string]any    `json:"metaData"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Unmarshal(settings): %v", err)
	}

	if len(settings.Engines) != 2 {
		t.Fatalf("engine count = %d, want 2", len(settings.Engines))
	}

	var imported *persistedEngine
	for i := range settings.Engines {
		if settings.Engines[i].ID == "chromium-1" {
			imported = &settings.Engines[i]
			break
		}
	}
	if imported == nil {
		t.Fatal("imported engine chromium-1 not found")
	}
	if imported.Name != "Discogs" {
		t.Fatalf("imported name = %q, want Discogs", imported.Name)
	}
	if imported.LoadPath != firefoxSearchLoadPath {
		t.Fatalf("load path = %q, want %q", imported.LoadPath, firefoxSearchLoadPath)
	}
	if len(imported.DefinedAliases) != 1 || imported.DefinedAliases[0] != "dis" {
		t.Fatalf("aliases = %#v, want [\"dis\"]", imported.DefinedAliases)
	}
	if got := imported.IconMap["16"]; got != "https://www.discogs.com/favicon.ico" {
		t.Fatalf("icon = %q, want discogs favicon", got)
	}
	if len(imported.URLs) != 1 {
		t.Fatalf("url count = %d, want 1", len(imported.URLs))
	}
	if imported.URLs[0].Template != "https://www.discogs.com/search/?type=all&q={searchTerms}" {
		t.Fatalf("template = %q", imported.URLs[0].Template)
	}

	backups, err := filepath.Glob(settingsPath + ".chromium2firefox.*.bak")
	if err != nil {
		t.Fatalf("Glob(backups): %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1", len(backups))
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
