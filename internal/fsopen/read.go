package fsopen

import (
	"errors"
	"io"
)

// ReadFile reads path using the same replacement-compatible handles as Open.
func ReadFile(path string) ([]byte, error) {
	file, err := Open(path)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(file)
	return body, errors.Join(readErr, file.Close())
}
