package converter

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/awsms/chromium2firefox/internal/progress"
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
	return newReporter(targetPaths, sourcePaths)
}

func newChromiumProfileReporter(chromiumProfileDir string, options Options, sourcePaths ...string) (*progress.Reporter, error) {
	var targetPaths []string
	var err error

	if options.History {
		var path string
		path, err = discoverRequiredChromiumFile(chromiumProfileDir, "History")
		if err != nil {
			return nil, err
		}
		targetPaths = append(targetPaths, path)
	}
	if options.Favicons {
		path, err := discoverOptionalChromiumFile(chromiumProfileDir, "Favicons")
		if err != nil {
			return nil, err
		}
		if path != "" {
			targetPaths = append(targetPaths, path)
		}
	}
	if options.Cookies {
		path, err := discoverOptionalChromiumFile(chromiumProfileDir, "Cookies")
		if err != nil {
			return nil, err
		}
		if path != "" {
			targetPaths = append(targetPaths, path)
		}
	}
	if options.Search {
		path, err := discoverOptionalChromiumFile(chromiumProfileDir, "Web Data")
		if err != nil {
			return nil, err
		}
		if path != "" {
			targetPaths = append(targetPaths, path)
		}
	}
	if options.Extensions {
		// Include Preferences for merging and Extension directories
		path, err := discoverOptionalChromiumFile(chromiumProfileDir, "Preferences")
		if err == nil && path != "" {
			targetPaths = append(targetPaths, path)
		}
		extDirs := []string{"Extensions", "Local Extension Settings", "Sync Extension Settings", "Extension Rules", "Extension State"}
		for _, dir := range extDirs {
			path, err := discoverOptionalProfileDir(chromiumProfileDir, dir)
			if err == nil && path != "" {
				targetPaths = append(targetPaths, path)
			}
		}
	}
	return newReporter(targetPaths, sourcePaths)
}

func newReporter(targetPaths, sourcePaths []string) (*progress.Reporter, error) {
	var total int64
	for _, path := range targetPaths {
		size, err := entrySize(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		total += size
	}
	for _, path := range sourcePaths {
		size, err := entrySize(path)
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

func entrySize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}

	var size int64
	err = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
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

func discoverOptionalProfileDir(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", nil
	}
	return path, nil
}


func discoverRequiredProfileFile(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("profile %s is missing %s", profileDir, name)
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

func discoverRequiredChromiumFile(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, nil
	}

	return "", fmt.Errorf("chromium profile %s is missing %s", profileDir, name)
}

func discoverOptionalChromiumFile(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, nil
	}

	return "", nil
}
