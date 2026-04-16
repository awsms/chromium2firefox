package firefox

import (
	"io"

	"chromium2firefox/internal/progress"
)

type progressReader struct {
	reader   io.Reader
	reporter progress.Sink
}

func newProgressReader(reader io.Reader, reporter progress.Sink) io.Reader {
	if reporter == nil {
		return reader
	}
	return &progressReader{reader: reader, reporter: reporter}
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.reporter.Advance(int64(n))
	}
	return n, err
}

type stageProgress struct {
	reporter progress.Sink
	size     int64
	total    int64
	done     int64
	reported int64
}

func newStageProgress(reporter progress.Sink, size, total int64) *stageProgress {
	return &stageProgress{
		reporter: reporter,
		size:     size,
		total:    total,
	}
}

func (p *stageProgress) Step(units int64) {
	if p == nil || p.reporter == nil || p.total <= 0 || units <= 0 {
		return
	}
	p.done += units
	target := (p.size * p.done) / p.total
	delta := target - p.reported
	if delta <= 0 {
		return
	}
	p.reported = target
	p.reporter.Advance(delta)
}

func splitStageSize(total int64, importPercent int64) (int64, int64) {
	if total <= 1 {
		return 1, 1
	}
	if importPercent <= 0 || importPercent >= 100 {
		importPercent = 90
	}
	importSize := (total * importPercent) / 100
	if importSize < 1 {
		importSize = 1
	}
	finalizeSize := total - importSize
	if finalizeSize < 1 {
		finalizeSize = 1
		importSize = total - 1
		if importSize < 1 {
			importSize = 1
		}
	}
	return importSize, finalizeSize
}

func splitFinalizeSizes(total int64, firstPercent int64, secondPercent int64) (int64, int64, int64) {
	if total <= 2 {
		return 1, 1, 1
	}
	if firstPercent < 0 {
		firstPercent = 0
	}
	if secondPercent < 0 {
		secondPercent = 0
	}
	if firstPercent+secondPercent >= 100 {
		firstPercent = 35
		secondPercent = 15
	}

	first := (total * firstPercent) / 100
	second := (total * secondPercent) / 100
	if first < 1 {
		first = 1
	}
	if second < 1 {
		second = 1
	}
	third := total - first - second
	if third < 1 {
		third = 1
		if second > 1 {
			second--
		} else if first > 1 {
			first--
		}
	}
	return first, second, third
}
