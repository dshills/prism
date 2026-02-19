// Package gitctx extracts diffs and commit metadata from a git repository.
//
// It supports all five prism review modes — unstaged, staged, commit, range,
// and snippet — by shelling out to git with appropriate arguments. Results are
// filtered by include/exclude glob patterns and truncated to a configurable
// maximum byte size.
//
// [ListCommits] returns the ordered list of commits in a revision range for
// use with per-commit review mode.
package gitctx
