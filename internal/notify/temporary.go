package notify

import "errors"

// temporaryError marks a delivery failure that may succeed if tried again: a
// network or name-resolution failure, a rate limit, a server-side error. A
// rejected request (a wrong webhook, a bad token) is not one.
type temporaryError struct{ err error }

func (e temporaryError) Error() string { return e.err.Error() }
func (e temporaryError) Unwrap() error { return e.err }

// Temporary marks err as worth retrying; nil stays nil.
func Temporary(err error) error {
	if err == nil {
		return nil
	}
	return temporaryError{err: err}
}

// IsTemporary reports whether a delivery failure may succeed if tried again.
func IsTemporary(err error) bool {
	var t temporaryError
	return errors.As(err, &t)
}
