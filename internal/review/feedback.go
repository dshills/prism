package review

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dshills/prism/internal/cache"
	"github.com/dshills/prism/internal/config"
)

// Verdicts an agent records on a finding after deciding what to do with it.
const (
	VerdictConfirmed = "confirmed" // a real problem, acted on
	VerdictDismissed = "dismissed" // a false positive
)

// minCalibrationSamples is how many judged findings a category needs before
// its rate is reported: fewer say more about chance than about the model.
const minCalibrationSamples = 5

// FeedbackEntry is one verdict on a finding, appended to the feedback log.
type FeedbackEntry struct {
	Time       string  `json:"time"`
	Repo       string  `json:"repo,omitempty"`
	ID         string  `json:"id"`
	Verdict    string  `json:"verdict"`
	Category   string  `json:"category"`
	Severity   string  `json:"severity,omitempty"`
	Provider   string  `json:"provider,omitempty"`
	Model      string  `json:"model,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

// Calibration is how often findings like this one were confirmed when an
// agent judged them: the model's track record, not its own confidence.
type Calibration struct {
	// ConfirmRate is confirmed / (confirmed + dismissed).
	ConfirmRate float64 `json:"confirmRate"`
	Samples     int     `json:"samples"`
	// Scope is what the rate is over: "model" (this provider, model and
	// category) when it has enough samples, else "category" (every model).
	Scope string `json:"scope"`
}

// FeedbackPath is the feedback log, next to the review cache. It is the
// user's, across repositories: a model's track record in a category says
// something everywhere it reviews.
func FeedbackPath(cfg config.Config) (string, error) {
	dir, err := cache.ResolveDir(cfg.Cache.Dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "feedback.jsonl"), nil
}

// AppendFeedback adds entries to the log at path, creating it readable only
// by the user.
func AppendFeedback(path string, entries []FeedbackEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			_ = f.Close()
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// LoadFeedback reads the log at path. A missing log is empty; a line that
// does not parse is skipped, so one bad write does not lose the rest.
func LoadFeedback(path string) ([]FeedbackEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []FeedbackEntry
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n') // any length: a long line is skipped, not fatal
		var e FeedbackEntry
		if len(line) > 0 && json.Unmarshal(line, &e) == nil && e.ID != "" &&
			(e.Verdict == VerdictConfirmed || e.Verdict == VerdictDismissed) {
			out = append(out, e)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// tally is confirmed and dismissed counts.
type tally struct{ confirmed, dismissed int }

func (t tally) samples() int { return t.confirmed + t.dismissed }

// FeedbackStats is the log's verdicts counted by category, and by provider,
// model and category. A finding judged more than once counts once, by its
// latest verdict.
type FeedbackStats struct {
	byCategory map[string]tally
	byModel    map[string]tally
}

// NewFeedbackStats counts entries.
func NewFeedbackStats(entries []FeedbackEntry) FeedbackStats {
	latest := map[string]FeedbackEntry{}
	for _, e := range entries { // in log order, so a later verdict wins
		latest[e.Repo+"\x00"+e.ID] = e
	}
	s := FeedbackStats{byCategory: map[string]tally{}, byModel: map[string]tally{}}
	for _, e := range latest {
		count := func(m map[string]tally, key string) {
			t := m[key]
			if e.Verdict == VerdictConfirmed {
				t.confirmed++
			} else {
				t.dismissed++
			}
			m[key] = t
		}
		count(s.byCategory, e.Category)
		if e.Provider != "" && e.Model != "" {
			count(s.byModel, modelKey(e.Provider, e.Model, e.Category))
		}
	}
	return s
}

func modelKey(provider, model, category string) string {
	return provider + ":" + model + "\x00" + category
}

// For is the calibration for a finding: its model's rate in its category
// when that has enough samples, else every model's rate in the category, or
// nil when neither has.
func (s FeedbackStats) For(f Finding) *Calibration {
	if t, ok := s.byModel[modelKey(f.Provider, f.Model, string(f.Category))]; ok && t.samples() >= minCalibrationSamples {
		return &Calibration{ConfirmRate: rate(t), Samples: t.samples(), Scope: "model"}
	}
	if t, ok := s.byCategory[string(f.Category)]; ok && t.samples() >= minCalibrationSamples {
		return &Calibration{ConfirmRate: rate(t), Samples: t.samples(), Scope: "category"}
	}
	return nil
}

func rate(t tally) float64 {
	return float64(int(float64(t.confirmed)/float64(t.samples())*1000+0.5)) / 1000
}

// CategoryLines describes the rates by category, most samples first, for
// `prism findings stats`.
func (s FeedbackStats) CategoryLines() []string {
	cats := make([]string, 0, len(s.byCategory))
	for c := range s.byCategory {
		cats = append(cats, c)
	}
	slices.SortFunc(cats, func(a, b string) int {
		if d := s.byCategory[b].samples() - s.byCategory[a].samples(); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	lines := make([]string, 0, len(cats))
	for _, c := range cats {
		t := s.byCategory[c]
		lines = append(lines, fmt.Sprintf("%-16s %3.0f%% confirmed (%d confirmed, %d dismissed)", c, rate(t)*100, t.confirmed, t.dismissed))
	}
	return lines
}

// calibrate sets each finding's calibration from the feedback log. It is
// best effort: without a readable log, findings carry none.
func calibrate(findings []Finding, cfg config.Config) {
	if len(findings) == 0 {
		return
	}
	path, err := FeedbackPath(cfg)
	if err != nil {
		return
	}
	entries, err := LoadFeedback(path)
	if err != nil || len(entries) == 0 {
		return
	}
	stats := NewFeedbackStats(entries)
	for i := range findings {
		findings[i].Calibration = stats.For(findings[i])
	}
}

// maxReasonLen caps a verdict's reason in the log.
const maxReasonLen = 1000

// FeedbackFor is the log entry recording verdict on f.
func FeedbackFor(f Finding, verdict, reason, repo, now string) FeedbackEntry {
	if len(reason) > maxReasonLen {
		reason = strings.ToValidUTF8(reason[:maxReasonLen], "")
	}
	return FeedbackEntry{
		Time: now, Repo: repo, ID: f.ID, Verdict: verdict,
		Category: string(f.Category), Severity: string(f.Severity),
		Provider: f.Provider, Model: f.Model, Confidence: f.Confidence, Reason: reason,
	}
}
