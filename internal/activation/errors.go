package activation

import "errors"

type RetryableError struct {
	err error
}

func NewRetryableError(err error) error {
	return RetryableError{err: err}
}

func (e RetryableError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e RetryableError) Unwrap() error {
	return e.err
}

func IsRetryable(err error) bool {
	var retryable RetryableError
	return errors.As(err, &retryable)
}
