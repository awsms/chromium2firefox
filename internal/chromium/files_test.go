package chromium

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergePreferencesMergesExtensionCommands(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	sourcePath := filepath.Join(tmpDir, "source-preferences.json")
	targetPath := filepath.Join(tmpDir, "target-preferences.json")

	sourcePrefs := map[string]any{
		"extensions": map[string]any{
			"commands": map[string]any{
				"linux:Ctrl+Shift+A": map[string]any{
					"command_name": "toggle_palette",
					"extension":    "source-ext",
					"global":       true,
				},
			},
			"global_shortcuts": map[string]any{
				"uuid": "source-uuid",
			},
			"settings": map[string]any{
				"source-ext": map[string]any{
					"manifest": map[string]any{
						"name": "Source Extension",
					},
				},
			},
		},
	}

	targetPrefs := map[string]any{
		"extensions": map[string]any{
			"commands": map[string]any{
				"linux:Ctrl+Shift+L": map[string]any{
					"command_name": "_execute_action",
					"extension":    "target-ext",
					"global":       false,
				},
			},
			"global_shortcuts": map[string]any{
				"uuid": "target-uuid",
			},
			"settings": map[string]any{
				"target-ext": map[string]any{
					"manifest": map[string]any{
						"name": "Target Extension",
					},
				},
			},
		},
	}

	writeJSONFile(t, sourcePath, sourcePrefs)
	writeJSONFile(t, targetPath, targetPrefs)

	if err := MergePreferences(ctx, sourcePath, targetPath, nil); err != nil {
		t.Fatalf("MergePreferences() error = %v", err)
	}

	var merged map[string]any
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", targetPath, err)
	}
	if err := json.Unmarshal(content, &merged); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	extensions := merged["extensions"].(map[string]any)
	commands := extensions["commands"].(map[string]any)
	if _, ok := commands["linux:Ctrl+Shift+A"]; !ok {
		t.Fatalf("expected source shortcut to be merged")
	}
	if _, ok := commands["linux:Ctrl+Shift+L"]; !ok {
		t.Fatalf("expected target shortcut to be preserved")
	}

	globalShortcuts := extensions["global_shortcuts"].(map[string]any)
	if globalShortcuts["uuid"] != "source-uuid" {
		t.Fatalf("expected source global_shortcuts uuid, got %v", globalShortcuts["uuid"])
	}

	settings := extensions["settings"].(map[string]any)
	if _, ok := settings["source-ext"]; !ok {
		t.Fatalf("expected source extension settings to be merged")
	}
	if _, ok := settings["target-ext"]; !ok {
		t.Fatalf("expected target extension settings to be preserved")
	}
}

func writeJSONFile(t *testing.T, path string, data map[string]any) {
	t.Helper()

	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
