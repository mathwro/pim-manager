package azureauth

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// tokenRefreshMargin keeps cached tokens from being handed out close to expiry.
const tokenRefreshMargin = 5 * time.Minute

// CachedTokens reuses read-only access tokens per tenant and resource for the
// session. It never persists tokens and must not back activation, which pins
// a freshly checked token instead.
type CachedTokens struct {
	source interface {
		AccessToken(context.Context, string) (string, error)
	}
	now func() time.Time
	// onJoin runs after a caller joins another caller's in-flight fetch (tests only).
	onJoin func()

	mu         sync.Mutex
	generation uint64
	entries    map[tokenKey]cachedToken
	flights    map[tokenKey]*tokenFlight
}

type tokenKey struct {
	tenantID string
	resource string
}

type cachedToken struct {
	token   string
	expires time.Time
}

type tokenFlight struct {
	done           chan struct{}
	token          string
	err            error
	leaderCanceled bool
}

func NewCachedTokens(source interface {
	AccessToken(context.Context, string) (string, error)
}) *CachedTokens {
	return &CachedTokens{
		source:  source,
		now:     time.Now,
		entries: make(map[tokenKey]cachedToken),
		flights: make(map[tokenKey]*tokenFlight),
	}
}

// AccessToken returns a cached token valid for more than five minutes or
// fetches one, sharing a single in-flight fetch between concurrent callers.
func (c *CachedTokens) AccessToken(ctx context.Context, resource string) (string, error) {
	key := tokenKey{tenantID: TenantFromContext(ctx), resource: resource}
	for {
		c.mu.Lock()
		if entry, ok := c.entries[key]; ok && entry.expires.Sub(c.now()) > tokenRefreshMargin {
			c.mu.Unlock()
			return entry.token, nil
		}
		if flight, ok := c.flights[key]; ok {
			c.mu.Unlock()
			if c.onJoin != nil {
				c.onJoin()
			}
			select {
			case <-flight.done:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if flight.leaderCanceled && ctx.Err() == nil {
				continue
			}
			return flight.token, flight.err
		}
		flight := &tokenFlight{done: make(chan struct{})}
		c.flights[key] = flight
		generation := c.generation
		c.mu.Unlock()

		flight.token, flight.err = c.source.AccessToken(ctx, resource)
		flight.leaderCanceled = flight.err != nil &&
			(ctx.Err() != nil || errors.Is(flight.err, context.Canceled) || errors.Is(flight.err, context.DeadlineExceeded))

		c.mu.Lock()
		if c.flights[key] == flight {
			delete(c.flights, key)
		}
		if flight.err == nil && generation == c.generation {
			if expires, ok := tokenExpiry(flight.token); ok {
				c.entries[key] = cachedToken{token: flight.token, expires: expires}
			}
		}
		c.mu.Unlock()
		close(flight.done)
		return flight.token, flight.err
	}
}

// Reset drops cached tokens; fetches already in flight still answer their
// callers but are neither stored nor joined by later callers.
func (c *CachedTokens) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	clear(c.entries)
	clear(c.flights)
}

func tokenExpiry(token string) (time.Time, bool) {
	payload, ok := jwtPayload(token)
	if !ok {
		return time.Time{}, false
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == nil {
		return time.Time{}, false
	}
	return time.Unix(int64(*claims.Exp), 0), true
}
