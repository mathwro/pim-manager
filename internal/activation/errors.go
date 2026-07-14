package activation

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/graph"
)

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

func WrapRetryable(err error) error {
	if err == nil {
		return nil
	}
	if IsRetryable(err) {
		return err
	}
	if !isTransientActivationError(err) {
		return err
	}
	return NewRetryableError(err)
}

func isTransientActivationError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var graphErr graph.ResponseError
	if errors.As(err, &graphErr) {
		return isTransientStatus(graphErr.StatusCode)
	}
	var armErr arm.ResponseError
	if errors.As(err, &armErr) {
		return isTransientStatus(armErr.StatusCode)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	return false
}

func isTransientStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}
