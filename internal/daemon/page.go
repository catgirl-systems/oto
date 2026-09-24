package daemon

import (
	"encoding/json"
	"errors"
	"iter"
)

// errPageItemTooLarge reports a page item whose JSON encoding alone exceeds
// the page budget; no smaller page can make progress past it.
var errPageItemTooLarge = errors.New("community: item exceeds page budget")

// takePage collects items until the count limit or the measured JSON byte
// budget is reached. The budget counts one separator byte per item, mirroring
// the JSON array that wraps the page. It returns more=true when an item was
// seen but left unconsumed; callers derive their cursor from the last item
// kept. A first item that alone exceeds the budget is an error, never an
// empty page with a cursor (which used to index [-1] or loop forever).
func takePage[T any](items iter.Seq[T], limit, budget int) (page []T, more bool, err error) {
	page = make([]T, 0, min(max(limit, 0), 256))
	used := 0
	for v := range items {
		encoded, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return nil, false, marshalErr
		}
		if (limit > 0 && len(page) == limit) || used+len(encoded)+1 > budget {
			if len(page) == 0 {
				return nil, false, errPageItemTooLarge
			}
			return page, true, nil
		}
		used += len(encoded) + 1
		page = append(page, v)
	}
	return page, false, nil
}
