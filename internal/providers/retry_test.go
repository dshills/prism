package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// TestMain skips real backoff waits: the tests check how often and how long
// prism would wait, not the wall clock.
func TestMain(m *testing.M) {
	sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	os.Exit(m.Run())
}

// recordSleeps records each backoff wait for the rest of the test.
func recordSleeps(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return ctx.Err()
	}
	t.Cleanup(func() { sleep = old })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), waits...)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{"none", nil, 0},
		{"seconds", map[string]string{"Retry-After": "7"}, 7 * time.Second},
		{"fractional seconds", map[string]string{"Retry-After": "1.5"}, 1500 * time.Millisecond},
		{"milliseconds win", map[string]string{"Retry-After": "7", "Retry-After-Ms": "250"}, 250 * time.Millisecond},
		{"http date", map[string]string{"Retry-After": now.Add(30 * time.Second).Format(http.TimeFormat)}, 30 * time.Second},
		{"date in the past", map[string]string{"Retry-After": now.Add(-time.Minute).Format(http.TimeFormat)}, 0},
		{"zero", map[string]string{"Retry-After": "0"}, 0},
		{"negative", map[string]string{"Retry-After": "-3"}, 0},
		{"garbage", map[string]string{"Retry-After": "soon"}, 0},
		{"bad ms falls back", map[string]string{"Retry-After": "2", "Retry-After-Ms": "x"}, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			if got := parseRetryAfter(h, now); got != tc.want {
				t.Errorf("parseRetryAfter = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for range 20 { // jitter is random: check the bounds hold every time
		if d, ok := retryDelay(0, &rateLimitError{retryAfter: 4 * time.Second}); !ok || d < 4*time.Second || d > 5*time.Second {
			t.Fatalf("asked 4s: got %s, %v; want 4s-5s", d, ok)
		}
		if d, ok := retryDelay(0, &serverError{retryAfter: 2 * time.Second}); !ok || d < 2*time.Second || d > 2500*time.Millisecond {
			t.Fatalf("5xx asked 2s: got %s, %v; want 2s-2.5s", d, ok)
		}
		// A rate limit with no Retry-After backs off from 2s, a server error from 1s.
		if d, ok := retryDelay(0, &rateLimitError{}); !ok || d < time.Second || d > 3*time.Second {
			t.Fatalf("429 attempt 0: got %s, want 1s-3s", d)
		}
		if d, ok := retryDelay(2, &rateLimitError{}); !ok || d < 4*time.Second || d > 12*time.Second {
			t.Fatalf("429 attempt 2: got %s, want 4s-12s", d)
		}
		if d, ok := retryDelay(0, &serverError{}); !ok || d < 500*time.Millisecond || d > 1500*time.Millisecond {
			t.Fatalf("5xx attempt 0: got %s, want 0.5s-1.5s", d)
		}
	}
	if _, ok := retryDelay(0, &rateLimitError{retryAfter: maxRetryAfter + time.Second}); ok {
		t.Error("a wait past maxRetryAfter should not be retried")
	}
}

// rateLimitedServer answers 429 with retryAfter until it has refused `refuse`
// requests, then succeeds with an OpenAI response.
func rateLimitedServer(t *testing.T, refuse int, retryAfter string) (*httptest.Server, *int) {
	t.Helper()
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts <= refuse {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"[]"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts
}

func TestRetry_HonorsRetryAfter(t *testing.T) {
	waits := recordSleeps(t)
	srv, attempts := rateLimitedServer(t, 1, "3")
	o := &OpenAI{apiKey: "k", model: "gpt-4o", baseURL: srv.URL, client: srv.Client()}

	if _, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x"}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if *attempts != 2 {
		t.Errorf("attempts = %d, want 2", *attempts)
	}
	got := waits()
	if len(got) != 1 || got[0] < 3*time.Second || got[0] > 3750*time.Millisecond {
		t.Errorf("waits = %v, want one wait of 3s plus jitter", got)
	}
}

// A server that asks for longer than maxRetryAfter gets its error back at
// once instead of a stalled review.
func TestRetry_GivesUpOnLongRetryAfter(t *testing.T) {
	waits := recordSleeps(t)
	srv, attempts := rateLimitedServer(t, 10, "120")
	o := &OpenAI{apiKey: "k", model: "gpt-4o", baseURL: srv.URL, client: srv.Client()}

	_, err := o.Review(context.Background(), ReviewRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatal("expected the rate limit error")
	}
	if *attempts != 1 || len(waits()) != 0 {
		t.Errorf("attempts = %d, waits = %v; want 1 attempt and no wait", *attempts, waits())
	}
	if want := "rate limited (retry after 2m0s)"; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestIsAuthError_Wrapped(t *testing.T) {
	wrapped := fmt.Errorf("chunked review: %w", fmt.Errorf("chunk 2: %w", &authError{message: "bad key"}))
	if !IsAuthError(wrapped) {
		t.Error("IsAuthError should see through wrapping")
	}
	if IsAuthError(fmt.Errorf("x: %w", &rateLimitError{})) {
		t.Error("a wrapped rate limit is not an auth error")
	}
}
