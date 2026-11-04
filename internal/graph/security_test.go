package graph

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPinnedTokenIsUsedWithoutConsultingTokenSource(t *testing.T) {
	source := &countingTokenSource{}
	var tokens []string
	client := NewClient(&http.Client{Transport: graphRoundTrip(func(req *http.Request) (*http.Response, error) {
		tokens = append(tokens, req.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}, source)
	ctx := WithAccessToken(context.Background(), "checked-token")
	for _, path := range []string{"/first", "https://graph.microsoft.com/v1.0/next?$skiptoken=second"} {
		if err := client.Get(ctx, path, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Post(ctx, "/activate", map[string]string{"action": "selfActivate"}, nil); err != nil {
		t.Fatal(err)
	}
	if PinnedAccessToken(ctx) != "checked-token" || source.calls != 0 || len(tokens) != 3 {
		t.Fatalf("checked token not preserved: token=%q calls=%d requests=%d", PinnedAccessToken(ctx), source.calls, len(tokens))
	}
	for _, token := range tokens {
		if token != "Bearer checked-token" {
			t.Fatalf("activation or pagination replaced the checked token: %q", token)
		}
	}
}

func TestGraphRejectsUntrustedPaginationBeforeFetchingToken(t *testing.T) {
	for _, path := range []string{
		"https://evil.example/next", "https://graph.microsoft.com.evil.example/next",
		"https://sub.graph.microsoft.com/next", "http://graph.microsoft.com/next",
		"https://user@graph.microsoft.com/next", "//evil.example/next", "https://graph.microsoft.com:444/next",
	} {
		t.Run(path, func(t *testing.T) {
			source := &countingTokenSource{}
			requests := 0
			client := NewClient(&http.Client{Transport: graphRoundTrip(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
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

func TestGraphDoesNotLeakBearerThroughRedirectsOrReplayActivation(t *testing.T) {
	for _, test := range []struct{ method, target string }{
		{http.MethodGet, "https://sub.graph.microsoft.com/stolen"},
		{http.MethodPost, "https://graph.microsoft.com/v1.0/replayed"},
	} {
		t.Run(test.method, func(t *testing.T) {
			requests := 0
			client := NewClient(&http.Client{Transport: graphRoundTrip(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{test.target}}, Body: io.NopCloser(strings.NewReader(`redirect`))}, nil
			})}, staticTokenSource{})
			err := client.do(WithAccessToken(context.Background(), "checked"), test.method, "/initial", map[string]string{"action": "selfActivate"}, nil)
			if err == nil || requests != 1 {
				t.Fatalf("redirect leaked bearer or replayed activation: requests=%d err=%v", requests, err)
			}
		})
	}
}

type countingTokenSource struct{ calls int }

func (s *countingTokenSource) AccessToken(context.Context, string) (string, error) {
	s.calls++
	return "alternative-token", nil
}
