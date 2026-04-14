package converter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"chromium2firefox/internal/history/chromium"
	"chromium2firefox/internal/history/firefox"
	chromiumsearch "chromium2firefox/internal/search/chromium"
	firefoxsearch "chromium2firefox/internal/search/firefox"
)

func ConvertProfile(ctx context.Context, chromiumProfileDir, firefoxProfileDir string) error {
	historyPath, err := discoverRequiredProfileFile(chromiumProfileDir, "History")
	if err != nil {
		return err
	}

	faviconPath, err := discoverOptionalProfileFile(chromiumProfileDir, "Favicons")
	if err != nil {
		return err
	}

	webDataPath, err := discoverOptionalProfileFile(chromiumProfileDir, "Web Data")
	if err != nil {
		return err
	}

	dataset, err := chromium.ReadHistory(ctx, historyPath)
	if err != nil {
		return fmt.Errorf("read chromium history: %w", err)
	}

	if err := firefox.ImportHistory(ctx, firefoxProfileDir, dataset); err != nil {
		return fmt.Errorf("import into firefox places database: %w", err)
	}

	if faviconPath != "" {
		favicons, err := chromium.ReadFavicons(ctx, faviconPath)
		if err != nil {
			return fmt.Errorf("read chromium favicons: %w", err)
		}
		if err := firefox.ImportFavicons(ctx, firefoxProfileDir, favicons); err != nil {
			return fmt.Errorf("import into firefox favicons database: %w", err)
		}
	}

	if webDataPath != "" {
		engines, err := chromiumsearch.ReadWebData(ctx, webDataPath)
		if err != nil {
			return fmt.Errorf("read chromium web data: %w", err)
		}
		if err := firefoxsearch.ImportSearchEngines(ctx, firefoxProfileDir, engines); err != nil {
			return fmt.Errorf("import into firefox search settings: %w", err)
		}
	}

	return nil
}

func discoverRequiredProfileFile(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("chromium profile %s is missing %s", profileDir, name)
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("%s is empty", path)
	}
	return path, nil
}

func discoverOptionalProfileFile(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return "", nil
	}
	return path, nil
}
