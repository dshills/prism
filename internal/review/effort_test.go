package review

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

// effortReviewer records the effort each request asked for.
type effortReviewer struct {
	mu      sync.Mutex
	efforts []string
}

func (r *effortReviewer) Review(_ context.Context, req providers.ReviewRequest) (providers.ReviewResponse, error) {
	r.mu.Lock()
	r.efforts = append(r.efforts, req.Effort)
	r.mu.Unlock()
	return providers.ReviewResponse{Content: "[]", Provider: "mock", Model: "m"}, nil
}

func (r *effortReviewer) Name() string { return "mock" }

// The configured effort reaches every request, and is part of the cache key:
// a review at one effort is not replayed as a review at another.
func TestReasoningEffort_SentAndKeyed(t *testing.T) {
	rev := &effortReviewer{}
	useProvider(t, rev)
	cfg := chunkTestConfig(t)
	cfg.ReasoningEffort = "low"
	ctx := context.Background()

	if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err != nil {
		t.Fatal(err)
	}
	if len(rev.efforts) != 3 || rev.efforts[0] != "low" || rev.efforts[2] != "low" {
		t.Fatalf("efforts sent = %v, want low for each of 3 chunks", rev.efforts)
	}
	cfg.ReasoningEffort = "high"
	if _, err := Run(ctx, threeChunkDiff("beta"), cfg); err != nil {
		t.Fatal(err)
	}
	if len(rev.efforts) != 6 || rev.efforts[5] != "high" {
		t.Errorf("a new effort replayed the cache: efforts sent = %v", rev.efforts)
	}
}

// Leaving the effort unset keeps the cache keys from before it existed.
func TestReasoningEffort_UnsetKeepsFingerprint(t *testing.T) {
	cfg := config.Default()
	base := promptFingerprint(defaultPromptBuilder, cfg, nil)
	sys, user := defaultPromptBuilder("", guideProbeFiles(), cfg, nil)
	if base != fingerprintOf(sys, user) {
		t.Error("unset effort changed the prompt fingerprint")
	}
	cfg.ReasoningEffort = "medium"
	if promptFingerprint(defaultPromptBuilder, cfg, nil) == base {
		t.Error("effort is not in the prompt fingerprint")
	}
}

// fingerprintOf is promptFingerprint's hash as it was before effort.
func fingerprintOf(sys, user string) string {
	h := sha256.Sum256([]byte(sys + "\x00" + user))
	return fmt.Sprintf("%x", h[:16])
}
