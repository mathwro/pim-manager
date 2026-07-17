package arm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPutReturnsResponseErrorWithStatusCode(t *testing.T) {
	client := NewClient(
		&http.Client{
			Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Status:     "400 Bad Request",
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"validation failed"}}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		staticTokenSource{},
	)

	err := client.Put(context.Background(), "/subscriptions/sub-1/providers/Microsoft.Authorization/roleAssignmentScheduleRequests/request-1?api-version=2020-10-01", map[string]string{"requestType": "SelfActivate"}, nil)
	if err == nil {
		t.Fatal("expected put error")
	}

	var responseErr ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("expected response error type, got %T", err)
	}
	if responseErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status code 400, got %d", responseErr.StatusCode)
	}
}

func TestClientRequestsMFAChallengeARMResource(t *testing.T) {
	tokenSource := &recordingTokenSource{}
	client := NewClient(
		&http.Client{Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		})},
		tokenSource,
	)

	if err := client.Get(context.Background(), "/subscriptions/sub-1", nil); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if tokenSource.resource != "https://management.core.windows.net/" {
		t.Fatalf("expected MFA-challenged ARM resource, got %q", tokenSource.resource)
	}
}

func TestClientUsesPinnedContextToken(t *testing.T) {
	tokenSource := &recordingTokenSource{}
	client := NewClient(
		&http.Client{Transport: armRoundTrip(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer checked-token" {
				t.Fatalf("expected checked token, got %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		})},
		tokenSource,
	)

	ctx := WithAccessToken(context.Background(), "checked-token")
	if err := client.Put(ctx, "/subscriptions/sub-1", map[string]string{"requestType": "SelfActivate"}, nil); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if tokenSource.resource != "" {
		t.Fatalf("expected pinned token to bypass token source, got resource %q", tokenSource.resource)
	}
}

func TestPinnedPhaseUsesOneTokenForMultipleRequests(t *testing.T) {
	tokenSource := &recordingTokenSource{}
	client := NewClient(&http.Client{Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}, tokenSource)

	ctx, err := client.PinAccessToken(context.Background())
	if err != nil {
		t.Fatalf("PinAccessToken returned error: %v", err)
	}
	if err := client.Get(ctx, "/one", nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(ctx, "/two", nil); err != nil {
		t.Fatal(err)
	}
	if tokenSource.calls != 1 {
		t.Fatalf("expected one token lookup, got %d", tokenSource.calls)
	}
}

type armRoundTrip func(*http.Request) (*http.Response, error)

func (f armRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staticTokenSource struct{}

func (staticTokenSource) AccessToken(context.Context, string) (string, error) {
	return "token", nil
}

type recordingTokenSource struct {
	resource string
	calls    int
}

func (s *recordingTokenSource) AccessToken(_ context.Context, resource string) (string, error) {
	s.calls++
	s.resource = resource
	return "token", nil
}
