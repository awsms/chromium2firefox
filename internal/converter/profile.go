package converter

import (
	"context"
	"fmt"

	"github.com/awsms/chromium2firefox/internal/chromium"
	"github.com/awsms/chromium2firefox/internal/firefox"
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
	chromiumFiles := []string{"Cookies", "Favicons", "Bookmarks", "Web Data", "History"}
	firefoxFiles := []string{"places.sqlite", "favicons.sqlite", "cookies.sqlite", "search.json.mozlz4"}

	chromiumCount := 0
	for _, name := range chromiumFiles {
		if path, _ := discoverOptionalProfileFile(dir, name); path != "" {
			chromiumCount++
		}
	}

	firefoxCount := 0
	for _, name := range firefoxFiles {
		if path, _ := discoverOptionalProfileFile(dir, name); path != "" {
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
	var (
		historyPath string
		err         error
	)
	if options.History {
		historyPath, err = discoverRequiredProfileFile(sourceProfileDir, "History")
		if err != nil {
			return err
		}
	}

	faviconPath, err := discoverOptionalProfileFile(sourceProfileDir, "Favicons")
	if err != nil {
		return err
	}

	cookiesPath, err := discoverOptionalProfileFile(sourceProfileDir, "Cookies")
	if err != nil {
		return err
	}

	webDataPath, err := discoverOptionalProfileFile(sourceProfileDir, "Web Data")
	if err != nil {
		return err
	}

	reporter, err := newChromiumProfileReporter(targetProfileDir, options, historyPath, faviconPath, cookiesPath, webDataPath)
	if err != nil {
		return err
	}
	reporter.Info("starting import from Chromium %s into Chromium %s", sourceProfileDir, targetProfileDir)

	if options.History {
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

	if options.Favicons && faviconPath != "" {
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

	if options.Cookies && cookiesPath != "" {
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

	if options.Search && webDataPath != "" {
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

	reporter.Info("[100%%] import completed")
	return nil
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
