package activation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/mathwro/pim-manager/internal/arm"
	"github.com/mathwro/pim-manager/internal/graph"
)

func TestWrapRetryableClassifiesErrors(t *testing.T) {
	if got := WrapRetryable(nil); got != nil {
		t.Fatalf("expected nil error to stay nil, got %v", got)
	}

	already := NewRetryableError(errors.New("already retryable"))
	if got := WrapRetryable(already); got != already {
		t.Fatalf("expected already-retryable error to be returned unchanged, got %#v", got)
	}

	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "graph 408 request timeout",
			err:       graph.ResponseError{StatusCode: http.StatusRequestTimeout, Status: "408 Request Timeout"},
			retryable: true,
		},
		{
			name:      "graph 429 too many requests",
			err:       graph.ResponseError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"},
			retryable: true,
		},
		{
			name:      "graph 500 internal server error",
			err:       graph.ResponseError{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error"},
			retryable: true,
		},
		{
			name:      "arm 503 service unavailable wrapped",
			err:       fmt.Errorf("activate role: %w", arm.ResponseError{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable"}),
			retryable: true,
		},
		{
			name:      "context deadline exceeded",
			err:       fmt.Errorf("activate role: %w", context.DeadlineExceeded),
			retryable: true,
		},
		{
			name:      "network timeout",
			err:       fakeNetError{timeout: true},
			retryable: true,
		},
		{
			name:      "context canceled",
			err:       fmt.Errorf("activate role: %w", context.Canceled),
			retryable: false,
		},
		{
			name:      "graph 400 bad request",
			err:       graph.ResponseError{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
			retryable: false,
		},
		{
			name:      "arm 403 forbidden",
			err:       arm.ResponseError{StatusCode: http.StatusForbidden, Status: "403 Forbidden"},
			retryable: false,
		},
		{
			name:      "network non-timeout error",
			err:       fakeNetError{timeout: false},
			retryable: false,
		},
		{
			name:      "plain error",
			err:       errors.New("policy validation failed"),
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := WrapRetryable(tt.err)
			if got := IsRetryable(wrapped); got != tt.retryable {
				t.Fatalf("IsRetryable(WrapRetryable(%v)) = %v, want %v", tt.err, got, tt.retryable)
			}
			if !errors.Is(wrapped, tt.err) {
				t.Fatalf("expected wrapped error to unwrap to original error %v, got %v", tt.err, wrapped)
			}
		})
	}
}

func TestRetryableErrorUnwrapAndZeroMessage(t *testing.T) {
	if got := (RetryableError{}).Error(); got != "" {
		t.Fatalf("expected empty message for nil underlying error, got %q", got)
	}

	cause := errors.New("throttled")
	wrapped := NewRetryableError(cause)
	if wrapped.Error() != "throttled" {
		t.Fatalf("expected message %q, got %q", "throttled", wrapped.Error())
	}
	if !errors.Is(wrapped, cause) {
		t.Fatalf("expected RetryableError to unwrap to cause")
	}
	if !IsRetryable(fmt.Errorf("outer: %w", wrapped)) {
		t.Fatal("expected IsRetryable to match wrapped RetryableError")
	}
}

type fakeNetError struct {
	timeout bool
}

func (e fakeNetError) Error() string   { return "network error" }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return false }
