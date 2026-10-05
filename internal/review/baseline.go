package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dshills/prism/internal/config"
)

// DefaultBaselineFile is the baseline's name at the repository root.
const DefaultBaselineFile = ".prism-baseline.json"

// baselineVersion is the version written to new baseline files.
const baselineVersion = 1

// Baseline is a repository's accepted findings: a source-controlled file that
// every review respects, so an agent never sees a finding a person has already
// decided about. Findings match by ID, the stable fingerprint of their code
// (fingerprint.go).
type Baseline struct {
	Version  int             `json:"version"`
	Findings []BaselineEntry `json:"findings"`
}

// BaselineEntry is one accepted finding. Only ID is matched; the rest says
// what was accepted and why, for whoever reads the file.
type BaselineEntry struct {
	ID       string `json:"id"`
	Path     string `json:"path,omitempty"`
	Category string `json:"category,omitempty"`
	Title    string `json:"title,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Added is the date the entry was added, YYYY-MM-DD.
	Added string `json:"added,omitempty"`
}

// Suppression is a finding left out of a report because it was accepted.
type Suppression struct {
	Finding Finding `json:"finding"`
	// Source is SuppressedByBaseline or SuppressedInline.
	Source string `json:"source"`
	Reason string `json:"reason,omitempty"`
}

// Where a suppression came from.
const (
	SuppressedByBaseline = "baseline"
	SuppressedInline     = "inline"
)

// BaselinePath is where cfg's baseline lives for the repository at root: the
// configured file, relative to root unless absolute, or "" when the baseline
// is turned off. With no root (a GitHub PR review), a relative file is taken
// from the working directory.
func BaselinePath(cfg config.Config, root string) string {
	name := cfg.BaselineFile
	switch {
	case strings.EqualFold(name, "none"):
		return ""
	case name == "":
		name = DefaultBaselineFile
	}
	if filepath.IsAbs(name) || root == "" {
		return name
	}
	return filepath.Join(root, name)
}

// LoadBaseline reads the baseline at path. A missing file is an empty
// baseline. A file that cannot be read or parsed is an error rather than an
// empty baseline: silently dropping every accepted finding would send agents
// back to work they were told to leave alone.
func LoadBaseline(path string) (*Baseline, error) {
	b := &Baseline{Version: baselineVersion}
	if path == "" {
		return b, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading baseline: %w", err)
	}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("parsing baseline %s: %w", path, err)
	}
	return b, nil
}

// Save writes the baseline to path, sorted by path then ID so that changes
// to the file diff cleanly in review. It writes a temporary file beside path
// and renames it into place, so a failed write leaves the old baseline whole.
func (b *Baseline) Save(path string) error {
	if b.Version == 0 {
		b.Version = baselineVersion
	}
	if b.Findings == nil {
		b.Findings = []BaselineEntry{}
	}
	slices.SortStableFunc(b.Findings, func(x, y BaselineEntry) int {
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic replaces path with data, or leaves it as it was.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Has reports whether the baseline accepts the finding with this ID.
func (b *Baseline) Has(id string) bool {
	return b.entry(id) != nil
}

func (b *Baseline) entry(id string) *BaselineEntry {
	for i := range b.Findings {
		if b.Findings[i].ID == id {
			return &b.Findings[i]
		}
	}
	return nil
}

// Add accepts e, replacing any entry with the same ID. It reports whether the
// ID was new.
func (b *Baseline) Add(e BaselineEntry) bool {
	if old := b.entry(e.ID); old != nil {
		*old = e
		return false
	}
	b.Findings = append(b.Findings, e)
	return true
}

// Remove drops the entry with this ID, reporting whether there was one.
func (b *Baseline) Remove(id string) bool {
	for i := range b.Findings {
		if b.Findings[i].ID == id {
			b.Findings = slices.Delete(b.Findings, i, i+1)
			return true
		}
	}
	return false
}

// EntryFor is the baseline entry that records f.
func EntryFor(f Finding, reason, added string) BaselineEntry {
	return BaselineEntry{
		ID:       f.ID,
		Path:     findingPath(f),
		Category: string(f.Category),
		Title:    f.Title,
		Reason:   reason,
		Added:    added,
	}
}

// applyBaseline splits findings into those the baseline has not accepted and
// those it has.
func applyBaseline(findings []Finding, b *Baseline) (kept []Finding, suppressed []Suppression) {
	if b == nil || len(b.Findings) == 0 {
		return findings, nil
	}
	kept = make([]Finding, 0, len(findings))
	for _, f := range findings {
		if e := b.entry(f.ID); e != nil {
			suppressed = append(suppressed, Suppression{Finding: f, Source: SuppressedByBaseline, Reason: e.Reason})
			continue
		}
		kept = append(kept, f)
	}
	return kept, suppressed
}

// StampSuppressedCommit sets commit on each suppressed finding's locations,
// as the per-commit range review does for reported ones.
func StampSuppressedCommit(ss []Suppression, commit string) []Suppression {
	out := make([]Suppression, len(ss))
	for i, s := range ss {
		s.Finding.Locations = slices.Clone(s.Finding.Locations)
		for j := range s.Finding.Locations {
			s.Finding.Locations[j].Commit = commit
		}
		out[i] = s
	}
	return out
}
