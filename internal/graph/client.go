package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const BaseURL = "https://graph.microsoft.com/v1.0"
const Resource = "https://graph.microsoft.com/"

type TokenSource interface {
	AccessToken(context.Context, string) (string, error)
}

// tokenInvalidator is implemented by token sources that cache tokens: a token
// Graph rejects with 401 must not be served again.
type tokenInvalidator interface {
	InvalidateAccessToken(string)
}

type Client struct {
	httpClient  *http.Client
	tokenSource TokenSource
	baseURL     string
}

type accessTokenKey struct{}

func WithAccessToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, accessTokenKey{}, token)
}

func PinnedAccessToken(ctx context.Context) string {
	token, _ := ctx.Value(accessTokenKey{}).(string)
	return token
}

type ResponseError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e ResponseError) Error() string {
	return fmt.Sprintf("Graph %s %s failed: %s: %s", e.Method, e.URL, e.Status, e.Body)
}

func NewClient(httpClient *http.Client, tokenSource TokenSource) Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	client := *httpClient
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// A redirected POST can replay a real activation without fresh confirmation.
		if len(via) == 0 || via[0].Method != http.MethodGet {
			return http.ErrUseLastResponse
		}
		if !sameOrigin(req.URL, via[0].URL) {
			return fmt.Errorf("refusing Graph redirect outside Graph origin")
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 Graph redirects")
		}
		return nil
	}
	return Client{httpClient: &client, tokenSource: tokenSource, baseURL: BaseURL}
}

func (c Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c Client) Post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c Client) do(ctx context.Context, method string, path string, body any, out any) error {
	u, err := c.requestURL(path)
	if err != nil {
		return err
	}
	token := PinnedAccessToken(ctx)
	if token == "" {
		if c.tokenSource == nil {
			return fmt.Errorf("Graph authentication is not connected")
		}
		token, err = c.tokenSource.AccessToken(ctx, Resource)
		if err != nil {
			return err
		}
	}
	if token == "" {
		return fmt.Errorf("Graph authentication returned an empty access token")
	}
	var reader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encode Graph request: %w", err)
		}
		reader = buf
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if invalidator, ok := c.tokenSource.(tokenInvalidator); ok && resp.StatusCode == http.StatusUnauthorized {
			invalidator.InvalidateAccessToken(token)
		}
		b, _ := io.ReadAll(resp.Body)
		return ResponseError{
			Method:     method,
			URL:        u,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(b)),
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c Client) requestURL(path string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid Graph base URL: %w", err)
	}
	target, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("invalid Graph request URL: %w", err)
	}
	if target.IsAbs() {
		if !sameOrigin(target, base) {
			return "", fmt.Errorf("refusing Graph URL outside Graph origin: %s", path)
		}
		return target.String(), nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || target.Host != "" || target.User != nil || target.Fragment != "" {
		return "", fmt.Errorf("invalid relative Graph request URL: %s", path)
	}
	return c.baseURL + path, nil
}

func sameOrigin(target, base *url.URL) bool {
	return target.User == nil && target.Opaque == "" && target.Fragment == "" &&
		strings.EqualFold(target.Scheme, base.Scheme) && strings.EqualFold(target.Host, base.Host)
}

func EscapeFilterValue(value string) string {
	return url.QueryEscape(strings.ReplaceAll(value, "'", "''"))
}
