package repository

import "errors"

// ErrNotFound is returned by GetByID/Delete when no row matches the id.
// Backends must wrap or return this so callers can map it to HTTP 404 without
// depending on driver-specific error types.
var ErrNotFound = errors.New("record not found")

// IsNotFound reports whether err is (or wraps) ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
