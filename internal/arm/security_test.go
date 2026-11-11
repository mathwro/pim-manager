package arm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestARMRejectsUntrustedPaginationBeforeFetchingToken(t *testing.T) {
	for _, path := range []string{
		"https://evil.example/next",
		"https://management.azure.com.evil.example/next",
		"https://sub.management.azure.com/next",
		"http://management.azure.com/next",
		"https://user@management.azure.com/next",
		"//evil.example/next",
		"https://management.azure.com:444/next",
		"subscriptions/sub-1",
		"/subscriptions/sub-1#frag",
		"https://management.azure.com/next#frag",
	} {
		t.Run(path, func(t *testing.T) {
			source := &recordingTokenSource{}
			requests := 0
			client := NewClient(&http.Client{Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			})}, source)
			if err := client.Get(context.Background(), path, nil); err == nil {
				t.Fatal("accepted an untrusted pagination URL")
			}
			if requests != 0 || source.calls != 0 {
				t.Fatalf("untrusted URL reached authentication/transport: requests=%d token calls=%d", requests, source.calls)
			}
		})
	}
}

func TestARMAcceptsTrustedPaginationURL(t *testing.T) {
	source := &recordingTokenSource{}
	var seen []string
	client := NewClient(&http.Client{Transport: armRoundTrip(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}, source)
	ctx := WithAccessToken(context.Background(), "checked-token")
	for _, path := range []string{
		"/subscriptions?api-version=2020-01-01",
		"https://management.azure.com/subscriptions?api-version=2020-01-01&$skiptoken=page2",
		"https://MANAGEMENT.AZURE.COM/subscriptions?api-version=2020-01-01&$skiptoken=page3",
	} {
		if err := client.Get(ctx, path, nil); err != nil {
			t.Fatalf("Get(%q) failed: %v", path, err)
		}
	}
	if source.calls != 0 || len(seen) != 3 {
		t.Fatalf("unexpected calls: token=%d requests=%d", source.calls, len(seen))
	}
}

func TestARMDoesNotLeakBearerThroughRedirectsOrReplayActivation(t *testing.T) {
	t.Run("cross-origin GET redirect blocked", func(t *testing.T) {
		requests := 0
		client := NewClient(&http.Client{Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Status:     "307 Temporary Redirect",
				Header:     http.Header{"Location": []string{"https://sub.management.azure.com/stolen"}},
				Body:       io.NopCloser(strings.NewReader(`redirect`)),
			}, nil
		})}, staticTokenSource{})
		err := client.Get(WithAccessToken(context.Background(), "checked"), "/initial", nil)
		if err == nil || requests != 1 {
			t.Fatalf("cross-origin redirect leaked bearer: requests=%d err=%v", requests, err)
		}
	})

	t.Run("PUT redirect not followed or replayed", func(t *testing.T) {
		requests := 0
		client := NewClient(&http.Client{Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Status:     "307 Temporary Redirect",
				Header:     http.Header{"Location": []string{"https://management.azure.com/replayed"}},
				Body:       io.NopCloser(strings.NewReader(`redirect`)),
			}, nil
		})}, staticTokenSource{})
		err := client.Put(WithAccessToken(context.Background(), "checked"), "/initial", map[string]string{"requestType": "SelfActivate"}, nil)
		if err == nil || requests != 1 {
			t.Fatalf("PUT redirect replayed activation or succeeded: requests=%d err=%v", requests, err)
		}
		var respErr ResponseError
		if !errors.As(err, &respErr) || respErr.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected 307 ResponseError, got %v", err)
		}
	})

	t.Run("same-origin GET redirect allowed up to caller CheckRedirect", func(t *testing.T) {
		requests := 0
		callerChecks := 0
		baseHTTP := &http.Client{
			Transport: armRoundTrip(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.Path == "/initial" {
					return &http.Response{
						StatusCode: http.StatusTemporaryRedirect,
						Status:     "307 Temporary Redirect",
						Header:     http.Header{"Location": []string{"https://management.azure.com/next"}},
						Body:       io.NopCloser(strings.NewReader(``)),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Header:     make(http.Header),
				}, nil
			}),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				callerChecks++
				return nil
			},
		}
		client := NewClient(baseHTTP, staticTokenSource{})
		if err := client.Get(WithAccessToken(context.Background(), "checked"), "/initial", nil); err != nil {
			t.Fatalf("same-origin GET redirect failed: %v", err)
		}
		if requests != 2 || callerChecks != 1 {
			t.Fatalf("expected 2 requests and 1 caller check, got requests=%d callerChecks=%d", requests, callerChecks)
		}
	})

	t.Run("does not mutate caller http.Client", func(t *testing.T) {
		baseHTTP := &http.Client{}
		_ = NewClient(baseHTTP, staticTokenSource{})
		if baseHTTP.CheckRedirect != nil {
			t.Fatal("NewClient mutated caller http.Client")
		}
	})

	t.Run("same-origin GET redirect capped at 10 when no caller CheckRedirect", func(t *testing.T) {
		requests := 0
		client := NewClient(&http.Client{
			Transport: armRoundTrip(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{
					StatusCode: http.StatusTemporaryRedirect,
					Status:     "307 Temporary Redirect",
					Header:     http.Header{"Location": []string{"https://management.azure.com/loop"}},
					Body:       io.NopCloser(strings.NewReader(``)),
				}, nil
			}),
		}, staticTokenSource{})
		err := client.Get(WithAccessToken(context.Background(), "checked"), "/initial", nil)
		if err == nil || requests != 10 {
			t.Fatalf("expected redirect cap at 10 requests, got requests=%d err=%v", requests, err)
		}
	})
}
