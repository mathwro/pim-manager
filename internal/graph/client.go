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

type Client struct {
	httpClient  *http.Client
	tokenSource TokenSource
	baseURL     string
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

func (c Client) Post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c Client) do(ctx context.Context, method string, path string, body any, out any) error {
	token, err := c.tokenSource.AccessToken(ctx, Resource)
	if err != nil {
		return err
	}
	u := c.baseURL + path
	if strings.HasPrefix(path, "https://") {
		u = path
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
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Graph %s %s failed: %s: %s", method, u, resp.Status, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func EscapeFilterValue(value string) string {
	return url.QueryEscape(strings.ReplaceAll(value, "'", "''"))
}
