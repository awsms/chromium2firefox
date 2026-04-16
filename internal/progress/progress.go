package progress

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	assumedBytesPerSecond         = 64 * 1024 * 1024
	assumedFinalizeBytesPerSecond = 512 * 1024
)

type Sink interface {
	StartStage(label, path string, size int64)
	Advance(delta int64)
	FinishStage(label, path string, size int64)
	Info(format string, args ...any)
}

type Reporter struct {
	mu           sync.Mutex
	out          io.Writer
	total        int64
	done         int64
	startedAt    time.Time
	stageSize    int64
	stageDone    int64
	stageDesc    string
	stagePath    string
	stageStarted time.Time
	lastPrint    time.Time
	lineActive   bool
}

func New(out io.Writer, total int64) *Reporter {
	return &Reporter{
		out:       out,
		total:     total,
		startedAt: time.Now(),
	}
}

func (r *Reporter) StartStage(label, path string, size int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.printStatusLocked(
		"[%3d%%] %s %s (%s, eta %s)",
		r.percentLocked(),
		label,
		filepath.Base(path),
		humanSize(size),
		r.stageEtaLocked(size, 0),
	)
	r.stageSize = size
	r.stageDone = 0
	r.stageDesc = fmt.Sprintf("%s %s", label, filepath.Base(path))
	r.stagePath = path
	r.stageStarted = time.Now()
	r.lastPrint = time.Now()
}

func (r *Reporter) Advance(delta int64) {
	if r == nil || delta <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.done += delta
	r.stageDone += delta
	if r.done > r.total {
		r.done = r.total
	}
	if r.stageDone > r.stageSize {
		r.stageDone = r.stageSize
	}

	now := time.Now()
	shouldPrint := now.Sub(r.lastPrint) >= 300*time.Millisecond
	if r.stageSize > 0 {
		shouldPrint = shouldPrint || (r.stageDone*100/r.stageSize) != ((r.stageDone-delta)*100/r.stageSize)
	}
	if !shouldPrint {
		return
	}

	stagePercent := 0
	if r.stageSize > 0 {
		stagePercent = int((r.stageDone * 100) / r.stageSize)
		if r.stageDone < r.stageSize && stagePercent >= 100 {
			stagePercent = 99
		}
	}
	r.printStatusLocked(
		"[%3d%%] %s (%d%% stage, eta %s)",
		r.percentLocked(),
		r.stageDesc,
		stagePercent,
		r.stageEtaLocked(r.stageSize, r.stageDone),
	)
	r.lastPrint = now
}

func (r *Reporter) FinishStage(label, path string, size int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stageSize == size && r.stageDone < size {
		r.done += size - r.stageDone
	}
	if r.done > r.total {
		r.done = r.total
	}
	r.stageDone = 0
	r.stageSize = 0
	r.stageDesc = ""
	r.stagePath = ""
	r.stageStarted = time.Time{}
	r.printFinalLocked(
		"[%3d%%] done %s %s",
		r.percentLocked(),
		label,
		filepath.Base(path),
	)
}

func (r *Reporter) Info(format string, args ...any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.printFinalLocked(format, args...)
}

func (r *Reporter) percentLocked() int {
	if r.total <= 0 {
		return 0
	}
	return int((r.done * 100) / r.total)
}

func (r *Reporter) stageEtaLocked(stageSize, stageDone int64) time.Duration {
	remaining := stageSize - stageDone
	if remaining < 0 {
		remaining = 0
	}
	elapsed := time.Since(r.stageStarted)
	if stageDone > 0 && elapsed > 0 {
		bytesPerSecond := float64(stageDone) / elapsed.Seconds()
		if bytesPerSecond > 0 {
			seconds := int64(float64(remaining) / bytesPerSecond)
			if remaining > 0 && seconds < 1 {
				seconds = 1
			}
			return time.Duration(seconds) * time.Second
		}
	}

	bytesPerSecond := int64(assumedBytesPerSecond)
	if strings.HasPrefix(r.stageDesc, "finalizing ") {
		bytesPerSecond = assumedFinalizeBytesPerSecond
	}

	seconds := int64(0)
	if remaining > 0 {
		seconds = (remaining + bytesPerSecond - 1) / bytesPerSecond
	}
	if remaining > 0 && elapsed > 0 {
		elapsedSeconds := int64(elapsed / time.Second)
		if elapsedSeconds > 0 && elapsedSeconds < seconds {
			seconds -= elapsedSeconds
		}
	}
	if remaining > 0 && seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}

func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func (r *Reporter) printStatusLocked(format string, args ...any) {
	fmt.Fprintf(r.out, "\r\033[2K"+format, args...)
	r.lineActive = true
}

func (r *Reporter) printFinalLocked(format string, args ...any) {
	if r.lineActive {
		fmt.Fprint(r.out, "\r\033[2K")
	}
	fmt.Fprintf(r.out, format+"\n", args...)
	r.lineActive = false
}
