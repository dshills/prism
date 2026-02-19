// Package github provides a minimal GitHub REST API client for posting prism
// findings as pull-request review comments.
//
// It detects the current repository and PR number from the local git remote
// and the GITHUB_TOKEN environment variable. Comments are posted in GitHub's
// standard PR review format with file path and line number annotations.
package github
