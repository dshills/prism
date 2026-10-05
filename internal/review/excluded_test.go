package review

import (
	"context"
	"strings"
	"testing"

	"github.com/dshills/prism/internal/gitctx"
)

// Files prism's rules left out are listed, but are policy, not a gap: they
// do not make the review incomplete.
func TestCoverage_Excluded(t *testing.T) {
	c := NewCoverage(ConfigReviewer("p", "m"), 1, 10, 0)
	for i := range 7 {
		c.Excluded = append(c.Excluded, gitctx.Excluded{Path: "gen" + string(rune('a'+i)) + ".pb.go", Reason: gitctx.ReasonGenerated})
	}
	c.Finalize()
	if c.Incomplete() || !c.Complete {
		t.Error("excluded files must not make the review incomplete")
	}
	line := c.ExcludedLine()
	if !strings.HasPrefix(line, "Excluded 7 files not worth reviewing: gena.pb.go (generated code)") || !strings.HasSuffix(line, "and 2 more") {
		t.Errorf("ExcludedLine = %q", line)
	}

	var sum Coverage
	sum.Add(c, true)
	sum.Add(c, false)
	if len(sum.Excluded) != 14 {
		t.Errorf("per-commit sum has %d excluded, want 14", len(sum.Excluded))
	}

	empty := NewCoverage(ConfigReviewer("p", "m"), 0, 0, 0)
	empty.Finalize()
	if empty.ExcludedLine() != "" || empty.Excluded == nil {
		t.Error("no exclusions: no line, and an empty (not null) list for JSON")
	}
}

// The report's coverage carries what the diff left out.
func TestRun_CoverageCarriesExcluded(t *testing.T) {
	useProvider(t, &fileReviewer{})
	diff := threeChunkDiff("beta")
	diff.Excluded = []gitctx.Excluded{{Path: "go.sum", Reason: gitctx.ReasonLockfile}}
	report, err := Run(context.Background(), diff, chunkTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if ex := report.Coverage.Excluded; len(ex) != 1 || ex[0].Path != "go.sum" {
		t.Errorf("coverage.excluded = %v", ex)
	}
}
