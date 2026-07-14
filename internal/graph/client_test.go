package graph

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetReturnsResponseErrorWithStatusCode(t *testing.T) {
	client := NewClient(
		&http.Client{
			Transport: graphRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Status:     "429 Too Many Requests",
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"throttled"}}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		staticTokenSource{},
	)

	err := client.Get(context.Background(), "/roleManagement/directory/roleEligibilitySchedules", nil)
	if err == nil {
		t.Fatal("expected get error")
	}

	var responseErr ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("expected response error type, got %T", err)
	}
	if responseErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected status code 429, got %d", responseErr.StatusCode)
	}
}

type graphRoundTrip func(*http.Request) (*http.Response, error)

func (f graphRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staticTokenSource struct{}

func (staticTokenSource) AccessToken(context.Context, string) (string, error) {
	return "token", nil
}
