package daemon

import (
	"errors"
	"testing"
)

func pageSeq(values ...int) func(func(int) bool) {
	return func(yield func(int) bool) {
		for _, v := range values {
			if !yield(v) {
				return
			}
		}
	}
}

func TestTakePage(t *testing.T) {
	page, more, err := takePage(pageSeq(1, 2, 3), 2, 1<<20)
	failIf(t, err != nil || len(page) != 2 || !more || page[0] != 1 || page[1] != 2, "limit cut", page, more, err)
	page, more, err = takePage(pageSeq(1, 2, 3), 10, 1<<20)
	failIf(t, err != nil || len(page) != 3 || more, "all consumed", page, more, err)
	page, more, err = takePage(pageSeq(), 10, 1<<20)
	failIf(t, err != nil || more || page == nil || len(page) != 0, "empty page must be non-nil", page, more, err)
	// Single-digit ints marshal to one byte; the budget counts one separator.
	page, more, err = takePage(pageSeq(1, 2, 3), 10, 2)
	failIf(t, err != nil || len(page) != 1 || !more, "budget cut", page, more, err)
	_, _, err = takePage(pageSeq(1), 10, 1)
	failIf(t, !errors.Is(err, errPageItemTooLarge), "first oversized item must error", err)
}
