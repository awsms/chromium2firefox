package converter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/awsms/chromium2firefox/internal/chromium"
	"github.com/awsms/chromium2firefox/internal/firefox"
	"github.com/awsms/chromium2firefox/internal/progress"
)

type ProfileType int

const (
	UnknownProfile ProfileType = iota
	ChromiumProfile
	FirefoxProfile
)

func (p ProfileType) String() string {
	switch p {
	case ChromiumProfile:
		return "Chromium"
	case FirefoxProfile:
		return "Firefox"
	default:
		return "Unknown"
	}
}

func DetectProfileType(dir string) ProfileType {
	chromiumFiles := []string{"Cookies", "Favicons", "Bookmarks", "Web Data", "History", "Preferences", "Extensions"}
	firefoxFiles := []string{"places.sqlite", "favicons.sqlite", "cookies.sqlite", "search.json.mozlz4", "prefs.js"}

	chromiumCount := 0
	for _, name := range chromiumFiles {
		if path, _ := discoverOptionalProfileEntry(dir, name); path != "" {
			chromiumCount++
		}
	}

	firefoxCount := 0
	for _, name := range firefoxFiles {
		if path, _ := discoverOptionalProfileEntry(dir, name); path != "" {
			firefoxCount++
		}
	}

	if chromiumCount > firefoxCount {
		return ChromiumProfile
	}
	if firefoxCount > chromiumCount {
		return FirefoxProfile
	}
	return UnknownProfile
}

func ConvertProfile(ctx context.Context, sourceDir, targetDir string, options Options) error {
	sourceType := DetectProfileType(sourceDir)
	targetType := DetectProfileType(targetDir)

	if sourceType == UnknownProfile {
		return fmt.Errorf("could not detect source profile type in %s", sourceDir)
	}
	if targetType == UnknownProfile {
		return fmt.Errorf("could not detect target profile type in %s", targetDir)
	}

	switch {
	case sourceType == ChromiumProfile && targetType == FirefoxProfile:
		return ConvertChromiumToFirefox(ctx, sourceDir, targetDir, options)
	case sourceType == FirefoxProfile && targetType == ChromiumProfile:
		return ConvertFirefoxToChromium(ctx, sourceDir, targetDir, options)
	case sourceType == ChromiumProfile && targetType == ChromiumProfile:
		return ConvertChromiumToChromium(ctx, sourceDir, targetDir, options)
	case sourceType == FirefoxProfile && targetType == FirefoxProfile:
		return fmt.Errorf("Firefox to Firefox conversion is not implemented yet")
	default:
		return fmt.Errorf("unsupported conversion from %s to %s", sourceType, targetType)
	}
}

func ConvertChromiumToChromium(ctx context.Context, sourceProfileDir, targetProfileDir string, options Options) error {
	var sourcePaths []string
	if options.History {
		if p, _ := discoverOptionalProfileFile(sourceProfileDir, "History"); p != "" {
			sourcePaths = append(sourcePaths, p)
		}
	}
	if options.Favicons {
		if p, _ := discoverOptionalProfileFile(sourceProfileDir, "Favicons"); p != "" {
			sourcePaths = append(sourcePaths, p)
		}
	}
	if options.Cookies {
		if p, _ := discoverOptionalProfileFile(sourceProfileDir, "Cookies"); p != "" {
			sourcePaths = append(sourcePaths, p)
		}
	}
	if options.Search {
		if p, _ := discoverOptionalProfileFile(sourceProfileDir, "Web Data"); p != "" {
			sourcePaths = append(sourcePaths, p)
		}
	}
	if options.Extensions {
		if p, _ := discoverOptionalChromiumFile(sourceProfileDir, "Preferences"); p != "" {
			sourcePaths = append(sourcePaths, p)
		}
		extDirs := chromiumExtensionDirectories()
		for _, dir := range extDirs {
			if p, _ := discoverOptionalProfileDir(sourceProfileDir, dir); p != "" {
				sourcePaths = append(sourcePaths, p)
			}
		}
	}

	reporter, err := newChromiumProfileReporter(targetProfileDir, options, sourcePaths...)
	if err != nil {
		return err
	}

	if !options.Merge {
		reporter.Info("starting import from Chromium %s into Chromium %s (OVERWRITE)", sourceProfileDir, targetProfileDir)

		copyTask := func(option bool, name, label string) error {
			if !option {
				return nil
			}
			sourcePath, _ := discoverOptionalProfileFile(sourceProfileDir, name)
			if sourcePath == "" {
				return nil
			}
			targetPath := filepath.Join(targetProfileDir, name)

			size, _ := entrySize(sourcePath)
			reporter.StartStage("reading", sourcePath, size)
			reporter.FinishStage("reading", sourcePath, size)

			if err := chromium.CopyFileWithBackup(sourcePath, targetPath, reporter); err != nil {
				return fmt.Errorf("copy %s: %w", label, err)
			}
			return nil
		}

		if err := copyTask(options.History, "History", "history"); err != nil {
			return err
		}
		if err := copyTask(options.Favicons, "Favicons", "favicons"); err != nil {
			return err
		}
		if err := copyTask(options.Cookies, "Cookies", "cookies"); err != nil {
			return err
		}
		if err := copyTask(options.Search, "Web Data", "web data"); err != nil {
			return err
		}
	} else {
		// SLOW PATH: MERGE
		reporter.Info("starting import from Chromium %s into Chromium %s (MERGE)", sourceProfileDir, targetProfileDir)

		if options.History {
			historyPath, _ := discoverOptionalProfileFile(sourceProfileDir, "History")
			if historyPath != "" {
				historySize, _ := fileSize(historyPath)
				reporter.StartStage("reading", historyPath, historySize)
				dataset, err := chromium.ReadHistory(ctx, historyPath)
				if err != nil {
					return fmt.Errorf("read source chromium history: %w", err)
				}
				reporter.FinishStage("reading", historyPath, historySize)

				targetHistory, err := discoverRequiredChromiumFile(targetProfileDir, "History")
				if err != nil {
					return err
				}
				if err := chromium.ImportHistory(ctx, targetHistory, dataset, historySize, reporter); err != nil {
					return fmt.Errorf("import into target chromium history: %w", err)
				}
			}
		}

		if options.Favicons {
			faviconPath, _ := discoverOptionalProfileFile(sourceProfileDir, "Favicons")
			if faviconPath != "" {
				faviconSize, _ := fileSize(faviconPath)
				reporter.StartStage("reading", faviconPath, faviconSize)
				favicons, err := chromium.ReadFavicons(ctx, faviconPath)
				if err != nil {
					return fmt.Errorf("read source chromium favicons: %w", err)
				}
				reporter.FinishStage("reading", faviconPath, faviconSize)

				targetFavicons, err := discoverOptionalChromiumFile(targetProfileDir, "Favicons")
				if err != nil {
					return err
				}
				if targetFavicons != "" {
					if err := chromium.ImportFavicons(ctx, targetFavicons, favicons, faviconSize, reporter); err != nil {
						return fmt.Errorf("import into target chromium favicons: %w", err)
					}
				}
			}
		}

		if options.Cookies {
			cookiesPath, _ := discoverOptionalProfileFile(sourceProfileDir, "Cookies")
			if cookiesPath != "" {
				cookiesSize, _ := fileSize(cookiesPath)
				reporter.StartStage("reading", cookiesPath, cookiesSize)
				cookies, err := chromium.ReadCookies(ctx, cookiesPath)
				if err != nil {
					return fmt.Errorf("read source chromium cookies: %w", err)
				}
				reporter.FinishStage("reading", cookiesPath, cookiesSize)

				targetCookies, err := discoverOptionalChromiumFile(targetProfileDir, "Cookies")
				if err != nil {
					return err
				}
				if targetCookies != "" {
					if err := chromium.ImportCookies(ctx, targetCookies, cookies, cookiesSize, reporter); err != nil {
						return fmt.Errorf("import into target chromium cookies: %w", err)
					}
				}
			}
		}

		if options.Search {
			webDataPath, _ := discoverOptionalProfileFile(sourceProfileDir, "Web Data")
			if webDataPath != "" {
				webDataSize, _ := fileSize(webDataPath)
				reporter.StartStage("reading", webDataPath, webDataSize)
				engines, err := chromium.ReadWebData(ctx, webDataPath)
				if err != nil {
					return fmt.Errorf("read source chromium web data: %w", err)
				}
				reporter.FinishStage("reading", webDataPath, webDataSize)

				targetWebData, err := discoverOptionalChromiumFile(targetProfileDir, "Web Data")
				if err != nil {
					return err
				}
				if targetWebData != "" {
					if err := chromium.ImportWebData(ctx, targetWebData, engines, webDataSize, reporter); err != nil {
						return fmt.Errorf("import into target chromium web data: %w", err)
					}
				}
			}
		}
	}

	if options.Extensions {
		sourcePrefs, _ := discoverOptionalChromiumFile(sourceProfileDir, "Preferences")
		targetPrefs, _ := discoverOptionalChromiumFile(targetProfileDir, "Preferences")
		if sourcePrefs != "" && targetPrefs != "" {
			if err := chromium.MergePreferences(ctx, sourcePrefs, targetPrefs, reporter); err != nil {
				return fmt.Errorf("merge preferences: %w", err)
			}
		}

		for _, dirName := range chromiumExtensionDirectories() {
			sourceDir, _ := discoverOptionalProfileDir(sourceProfileDir, dirName)
			if sourceDir != "" {
				targetDir := filepath.Join(targetProfileDir, dirName)
				size, _ := entrySize(sourceDir)
				if reporter != nil {
					reporter.StartStage("importing", sourceDir, size)
				}
				if err := chromium.CopyDirectory(sourceDir, targetDir, reporter); err != nil {
					return fmt.Errorf("copy extension directory %s: %w", dirName, err)
				}
				if reporter != nil {
					reporter.FinishStage("importing", sourceDir, size)
				}
			}
		}
		if sourcePrefs != "" {
			if err := copyExtensionIndexedDB(sourceProfileDir, targetProfileDir, sourcePrefs, reporter); err != nil {
				return fmt.Errorf("copy extension indexeddb: %w", err)
			}
		}
	}

	reporter.Info("[100%%] import completed")
	return nil
}

func copyExtensionIndexedDB(sourceProfileDir, targetProfileDir, sourcePrefs string, reporter *progress.Reporter) error {
	extensionIDs, err := chromium.ExtensionIDsFromPreferences(sourcePrefs)
	if err != nil {
		return err
	}
	if len(extensionIDs) == 0 {
		return nil
	}

	sourceIndexedDBDir, err := discoverOptionalProfileDir(sourceProfileDir, "IndexedDB")
	if err != nil || sourceIndexedDBDir == "" {
		return err
	}

	targetIndexedDBDir := filepath.Join(targetProfileDir, "IndexedDB")
	if err := os.MkdirAll(targetIndexedDBDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target indexeddb dir: %w", err)
	}

	entries, err := os.ReadDir(sourceIndexedDBDir)
	if err != nil {
		return fmt.Errorf("read source indexeddb dir: %w", err)
	}

	for _, entry := range entries {
		if !isExtensionIndexedDBEntry(entry.Name(), extensionIDs) {
			continue
		}

		sourcePath := filepath.Join(sourceIndexedDBDir, entry.Name())
		targetPath := filepath.Join(targetIndexedDBDir, entry.Name())
		size, _ := entrySize(sourcePath)
		if reporter != nil {
			reporter.StartStage("importing", sourcePath, size)
		}
		if err := chromium.CopyPathReplacing(sourcePath, targetPath, reporter); err != nil {
			return fmt.Errorf("copy indexeddb entry %s: %w", entry.Name(), err)
		}
		if reporter != nil {
			reporter.FinishStage("importing", sourcePath, size)
		}
	}

	return nil
}

func isExtensionIndexedDBEntry(name string, extensionIDs []string) bool {
	if !strings.HasPrefix(name, "chrome-extension_") {
		return false
	}
	for _, id := range extensionIDs {
		if strings.HasPrefix(name, "chrome-extension_"+id+"_") {
			return true
		}
	}
	return false
}

func chromiumExtensionDirectories() []string {
	return []string{
		"Extensions",
		"Local Extension Settings",
		"Sync Extension Settings",
		"Managed Extension Settings",
		"Extension Rules",
		"Extension State",
		"Extension Scripts",
	}
}

func ConvertChromiumToFirefox(ctx context.Context, chromiumProfileDir, firefoxProfileDir string, options Options) error {
	var (
		historyPath string
		err         error
	)
	if options.History {
		historyPath, err = discoverRequiredProfileFile(chromiumProfileDir, "History")
		if err != nil {
			return err
		}
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

	reporter, err := newProfileReporter(firefoxProfileDir, options, historyPath, faviconPath, cookiesPath, webDataPath)
	if err != nil {
		return err
	}
	reporter.Info("starting import from %s into %s", chromiumProfileDir, firefoxProfileDir)

	if options.History {
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
	}

	if options.Favicons && faviconPath != "" {
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

	if options.Cookies && cookiesPath != "" {
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

	if options.Search && webDataPath != "" {
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

func ConvertFirefoxToChromium(ctx context.Context, firefoxProfileDir, chromiumProfileDir string, options Options) error {
	var (
		placesPath   string
		faviconsPath string
		cookiesPath  string
		searchPath   string
		err          error
	)

	if options.History {
		placesPath, err = discoverRequiredProfileFile(firefoxProfileDir, "places.sqlite")
		if err != nil {
			return err
		}
	}

	if options.Favicons {
		faviconsPath, err = discoverOptionalProfileFile(firefoxProfileDir, "favicons.sqlite")
		if err != nil {
			return err
		}
	}

	if options.Cookies {
		cookiesPath, err = discoverOptionalProfileFile(firefoxProfileDir, "cookies.sqlite")
		if err != nil {
			return err
		}
	}

	if options.Search {
		searchPath, err = discoverOptionalProfileFile(firefoxProfileDir, "search.json.mozlz4")
		if err != nil {
			return err
		}
	}

	reporter, err := newChromiumProfileReporter(chromiumProfileDir, options, placesPath, faviconsPath, cookiesPath, searchPath)
	if err != nil {
		return err
	}

	reporter.Info("starting import from %s into %s", firefoxProfileDir, chromiumProfileDir)

	if options.History {
		placesSize, _ := fileSize(placesPath)
		reporter.StartStage("reading", placesPath, placesSize)
		dataset, err := firefox.ReadHistory(ctx, placesPath)
		if err != nil {
			return fmt.Errorf("read firefox history: %w", err)
		}
		reporter.FinishStage("reading", placesPath, placesSize)
		targetHistory, err := discoverRequiredChromiumFile(chromiumProfileDir, "History")
		if err != nil {
			return err
		}
		if err := chromium.ImportHistory(ctx, targetHistory, dataset, placesSize, reporter); err != nil {
			return fmt.Errorf("import into chromium history: %w", err)
		}
	}

	if options.Favicons && faviconsPath != "" {
		faviconsSize, _ := fileSize(faviconsPath)
		reporter.StartStage("reading", faviconsPath, faviconsSize)
		favicons, err := firefox.ReadFavicons(ctx, faviconsPath)
		if err != nil {
			return fmt.Errorf("read firefox favicons: %w", err)
		}
		reporter.FinishStage("reading", faviconsPath, faviconsSize)
		targetFavicons, err := discoverOptionalChromiumFile(chromiumProfileDir, "Favicons")
		if err != nil {
			return err
		}
		if targetFavicons != "" {
			if err := chromium.ImportFavicons(ctx, targetFavicons, favicons, faviconsSize, reporter); err != nil {
				return fmt.Errorf("import into chromium favicons: %w", err)
			}
		}
	}

	if options.Cookies && cookiesPath != "" {
		cookiesSize, _ := fileSize(cookiesPath)
		reporter.StartStage("reading", cookiesPath, cookiesSize)
		cookies, err := firefox.ReadCookies(ctx, cookiesPath)
		if err != nil {
			return fmt.Errorf("read firefox cookies: %w", err)
		}
		reporter.FinishStage("reading", cookiesPath, cookiesSize)
		targetCookies, err := discoverOptionalChromiumFile(chromiumProfileDir, "Cookies")
		if err != nil {
			return err
		}
		if targetCookies != "" {
			if err := chromium.ImportCookies(ctx, targetCookies, cookies, cookiesSize, reporter); err != nil {
				return fmt.Errorf("import into chromium cookies: %w", err)
			}
		}
	}

	if options.Search && searchPath != "" {
		searchSize, _ := fileSize(searchPath)
		reporter.StartStage("reading", searchPath, searchSize)
		engines, err := firefox.ReadSearchEngines(ctx, searchPath)
		if err != nil {
			return fmt.Errorf("read firefox search engines: %w", err)
		}
		reporter.FinishStage("reading", searchPath, searchSize)
		targetWebData, err := discoverOptionalChromiumFile(chromiumProfileDir, "Web Data")
		if err != nil {
			return err
		}
		if targetWebData != "" {
			if err := chromium.ImportWebData(ctx, targetWebData, engines, searchSize, reporter); err != nil {
				return fmt.Errorf("import into chromium web data: %w", err)
			}
		}
	}

	reporter.Info("[100%%] import completed")
	return nil
}
