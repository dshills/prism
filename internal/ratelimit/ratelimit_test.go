package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestNew_Unlimited(t *testing.T) {
	l0 := New(0)
	if l0 == nil {
		t.Fatal("New(0) should return a non-nil unlimited Limiter")
	}
	if err := l0.Wait(context.Background()); err != nil {
		t.Fatalf("unlimited limiter Wait returned error: %v", err)
	}
	l1 := New(-1)
	if l1 == nil {
		t.Fatal("New(-1) should return a non-nil unlimited Limiter")
	}
}

func TestWait_NilReceiver(t *testing.T) {
	var l *Limiter
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("nil receiver Wait returned error: %v", err)
	}
}

func TestWait_BurstImmediate(t *testing.T) {
	// 600 RPM = 10 per second; burst pre-loads 10 tokens, so first 10 calls
	// should be immediate.
	l := New(600)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 10; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("burst Wait %d returned error: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("burst took %v, expected < 50ms", elapsed)
	}
}

func TestWait_Throttled(t *testing.T) {
	// 60 RPM = 1 per second; after burst of 1 (min(60,10)=10 but rate is slow).
	// Use very low RPM to make throttling observable in a short test.
	// 12 RPM = 5s per token; burst=10. After 10 immediate, 11th should wait ~5s.
	// That's too slow for a unit test. Use 120 RPM = 0.5s per token; burst=10.
	// After 10 immediate, 11th should wait ~0.5s.
	l := New(120)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_ = l.Wait(ctx)
	}
	start := time.Now()
	_ = l.Wait(ctx)
	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Errorf("11th token came in %v, expected ≥400ms (120 RPM = 500ms/token)", elapsed)
	}
}

func TestWait_ContextCancelled(t *testing.T) {
	// 1 RPM = 60s per token; drain burst then cancel.
	l := New(1)
	ctx, cancel := context.WithCancel(context.Background())

	// Drain the single burst token.
	_ = l.Wait(ctx)

	// Next Wait should block; cancel it quickly.
	cancel()
	start := time.Now()
	err := l.Wait(ctx)
	if err == nil {
		t.Fatal("expected error after cancel, got nil")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("cancel took %v, expected < 200ms", elapsed)
	}
}
