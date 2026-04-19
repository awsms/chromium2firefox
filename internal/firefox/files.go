package firefox

import (
	"fmt"
	"io"
	"os"
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

	backupPath := fmt.Sprintf("%s.chromium2firefox.%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	dst, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
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
