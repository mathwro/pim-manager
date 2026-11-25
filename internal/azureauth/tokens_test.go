package azureauth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type tokenSourceFunc func(context.Context, string) (string, error)

func (f tokenSourceFunc) AccessToken(ctx context.Context, resource string) (string, error) {
	return f(ctx, resource)
}

func expiringToken(label string, exp time.Time) string {
	return graphToken(fmt.Sprintf(`{"exp":%d,"label":%q}`, exp.Unix(), label))
}

func newTestCachedTokens(source tokenSourceFunc, now *time.Time) *CachedTokens {
	tokens := NewCachedTokens(source)
	tokens.now = func() time.Time { return *now }
	return tokens
}

func TestCachedTokensReuseTokenUntilFiveMinutesBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	var fetches atomic.Int32
	tokens := newTestCachedTokens(func(ctx context.Context, resource string) (string, error) {
		n := fetches.Add(1)
		return expiringToken(fmt.Sprintf("%s-%s-%d", TenantFromContext(ctx), resource, n), exp), nil
	}, &now)
	ctx := WithTenant(context.Background(), "tenant-1")

	first, err := tokens.AccessToken(ctx, "arm")
	if err != nil {
		t.Fatal(err)
	}
	now = exp.Add(-5*time.Minute - time.Second)
	second, err := tokens.AccessToken(ctx, "arm")
	if err != nil || second != first || fetches.Load() != 1 {
		t.Fatalf("expected cached token, fetches=%d err=%v", fetches.Load(), err)
	}
	if _, err := tokens.AccessToken(WithTenant(context.Background(), "tenant-2"), "arm"); err != nil || fetches.Load() != 2 {
		t.Fatalf("expected tenant-scoped cache key, fetches=%d err=%v", fetches.Load(), err)
	}
	if _, err := tokens.AccessToken(ctx, "graph"); err != nil || fetches.Load() != 3 {
		t.Fatalf("expected resource-scoped cache key, fetches=%d err=%v", fetches.Load(), err)
	}

	now = exp.Add(-5 * time.Minute)
	third, err := tokens.AccessToken(ctx, "arm")
	if err != nil || third == first || fetches.Load() != 4 {
		t.Fatalf("expected refetch within five minutes of expiry, fetches=%d err=%v", fetches.Load(), err)
	}
}

func TestCachedTokensDoNotCacheUnreadableExpiryOrErrors(t *testing.T) {
	for _, token := range []string{
		"opaque-token",
		graphToken(`{"exp":"4102444800"}`),
		graphToken(`{"oid":"principal-1"}`),
	} {
		t.Run(token, func(t *testing.T) {
			now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
			var fetches atomic.Int32
			tokens := newTestCachedTokens(func(context.Context, string) (string, error) {
				fetches.Add(1)
				return token, nil
			}, &now)
			for range 2 {
				got, err := tokens.AccessToken(context.Background(), "arm")
				if err != nil || got != token {
					t.Fatalf("expected uncached token returned, got %q err=%v", got, err)
				}
			}
			if fetches.Load() != 2 {
				t.Fatalf("expected every call to fetch, got %d", fetches.Load())
			}
		})
	}

	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var fetches atomic.Int32
	tokens := newTestCachedTokens(func(context.Context, string) (string, error) {
		if fetches.Add(1) == 1 {
			return "", errors.New("az failed")
		}
		return expiringToken("ok", now.Add(time.Hour)), nil
	}, &now)
	if _, err := tokens.AccessToken(context.Background(), "arm"); err == nil {
		t.Fatal("expected source error")
	}
	if _, err := tokens.AccessToken(context.Background(), "arm"); err != nil || fetches.Load() != 2 {
		t.Fatalf("expected error not to be cached, fetches=%d err=%v", fetches.Load(), err)
	}
}

func TestCachedTokensShareOneInFlightFetch(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	var fetches atomic.Int32
	tokens := newTestCachedTokens(func(context.Context, string) (string, error) {
		fetches.Add(1)
		<-release
		return expiringToken("shared", now.Add(time.Hour)), nil
	}, &now)
	joined := make(chan struct{}, 4)
	tokens.onJoin = func() { joined <- struct{}{} }

	const callers = 5
	results := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := tokens.AccessToken(context.Background(), "arm")
			results <- token
			errs <- err
		}()
	}
	for range callers - 1 {
		waitFor(t, joined, "waiter to join in-flight fetch")
	}
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := expiringToken("shared", now.Add(time.Hour))
	for token := range results {
		if token != want {
			t.Fatalf("expected shared token, got %q", token)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("expected one shared fetch, got %d", fetches.Load())
	}
}

func TestCachedTokensResetForcesRefetchAndFencesInFlightFetch(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	started := make(chan int32, 3)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var fetches atomic.Int32
	tokens := newTestCachedTokens(func(context.Context, string) (string, error) {
		n := fetches.Add(1)
		started <- n
		<-releases[n-1]
		return expiringToken(fmt.Sprintf("token-%d", n), now.Add(time.Hour)), nil
	}, &now)
	close(releases[2])

	stale := make(chan string, 1)
	go func() {
		token, _ := tokens.AccessToken(context.Background(), "arm")
		stale <- token
	}()
	if n := waitFor(t, started, "first fetch"); n != 1 {
		t.Fatalf("unexpected fetch %d", n)
	}
	tokens.Reset()

	fresh := make(chan string, 1)
	go func() {
		token, _ := tokens.AccessToken(context.Background(), "arm")
		fresh <- token
	}()
	if n := waitFor(t, started, "post-reset fetch not joining the fenced flight"); n != 2 {
		t.Fatalf("unexpected fetch %d", n)
	}
	close(releases[0])
	if got := waitFor(t, stale, "fenced fetch result"); got != expiringToken("token-1", now.Add(time.Hour)) {
		t.Fatalf("fenced caller should still receive its token, got %q", got)
	}
	close(releases[1])
	if got := waitFor(t, fresh, "post-reset fetch result"); got != expiringToken("token-2", now.Add(time.Hour)) {
		t.Fatalf("unexpected post-reset token %q", got)
	}
	cached, err := tokens.AccessToken(context.Background(), "arm")
	if err != nil || cached != expiringToken("token-2", now.Add(time.Hour)) || fetches.Load() != 2 {
		t.Fatalf("expected the post-reset token cached and the fenced token dropped, got %q fetches=%d err=%v", cached, fetches.Load(), err)
	}

	tokens.Reset()
	if _, err := tokens.AccessToken(context.Background(), "arm"); err != nil || fetches.Load() != 3 {
		t.Fatalf("expected Reset to drop cached tokens, fetches=%d err=%v", fetches.Load(), err)
	}
}

func TestCachedTokensWaiterRetriesWhenLeaderIsCanceled(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	leaderStarted := make(chan struct{})
	var fetches atomic.Int32
	tokens := newTestCachedTokens(func(ctx context.Context, _ string) (string, error) {
		if fetches.Add(1) == 1 {
			close(leaderStarted)
			<-ctx.Done()
			return "", fmt.Errorf("get token: %w", ctx.Err())
		}
		return expiringToken("retried", now.Add(time.Hour)), nil
	}, &now)
	joined := make(chan struct{}, 1)
	tokens.onJoin = func() { joined <- struct{}{} }

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := tokens.AccessToken(leaderCtx, "arm")
		leaderErr <- err
	}()
	waitFor(t, leaderStarted, "leader fetch")
	waiterResult := make(chan string, 1)
	waiterErr := make(chan error, 1)
	go func() {
		token, err := tokens.AccessToken(context.Background(), "arm")
		waiterResult <- token
		waiterErr <- err
	}()
	waitFor(t, joined, "waiter to join")
	cancelLeader()

	if err := waitFor(t, leaderErr, "leader result"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected leader cancellation, got %v", err)
	}
	if err := waitFor(t, waiterErr, "waiter result"); err != nil {
		t.Fatalf("waiter should retry instead of inheriting cancellation: %v", err)
	}
	if got := <-waiterResult; got != expiringToken("retried", now.Add(time.Hour)) || fetches.Load() != 2 {
		t.Fatalf("expected waiter to lead a retry, got %q fetches=%d", got, fetches.Load())
	}
}

func TestCachedTokensWaiterHonorsOwnContext(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	started := make(chan struct{})
	tokens := newTestCachedTokens(func(context.Context, string) (string, error) {
		close(started)
		<-release
		return expiringToken("leader", now.Add(time.Hour)), nil
	}, &now)
	joined := make(chan struct{}, 1)
	tokens.onJoin = func() { joined <- struct{}{} }

	leaderResult := make(chan string, 1)
	go func() {
		token, _ := tokens.AccessToken(context.Background(), "arm")
		leaderResult <- token
	}()
	waitFor(t, started, "leader fetch")
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() {
		_, err := tokens.AccessToken(waiterCtx, "arm")
		waiterErr <- err
	}()
	waitFor(t, joined, "waiter to join")
	cancelWaiter()
	if err := waitFor(t, waiterErr, "waiter cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected waiter cancellation, got %v", err)
	}
	close(release)
	if got := waitFor(t, leaderResult, "leader result"); got != expiringToken("leader", now.Add(time.Hour)) {
		t.Fatalf("leader should be unaffected by waiter cancellation, got %q", got)
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}
