package providers

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// stubReviewer answers with content, or fails with err.
type stubReviewer struct {
	name    string
	err     error
	content string
	calls   atomic.Int32
}

func (s *stubReviewer) Review(context.Context, ReviewRequest) (ReviewResponse, error) {
	s.calls.Add(1)
	if s.err != nil {
		return ReviewResponse{}, s.err
	}
	return ReviewResponse{Content: s.content, Provider: s.name, Model: "m"}, nil
}

func (s *stubReviewer) Name() string { return s.name }

func fallbackTo(t *testing.T, fb Reviewer, fbErr error) (func() (Reviewer, error), *atomic.Int32) {
	t.Helper()
	var created atomic.Int32
	return func() (Reviewer, error) {
		created.Add(1)
		return fb, fbErr
	}, &created
}

// A working primary is used, and the fallback is never even created.
func TestFallback_PrimaryWorks(t *testing.T) {
	primary := &stubReviewer{name: "openai", content: "[]"}
	create, created := fallbackTo(t, &stubReviewer{name: "ollama", content: "[]"}, nil)
	f := NewFallback(primary, nil, "openai:gpt", "ollama:llama", create)

	resp, err := f.Review(context.Background(), ReviewRequest{})
	if err != nil || resp.Fallback || resp.Provider != "openai" {
		t.Fatalf("Review = %+v, %v; want the primary's answer", resp, err)
	}
	if _, _, used := f.FellBack(); used || created.Load() != 0 {
		t.Errorf("fell back = %v, fallback created %d times; want neither", used, created.Load())
	}
	if f.Name() != "openai" {
		t.Errorf("Name = %q, want the primary's", f.Name())
	}
}

// An auth failure moves the review to the fallback, and the switch sticks:
// later calls do not try the primary again.
func TestFallback_SwitchesAndSticks(t *testing.T) {
	primary := &stubReviewer{name: "openai", err: &authError{message: "invalid key"}}
	fb := &stubReviewer{name: "ollama", content: "[]"}
	create, created := fallbackTo(t, fb, nil)
	f := NewFallback(primary, nil, "openai:gpt", "ollama:llama", create)

	for i := range 3 {
		resp, err := f.Review(context.Background(), ReviewRequest{})
		if err != nil || !resp.Fallback || resp.Provider != "ollama" {
			t.Fatalf("Review = %+v, %v; want the fallback's answer", resp, err)
		}
		// The call that switched made two model calls; later ones, one.
		if want := map[bool]int{true: 2, false: 1}[i == 0]; CallsOf(resp) != want {
			t.Errorf("call %d: CallsOf = %d, want %d", i, CallsOf(resp), want)
		}
	}
	if primary.calls.Load() != 1 || fb.calls.Load() != 3 || created.Load() != 1 {
		t.Errorf("primary called %d, fallback %d, created %d; want 1, 3, 1", primary.calls.Load(), fb.calls.Load(), created.Load())
	}
	label, reason, used := f.FellBack()
	if !used || label != "ollama:llama" || !strings.Contains(reason, "openai:gpt failed") || !strings.Contains(reason, "invalid key") {
		t.Errorf("FellBack = %q, %q, %v", label, reason, used)
	}
}

// Errors another provider would not get past do not switch.
func TestFallback_NotFor(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"a refused request", context.Background(), &requestError{status: 400, body: "prompt too long"}},
		{"an oversized request", context.Background(), &requestError{status: 413, body: "too large"}},
		{"a cancelled review", cancelled, context.Canceled},
	} {
		primary := &stubReviewer{name: "openai", err: tc.err}
		create, created := fallbackTo(t, &stubReviewer{name: "ollama", content: "[]"}, nil)
		f := NewFallback(primary, nil, "openai:gpt", "ollama:llama", create)
		if _, err := f.Review(tc.ctx, ReviewRequest{}); !errors.Is(err, tc.err) {
			t.Errorf("%s: err = %v, want the primary's", tc.name, err)
		}
		if _, _, used := f.FellBack(); used || created.Load() != 0 {
			t.Errorf("%s: fell back", tc.name)
		}
	}
}

// Errors another provider can get past do switch: exhausted retries, a
// transport failure, a missing model.
func TestFallback_For(t *testing.T) {
	for name, err := range map[string]error{
		"rate limited":  &rateLimitError{},
		"server error":  &serverError{statusCode: 503},
		"unreachable":   errors.New("sending request: connection refused"),
		"missing model": &requestError{status: 404, body: "model not found"},
	} {
		f := NewFallback(&stubReviewer{name: "openai", err: err}, nil, "openai:gpt", "ollama:llama",
			func() (Reviewer, error) { return &stubReviewer{name: "ollama", content: "[]"}, nil })
		if resp, rerr := f.Review(context.Background(), ReviewRequest{}); rerr != nil || !resp.Fallback {
			t.Errorf("%s: Review = %+v, %v; want the fallback's answer", name, resp, rerr)
		}
	}
}

// A primary that could not be created (a missing API key) is skipped: the
// fallback reviews from the first call.
func TestFallback_PrimaryUnavailable(t *testing.T) {
	fb := &stubReviewer{name: "ollama", content: "[]"}
	f := NewFallback(nil, errors.New("OPENAI_API_KEY environment variable is not set"), "openai:gpt", "ollama:llama",
		func() (Reviewer, error) { return fb, nil })
	resp, err := f.Review(context.Background(), ReviewRequest{})
	if err != nil || !resp.Fallback {
		t.Fatalf("Review = %+v, %v", resp, err)
	}
	if _, reason, used := f.FellBack(); !used || !strings.Contains(reason, "unavailable") || !strings.Contains(reason, "OPENAI_API_KEY") {
		t.Errorf("FellBack reason = %q, %v", reason, used)
	}
	if f.Name() != "ollama" {
		t.Errorf("Name = %q, want the fallback's when there is no primary", f.Name())
	}
}

// When the fallback cannot help either, the error names both failures, and
// an auth failure is still recognised as one (exit 3).
func TestFallback_BothFail(t *testing.T) {
	primaryErr := &authError{message: "invalid key"}
	f := NewFallback(&stubReviewer{name: "openai", err: primaryErr}, nil, "openai:gpt", "ollama:llama",
		func() (Reviewer, error) {
			return &stubReviewer{name: "ollama", err: errors.New("connection refused")}, nil
		})
	_, err := f.Review(context.Background(), ReviewRequest{})
	if err == nil || !strings.Contains(err.Error(), "fallback ollama:llama") || !strings.Contains(err.Error(), "connection refused") || !IsAuthError(err) {
		t.Errorf("err = %v, want both failures, still an auth error", err)
	}

	noFallback := NewFallback(&stubReviewer{name: "openai", err: primaryErr}, nil, "openai:gpt", "ollama:llama",
		func() (Reviewer, error) { return nil, errors.New("unknown provider: olama") })
	if _, err := noFallback.Review(context.Background(), ReviewRequest{}); err == nil || !strings.Contains(err.Error(), "could not be created") {
		t.Errorf("err = %v, want the fallback creation failure", err)
	}
}

// A reason is one line of bounded length, whatever the error body held.
func TestBrief(t *testing.T) {
	if got := brief("API error (status 404): {\n    \"error\": \"no\"\n}"); got != `API error (status 404): { "error": "no" }` {
		t.Errorf("brief = %q", got)
	}
	if got := brief(strings.Repeat("x", 500)); len(got) > maxReason+len("…") {
		t.Errorf("brief length = %d, want at most %d", len(got), maxReason)
	}
}
