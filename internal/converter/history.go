package converter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"chromium2firefox/internal/history/chromium"
	"chromium2firefox/internal/history/firefox"
)

func ConvertHistory(ctx context.Context, chromiumHistoryPath, chromiumFaviconsPath, firefoxProfileDir string) error {
	dataset, err := chromium.ReadHistory(ctx, chromiumHistoryPath)
	if err != nil {
		return fmt.Errorf("read chromium history: %w", err)
	}

	if err := firefox.ImportHistory(ctx, firefoxProfileDir, dataset); err != nil {
		return fmt.Errorf("import into firefox places database: %w", err)
	}

	faviconPath := chromiumFaviconsPath
	if faviconPath == "" {
		candidate := filepath.Join(filepath.Dir(chromiumHistoryPath), "Favicons")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() > 0 {
			faviconPath = candidate
		}
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

	return nil
}
