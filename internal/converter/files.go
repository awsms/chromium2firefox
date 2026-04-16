package converter

import (
	"fmt"
	"os"
	"path/filepath"

	"chromium2firefox/internal/progress"
)

func newProfileReporter(firefoxProfileDir string, options Options, sourcePaths ...string) (*progress.Reporter, error) {
	var targetPaths []string
	if options.History {
		targetPaths = append(targetPaths, filepath.Join(firefoxProfileDir, "places.sqlite"))
	}
	if options.Favicons {
		targetPaths = append(targetPaths, filepath.Join(firefoxProfileDir, "favicons.sqlite"))
	}
	if options.Cookies {
		targetPaths = append(targetPaths, filepath.Join(firefoxProfileDir, "cookies.sqlite"))
	}
	if options.Search {
		targetPaths = append(targetPaths, filepath.Join(firefoxProfileDir, "search.json.mozlz4"))
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
