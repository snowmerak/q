package closeutil

import (
	"errors"
	"io"
	"reflect"
)

// Deduplicate filters nil closers and removes duplicate comparable closer
// values while preserving first-seen order.
func Deduplicate(closers []io.Closer) []io.Closer {
	if len(closers) == 0 {
		return nil
	}
	seen := make(map[any]struct{}, len(closers))
	out := make([]io.Closer, 0, len(closers))
	for _, c := range closers {
		if c == nil {
			continue
		}
		typ := reflect.TypeOf(c)
		if typ.Comparable() {
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CloseAll closes each deduplicated closer in order and joins all errors.
func CloseAll(closers []io.Closer) error {
	var err error
	for _, c := range Deduplicate(closers) {
		err = errors.Join(err, c.Close())
	}
	return err
}
