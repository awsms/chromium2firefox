package chromium

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func CopyFileWithBackup(src, dst string, reporter progress.Sink) error {
	if err := backupFile(dst, reporter); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("backup target file: %w", err)
	}
	return copyFile(src, dst, reporter)
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

func CopyPathReplacing(src, dst string, reporter progress.Sink) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dst); err != nil && !os.IsNotExist(err) {
		return err
	}

	if info.IsDir() {
		return CopyDirectory(src, dst, reporter)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(src, dst, reporter)
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

	if _, err = io.Copy(out, progress.NewReader(in, reporter)); err != nil {
		return err
	}
	return out.Sync()
}

func MergePreferences(ctx context.Context, sourcePath, targetPath string, reporter progress.Sink) error {
	if err := backupFile(targetPath, reporter); err != nil {
		return fmt.Errorf("backup target preferences: %w", err)
	}

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if reporter != nil {
		reporter.StartStage("reading", sourcePath, sourceInfo.Size())
	}
	sourceData, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read source preferences: %w", err)
	}
	if reporter != nil {
		reporter.Advance(sourceInfo.Size())
		reporter.FinishStage("reading", sourcePath, sourceInfo.Size())
	}

	if _, err := os.Stat(targetPath); err != nil {
		return err
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

	if sourceExt, ok := sourceMap["extensions"].(map[string]any); ok {
		mergeExtensionPreferences(targetMap, sourceExt)
	}

	newData, err := json.MarshalIndent(targetMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged preferences: %w", err)
	}

	if reporter != nil {
		reporter.StartStage("importing", targetPath, int64(len(newData)))
	}
	err = os.WriteFile(targetPath, newData, 0644)
	if err != nil {
		return err
	}
	if reporter != nil {
		reporter.Advance(int64(len(newData)))
		reporter.FinishStage("importing", targetPath, int64(len(newData)))
	}
	return nil
}

func mergeExtensionPreferences(targetMap map[string]any, sourceExt map[string]any) {
	targetExt, ok := targetMap["extensions"].(map[string]any)
	if !ok {
		targetExt = make(map[string]any)
		targetMap["extensions"] = targetExt
	}

	mergeNestedMap(targetExt, sourceExt, "settings")
	mergeNestedMap(targetExt, sourceExt, "commands")
	mergeNestedMap(targetExt, sourceExt, "global_shortcuts")
}

func mergeNestedMap(target, source map[string]any, key string) {
	sourceMap, ok := source[key].(map[string]any)
	if !ok {
		return
	}

	targetMap, ok := target[key].(map[string]any)
	if !ok {
		targetMap = make(map[string]any)
		target[key] = targetMap
	}

	for k, v := range sourceMap {
		targetMap[k] = v
	}
}

func ExtensionIDsFromPreferences(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read preferences: %w", err)
	}

	var prefs map[string]any
	if err := json.Unmarshal(data, &prefs); err != nil {
		return nil, fmt.Errorf("unmarshal preferences: %w", err)
	}

	extensions, ok := prefs["extensions"].(map[string]any)
	if !ok {
		return nil, nil
	}
	settings, ok := extensions["settings"].(map[string]any)
	if !ok {
		return nil, nil
	}

	ids := make([]string, 0, len(settings))
	for id := range settings {
		if strings.TrimSpace(id) == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}
