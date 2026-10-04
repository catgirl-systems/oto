package soulseek

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// tinyReader returns one byte per read, like a peer trickling data.
type tinyReader struct {
	data []byte
	fail error
}

func (r *tinyReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.fail != nil {
			return 0, r.fail
		}
		return 0, io.EOF
	}
	p[0], r.data = r.data[0], r.data[1:]
	return 1, nil
}

func TestCopyProgressIsThrottledButExact(t *testing.T) {
	var reports []Progress
	var out bytes.Buffer
	err := copyAtMost(context.Background(), &out, &tinyReader{data: bytes.Repeat([]byte("x"), 5000)}, 5000, 0, func(p Progress) { reports = append(reports, p) })
	must(t, err)
	failIfFmt(t, len(reports) == 0 || len(reports) > 3, "%d progress reports for 5000 one-byte reads", len(reports))
	failIfFmt(t, reports[len(reports)-1].Done != 5000, "final report %+v", reports[len(reports)-1])

	reports = nil
	broken := errors.New("peer vanished")
	err = copyAtMost(context.Background(), io.Discard, &tinyReader{data: bytes.Repeat([]byte("x"), 300), fail: broken}, 5000, 100, func(p Progress) { reports = append(reports, p) })
	failIf(t, !errors.Is(err, broken), "unexpected error", err)
	failIfFmt(t, len(reports) == 0 || reports[len(reports)-1].Done != 400, "failed copy under-reported resume point: %+v", reports)
}
