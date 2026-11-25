package arm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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
	client := *httpClient
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// A redirected PUT can replay a real activation without fresh confirmation.
		if len(via) == 0 || via[0].Method != http.MethodGet {
			return http.ErrUseLastResponse
		}
		if !sameOrigin(req.URL, via[0].URL) {
			return fmt.Errorf("refusing ARM redirect outside ARM origin")
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 ARM redirects")
		}
		return nil
	}
	return Client{httpClient: &client, tokenSource: tokenSource, baseURL: BaseURL}
}

func (c Client) PinAccessToken(ctx context.Context) (context.Context, error) {
	if PinnedAccessToken(ctx) != "" {
		return ctx, nil
	}
	token, err := c.tokenSource.AccessToken(ctx, Resource)
	if err != nil {
		return nil, err
	}
	return WithAccessToken(ctx, token), nil
}

func (c Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c Client) Post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c Client) Put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

func (c Client) do(ctx context.Context, method string, path string, body any, out any) error {
	u, err := c.requestURL(path)
	if err != nil {
		return err
	}
	ctx, err = c.PinAccessToken(ctx)
	if err != nil {
		return err
	}
	token := PinnedAccessToken(ctx)
	var payload []byte
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encode ARM request: %w", err)
		}
		payload = buf.Bytes()
	}
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
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
		// Only reads are retried: GET, and POST, which this client uses solely for
		// Resource Graph queries. Activation PUTs are never replayed.
		if resp.StatusCode == http.StatusTooManyRequests && (method == http.MethodGet || method == http.MethodPost) && attempt < maxThrottleRetries {
			if wait, ok := throttleWait(resp.Header.Get("Retry-After"), time.Now()); ok {
				resp.Body.Close()
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
		}
		return decodeResponse(resp, method, u, out)
	}
}

const (
	maxThrottleRetries  = 2
	defaultThrottleWait = 2 * time.Second
	maxThrottleWait     = 10 * time.Second
)

// throttleWait reads Retry-After (seconds or HTTP date). Waits longer than
// maxThrottleWait are not retried so the 429 reaches the user promptly.
func throttleWait(retryAfter string, now time.Time) (time.Duration, bool) {
	retryAfter = strings.TrimSpace(retryAfter)
	wait := defaultThrottleWait
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		wait = time.Duration(max(seconds, 0)) * time.Second
	} else if at, err := http.ParseTime(retryAfter); err == nil {
		wait = max(at.Sub(now), 0)
	}
	return wait, wait <= maxThrottleWait
}

func decodeResponse(resp *http.Response, method string, u string, out any) error {
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

func (c Client) requestURL(path string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid ARM base URL: %w", err)
	}
	target, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("invalid ARM request URL: %w", err)
	}
	if target.IsAbs() {
		if !sameOrigin(target, base) {
			return "", fmt.Errorf("refusing ARM URL outside ARM origin: %s", path)
		}
		return target.String(), nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || target.Host != "" || target.User != nil || target.Fragment != "" {
		return "", fmt.Errorf("invalid relative ARM request URL: %s", path)
	}
	return c.baseURL + path, nil
}

func sameOrigin(target, base *url.URL) bool {
	return target.User == nil && target.Opaque == "" && target.Fragment == "" &&
		strings.EqualFold(target.Scheme, base.Scheme) && strings.EqualFold(target.Host, base.Host)
}
