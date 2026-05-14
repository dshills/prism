# PLAN: Dynamic Concurrency + Rate Limiting for Chunked Review

## Goal

Raise chunked-review throughput by increasing the default concurrency ceiling
and adding a token-bucket rate limiter that prevents hitting provider rate
limits before retries kick in.

## Current state

`maxConcurrency = 4` is a hard-coded constant in `internal/review/chunker.go`.
Providers handle reactive 429 recovery via `retryWithBackoff`, but there is no
proactive pre-flight throttle. With 4 concurrent goroutines and typical LLM
latency of 5–15 s/chunk, a 50-chunk codebase review takes 62–187 s.

## Design decisions

### Rate limiter placement
The limiter lives in a new `internal/ratelimit` package and is used by the
chunker. Providers keep their existing reactive retry; the limiter adds a
complementary proactive layer that avoids the retry overhead altogether.

### Token-bucket implementation
Lazy-refill bucket (no background goroutine): refill is computed from wall-clock
elapsed time on each `Wait` call. Zero RPM means unlimited.  Initial burst capped
at `min(rpm, 10)` to allow short parallel bursts without immediately draining the
bucket.

### Per-provider defaults

| Provider  | Default RPM | Default max concurrency |
|-----------|-------------|-------------------------|
| anthropic | 50          | 8                       |
| openai    | 60          | 8                       |
| gemini    | 60          | 8                       |
| ollama    | 0 (none)    | 16                      |
| *unknown* | 60          | 8                       |

### Configuration
Two new optional fields in `Config`; zero value means "use provider default":

```json
{
  "maxConcurrency": 0,
  "rateLimitRpm": 0
}
```

Env vars: `PRISM_MAX_CONCURRENCY`, `PRISM_RATE_LIMIT_RPM`.
No new CLI flags (config/env is sufficient for tuning).

## Implementation phases

### Phase 1 — Config fields (`internal/config/config.go`)
- Add `MaxConcurrency int` and `RateLimitRPM int` to `Config`.
- Add env-var loading for both in `Load`.
- Update `Default()` (both remain 0).

### Phase 2 — Rate limiter (`internal/ratelimit/ratelimit.go`)
- `type Limiter struct` — lazy-refill token bucket.
- `New(rpm int) *Limiter` — nil when rpm ≤ 0 (unlimited).
- `(*Limiter).Wait(ctx context.Context) error` — blocks until token available
  or ctx cancelled.

### Phase 3 — Provider defaults (`internal/providers/defaults.go`)
- `DefaultRPM(name string) int`
- `DefaultMaxConcurrency(name string) int`

### Phase 4 — Wire into chunker (`internal/review/chunker.go`)
- Remove `maxConcurrency` constant.
- `RunChunkedWithOptions` receives a `providers.Reviewer`; call `provider.Name()`
  to pick defaults.
- Compute effective concurrency and RPM from config + provider defaults.
- Create `ratelimit.New(effectiveRPM)`, call `limiter.Wait(ctx)` inside each
  goroutine before the LLM call.

## Files changed
- `internal/config/config.go`
- `internal/ratelimit/ratelimit.go` (new)
- `internal/ratelimit/ratelimit_test.go` (new)
- `internal/providers/defaults.go` (new)
- `internal/review/chunker.go`
