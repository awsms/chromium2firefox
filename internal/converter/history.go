package converter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"chromium2firefox/internal/chromium"
	"chromium2firefox/internal/firefox"
	"chromium2firefox/internal/progress"
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

	cookiesPath, err := discoverOptionalProfileFile(chromiumProfileDir, "Cookies")
	if err != nil {
		return err
	}

	webDataPath, err := discoverOptionalProfileFile(chromiumProfileDir, "Web Data")
	if err != nil {
		return err
	}

	reporter, err := newProfileReporter(firefoxProfileDir, historyPath, faviconPath, cookiesPath, webDataPath)
	if err != nil {
		return err
	}
	reporter.Info("starting import from %s into %s", chromiumProfileDir, firefoxProfileDir)

	historySize, _ := fileSize(historyPath)
	reporter.StartStage("reading", historyPath, historySize)
	dataset, err := chromium.ReadHistory(ctx, historyPath)
	if err != nil {
		return fmt.Errorf("read chromium history: %w", err)
	}
	reporter.FinishStage("reading", historyPath, historySize)

	if err := firefox.ImportHistory(ctx, firefoxProfileDir, dataset, historySize, reporter); err != nil {
		return fmt.Errorf("import into firefox places database: %w", err)
	}

	if faviconPath != "" {
		faviconSize, _ := fileSize(faviconPath)
		reporter.StartStage("reading", faviconPath, faviconSize)
		favicons, err := chromium.ReadFavicons(ctx, faviconPath)
		if err != nil {
			return fmt.Errorf("read chromium favicons: %w", err)
		}
		reporter.FinishStage("reading", faviconPath, faviconSize)
		if err := firefox.ImportFavicons(ctx, firefoxProfileDir, favicons, faviconSize, reporter); err != nil {
			return fmt.Errorf("import into firefox favicons database: %w", err)
		}
	}

	if cookiesPath != "" {
		cookiesSize, _ := fileSize(cookiesPath)
		reporter.StartStage("reading", cookiesPath, cookiesSize)
		cookies, err := chromium.ReadCookies(ctx, cookiesPath)
		if err != nil {
			return fmt.Errorf("read chromium cookies: %w", err)
		}
		reporter.FinishStage("reading", cookiesPath, cookiesSize)
		if err := firefox.ImportCookies(ctx, firefoxProfileDir, cookies, cookiesSize, reporter); err != nil {
			return fmt.Errorf("import into firefox cookies database: %w", err)
		}
	}

	if webDataPath != "" {
		webDataSize, _ := fileSize(webDataPath)
		reporter.StartStage("reading", webDataPath, webDataSize)
		engines, err := chromium.ReadWebData(ctx, webDataPath)
		if err != nil {
			return fmt.Errorf("read chromium web data: %w", err)
		}
		reporter.FinishStage("reading", webDataPath, webDataSize)
		if err := firefox.ImportSearchEngines(ctx, firefoxProfileDir, engines, webDataSize, reporter); err != nil {
			return fmt.Errorf("import into firefox search settings: %w", err)
		}
	}

	reporter.Info("[100%%] import completed")
	return nil
}

func newProfileReporter(firefoxProfileDir string, sourcePaths ...string) (*progress.Reporter, error) {
	targetPaths := []string{
		filepath.Join(firefoxProfileDir, "places.sqlite"),
		filepath.Join(firefoxProfileDir, "favicons.sqlite"),
		filepath.Join(firefoxProfileDir, "cookies.sqlite"),
		filepath.Join(firefoxProfileDir, "search.json.mozlz4"),
	}

	var total int64
	for _, path := range targetPaths {
		size, err := fileSize(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		total += size
	}
	for _, path := range sourcePaths {
		size, err := fileSize(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		total += size * 2
	}
	if total == 0 {
		total = 1
	}
	return progress.New(os.Stderr, total), nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%s is a directory", path)
	}
	return info.Size(), nil
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
