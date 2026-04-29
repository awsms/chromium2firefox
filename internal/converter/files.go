package converter

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/awsms/chromium2firefox/internal/progress"
)

func newProfileReporter(firefoxProfileDir string, options Options, sourcePaths ...string) (*progress.Reporter, error) {
	var total int64
	targetFiles := []string{"places.sqlite", "favicons.sqlite", "cookies.sqlite", "search.json.mozlz4"}
	for _, name := range targetFiles {
		if path, _ := discoverOptionalProfileFile(firefoxProfileDir, name); path != "" {
			size, _ := entrySize(path)
			total += size
		}
	}
	for _, path := range sourcePaths {
		size, _ := entrySize(path)
		total += size * 2
	}
	return newReporter(total), nil
}

func newChromiumProfileReporter(chromiumProfileDir string, options Options, sourcePaths ...string) (*progress.Reporter, error) {
	var total int64

	// History, Favicons, Cookies, Web Data usually follow: read + backup + write = 2*source + target
	mergedFiles := []string{"History", "Favicons", "Cookies", "Web Data"}
	for _, name := range mergedFiles {
		srcPath := ""
		for _, p := range sourcePaths {
			if filepath.Base(p) == name {
				srcPath = p
				break
			}
		}
		if srcPath != "" {
			srcSize, _ := entrySize(srcPath)
			total += srcSize * 2
			if dstPath, _ := discoverOptionalChromiumFile(chromiumProfileDir, name); dstPath != "" {
				dstSize, _ := entrySize(dstPath)
				total += dstSize
			}
		}
	}

	if options.Extensions {
		// Preferences merge: source + 2*target
		if srcPath, _ := discoverOptionalChromiumFile(chromiumProfileDir, "Preferences"); srcPath != "" {
			srcSize, _ := entrySize(srcPath)
			total += srcSize
			if dstPath, _ := discoverOptionalChromiumFile(chromiumProfileDir, "Preferences"); dstPath != "" {
				dstSize, _ := entrySize(dstPath)
				total += dstSize * 2
			}
		}
		// Extension directories: direct copy = 1*source
		for _, dir := range chromiumExtensionDirectories() {
			srcPath := ""
			for _, p := range sourcePaths {
				if filepath.Base(p) == dir {
					srcPath = p
					break
				}
			}
			if srcPath != "" {
				srcSize, _ := entrySize(srcPath)
				total += srcSize
			}
		}
	}

	return newReporter(total), nil
}

func newReporter(total int64) *progress.Reporter {
	if total <= 0 {
		total = 1
	}
	return progress.New(os.Stderr, total)
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

func discoverOptionalProfileEntry(profileDir, name string) (string, error) {
	path := filepath.Join(profileDir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return "", nil
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
