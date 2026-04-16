package converter

import (
	"context"
	"fmt"

	"chromium2firefox/internal/chromium"
	"chromium2firefox/internal/firefox"
)

func ConvertProfile(ctx context.Context, chromiumProfileDir, firefoxProfileDir string, options Options) error {
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
