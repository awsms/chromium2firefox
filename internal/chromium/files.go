package chromium

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/awsms/chromium2firefox/internal/progress"
)

func ensureRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

func backupFile(path string, reporter progress.Sink) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}
	if reporter != nil {
		reporter.StartStage("backing up", path, info.Size())
	}

	var dst *os.File
	for attempt := 0; ; attempt++ {
		suffix := time.Now().UTC().Format("20060102T150405.000000000Z")
		if attempt > 0 {
			suffix = fmt.Sprintf("%s.%d", suffix, attempt)
		}
		backupPath := fmt.Sprintf("%s.chromium2firefox.%s.bak", path, suffix)
		dst, err = os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
	}
	defer dst.Close()

	if _, err := io.Copy(dst, progress.NewReader(src, reporter)); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	if reporter != nil {
		reporter.FinishStage("backing up", path, info.Size())
	}
	return nil
}

func CopyDirectory(src, dst string, reporter progress.Sink) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}

	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := CopyDirectory(srcPath, dstPath, reporter); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath, reporter); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string, reporter progress.Sink) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func MergePreferences(ctx context.Context, sourcePath, targetPath string, reporter progress.Sink) error {
	if err := backupFile(targetPath, reporter); err != nil {
		return fmt.Errorf("backup target preferences: %w", err)
	}

	sourceData, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read source preferences: %w", err)
	}

	targetData, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("read target preferences: %w", err)
	}

	var sourceMap, targetMap map[string]any
	if err := json.Unmarshal(sourceData, &sourceMap); err != nil {
		return fmt.Errorf("unmarshal source preferences: %w", err)
	}
	if err := json.Unmarshal(targetData, &targetMap); err != nil {
		return fmt.Errorf("unmarshal target preferences: %w", err)
	}

	// Merge "extensions" section
	if sourceExt, ok := sourceMap["extensions"].(map[string]any); ok {
		targetExt, ok := targetMap["extensions"].(map[string]any)
		if !ok {
			targetExt = make(map[string]any)
			targetMap["extensions"] = targetExt
		}

		// Merge "settings"
		if sourceSettings, ok := sourceExt["settings"].(map[string]any); ok {
			targetSettings, ok := targetExt["settings"].(map[string]any)
			if !ok {
				targetSettings = make(map[string]any)
				targetExt["settings"] = targetSettings
			}
			for k, v := range sourceSettings {
				targetSettings[k] = v
			}
		}
	}

	newData, err := json.MarshalIndent(targetMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged preferences: %w", err)
	}

	return os.WriteFile(targetPath, newData, 0644)
}

