package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Fallback is a Reviewer that reviews with a primary provider and, once the
// primary fails in a way another provider could get past (an auth error,
// retries exhausted on rate limits or server errors, a transport failure, a
// missing model), with a fallback provider for the rest of its life. An agent
// cannot fix a provider outage, so without a fallback the review gate is lost.
//
// The switch is sticky so the other chunks of a review do not each pay for
// the primary's failure first. The fallback is created only when first
// needed, so a fallback that is never used needs no credentials.
type Fallback struct {
	primary      Reviewer // nil when the primary could not be created
	primaryLabel string   // "provider:model"
	label        string   // the fallback's "provider:model"
	create       func() (Reviewer, error)

	mu       sync.Mutex
	switched bool
	reason   string // why the primary was left
	cause    error  // the primary's error, or why it could not be created
	fb       Reviewer
	fbErr    error
}

// NewFallback wraps primary with a fallback created by create. primaryErr is
// the error creating the primary, if it failed; the fallback is then used
// from the start.
func NewFallback(primary Reviewer, primaryErr error, primaryLabel, fallbackLabel string, create func() (Reviewer, error)) *Fallback {
	f := &Fallback{primary: primary, primaryLabel: primaryLabel, label: fallbackLabel, create: create}
	if primary == nil || primaryErr != nil {
		f.primary = nil
		f.switched = true
		f.cause = primaryErr
		f.reason = brief(fmt.Sprintf("%s unavailable: %v", primaryLabel, primaryErr))
	}
	return f
}

// Name is the primary's name, which sets the rate limits a review starts
// with, or the fallback's when there is no primary.
func (f *Fallback) Name() string {
	if f.primary != nil {
		return f.primary.Name()
	}
	if fb, err := f.fallback(); err == nil {
		return fb.Name()
	}
	return "fallback"
}

// FellBack reports whether the fallback has been used, and why.
func (f *Fallback) FellBack() (label, reason string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.label, f.reason, f.switched
}

func (f *Fallback) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	f.mu.Lock()
	switched := f.switched
	f.mu.Unlock()

	calls := 0
	if !switched {
		resp, err := f.primary.Review(ctx, req)
		if err == nil || !shouldFallBack(ctx, err) {
			return resp, err
		}
		calls = 1
		f.mu.Lock()
		if !f.switched {
			f.switched = true
			f.cause = err
			f.reason = brief(fmt.Sprintf("%s failed: %v", f.primaryLabel, err))
		}
		f.mu.Unlock()
	}

	fb, err := f.fallback()
	if err != nil {
		return ReviewResponse{Calls: max(calls, 1)}, fmt.Errorf("%w; fallback %s could not be created: %w", f.primaryCause(), f.label, err)
	}
	resp, err := fb.Review(ctx, req)
	resp.Calls = calls + CallsOf(resp)
	if err != nil {
		return resp, fmt.Errorf("fallback %s: %w (after %w)", f.label, err, f.primaryCause())
	}
	resp.Fallback = true
	return resp, nil
}

// fallback creates the fallback provider on first use.
func (f *Fallback) fallback() (Reviewer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fb == nil && f.fbErr == nil {
		f.fb, f.fbErr = f.create()
		if f.fb == nil && f.fbErr == nil {
			f.fbErr = errors.New("no provider")
		}
	}
	return f.fb, f.fbErr
}

func (f *Fallback) primaryCause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cause == nil {
		return fmt.Errorf("%s failed", f.primaryLabel)
	}
	return f.cause
}

// maxReason caps a fallback reason, which quotes an error that may carry a
// whole API response body.
const maxReason = 200

func brief(s string) string {
	s = strings.Join(strings.Fields(s), " ") // one line, even for a pretty-printed body
	if len(s) <= maxReason {
		return s
	}
	return s[:maxReason] + "…"
}

// shouldFallBack reports whether err from the primary is one another
// provider could get past. A cancelled review is not, and nor is a request
// the endpoint refused for its content (400, 413, 422): the fallback would
// most likely refuse it too, and switching on it would move the rest of the
// review to the fallback for one bad request.
func shouldFallBack(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	if IsTruncated(err) {
		return false // the output is too long for one response, from any model
	}
	var re *requestError
	if errors.As(err, &re) {
		switch re.status {
		case 400, 413, 422:
			return false
		}
	}
	return true
}
