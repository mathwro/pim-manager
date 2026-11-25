package arm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestPostSendsJSONAndDecodesResponse(t *testing.T) {
	var gotMethod, gotPath string
	client := NewClient(
		&http.Client{Transport: armRoundTrip(func(req *http.Request) (*http.Response, error) {
			gotMethod = req.Method
			gotPath = req.URL.RequestURI()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"count":2}`)),
				Header:     make(http.Header),
			}, nil
		})},
		staticTokenSource{},
	)
	var out struct {
		Count int `json:"count"`
	}
	err := client.Post(context.Background(), "/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01", map[string]string{"query": "ResourceContainers"}, &out)
	if err != nil || gotMethod != http.MethodPost || !strings.Contains(gotPath, "Microsoft.ResourceGraph") || out.Count != 2 {
		t.Fatalf("Post = (%v, %s, %s, %#v)", err, gotMethod, gotPath, out)
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

func throttledClient(t *testing.T, responses []int, retryAfter string, onCall func(*http.Request)) (Client, *[]string) {
	t.Helper()
	var bodies []string
	client := NewClient(&http.Client{Transport: armRoundTrip(func(req *http.Request) (*http.Response, error) {
		if onCall != nil {
			onCall(req)
		}
		body := ""
		if req.Body != nil {
			b, _ := io.ReadAll(req.Body)
			body = string(b)
		}
		bodies = append(bodies, body)
		status := responses[min(len(bodies), len(responses))-1]
		header := make(http.Header)
		if status == http.StatusTooManyRequests {
			header.Set("Retry-After", retryAfter)
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(`{"count":1}`))}, nil
	})}, staticTokenSource{})
	return client, &bodies
}

func TestReadRequestsRetryAfterThrottling(t *testing.T) {
	client, calls := throttledClient(t, []int{http.StatusTooManyRequests, http.StatusOK}, "0", nil)
	var out struct {
		Count int `json:"count"`
	}
	if err := client.Get(context.Background(), "/subscriptions?api-version=2022-12-01", &out); err != nil || out.Count != 1 || len(*calls) != 2 {
		t.Fatalf("GET after 429 = (%v, %+v), calls=%d", err, out, len(*calls))
	}
}

func TestPostRetriesResendTheSameBodyAndStopAfterTwoRetries(t *testing.T) {
	client, bodies := throttledClient(t, []int{http.StatusTooManyRequests}, "0", nil)
	err := client.Post(context.Background(), "/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01", map[string]string{"query": "ResourceContainers"}, nil)
	var responseErr ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected the final 429 to surface, got %v", err)
	}
	if len(*bodies) != 3 {
		t.Fatalf("expected one request plus two retries, got %d", len(*bodies))
	}
	for _, body := range *bodies {
		if !strings.Contains(body, "ResourceContainers") {
			t.Fatalf("retry lost the request body: %q", *bodies)
		}
	}
}

func TestPutIsNeverRetriedAfterThrottling(t *testing.T) {
	client, calls := throttledClient(t, []int{http.StatusTooManyRequests, http.StatusOK}, "0", nil)
	err := client.Put(context.Background(), "/subscriptions/sub-1/providers/Microsoft.Authorization/roleAssignmentScheduleRequests/request-1?api-version=2020-10-01", map[string]string{"requestType": "SelfActivate"}, nil)
	var responseErr ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusTooManyRequests || len(*calls) != 1 {
		t.Fatalf("activation PUT was replayed after 429: err=%v calls=%d", err, len(*calls))
	}
}

func TestThrottleRetryDoesNotWaitBeyondLimit(t *testing.T) {
	client, calls := throttledClient(t, []int{http.StatusTooManyRequests, http.StatusOK}, "3600", nil)
	err := client.Get(context.Background(), "/subscriptions?api-version=2022-12-01", nil)
	var responseErr ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusTooManyRequests || len(*calls) != 1 {
		t.Fatalf("long Retry-After must surface the 429 instead of blocking: err=%v calls=%d", err, len(*calls))
	}
}

func TestThrottleRetryStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, calls := throttledClient(t, []int{http.StatusTooManyRequests, http.StatusOK}, "5", func(*http.Request) { cancel() })
	err := client.Get(ctx, "/subscriptions?api-version=2022-12-01", nil)
	if !errors.Is(err, context.Canceled) || len(*calls) != 1 {
		t.Fatalf("canceled wait must stop retrying: err=%v calls=%d", err, len(*calls))
	}
}

func TestThrottleWaitReadsRetryAfterForms(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{"", defaultThrottleWait, true},
		{"not a delay", defaultThrottleWait, true},
		{"3", 3 * time.Second, true},
		{"-4", 0, true},
		{now.Add(4 * time.Second).Format(http.TimeFormat), 4 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"11", 11 * time.Second, false},
		{now.Add(time.Hour).Format(http.TimeFormat), time.Hour, false},
	} {
		if got, ok := throttleWait(tc.header, now); got != tc.want || ok != tc.ok {
			t.Errorf("throttleWait(%q) = (%s, %t), want (%s, %t)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}
