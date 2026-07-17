package arm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const BaseURL = "https://management.azure.com"
const Resource = "https://management.core.windows.net/"
const AuthorizationAPIVersion = "2020-10-01"

type accessTokenKey struct{}

func WithAccessToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, accessTokenKey{}, token)
}

func PinnedAccessToken(ctx context.Context) string {
	token, _ := ctx.Value(accessTokenKey{}).(string)
	return token
}

type TokenSource interface {
	AccessToken(context.Context, string) (string, error)
}

type Client struct {
	httpClient  *http.Client
	tokenSource TokenSource
	baseURL     string
}

type ResponseError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e ResponseError) Error() string {
	return fmt.Sprintf("ARM %s %s failed: %s: %s", e.Method, e.URL, e.Status, e.Body)
}

func NewClient(httpClient *http.Client, tokenSource TokenSource) Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return Client{httpClient: httpClient, tokenSource: tokenSource, baseURL: BaseURL}
}

func (c Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c Client) Put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

func (c Client) do(ctx context.Context, method string, path string, body any, out any) error {
	token := PinnedAccessToken(ctx)
	if token == "" {
		var err error
		token, err = c.tokenSource.AccessToken(ctx, Resource)
		if err != nil {
			return err
		}
	}
	u := c.baseURL + path
	if strings.HasPrefix(path, "https://") {
		u = path
	}
	var reader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encode ARM request: %w", err)
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
