// Package gitrepo checks out a repository at a specific commit and applies
// patches, using the git CLI (the same tool developers use, so behaviour is
// unsurprising). Shared by the indexer and the sandbox runner.
package gitrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Checkout describes a finished checkout.
type Checkout struct {
	Dir       string
	CommitSHA string
}

// Options for Clone.
type Options struct {
	Branch    string // used when CommitSHA is empty
	CommitSHA string // exact commit to check out
	// GitHubToken authenticates HTTPS clones from github.com. It is passed as
	// an HTTP header via config, never embedded in the URL or logged.
	GitHubToken string
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// Clone fetches url into dir (which must not exist or be empty) with a
// depth-1 fetch: we only ever need one commit's tree.
func Clone(ctx context.Context, url, dir string, opts Options) (Checkout, error) {
	if opts.CommitSHA != "" && !shaPattern.MatchString(opts.CommitSHA) {
		return Checkout{}, fmt.Errorf("invalid commit sha %q", opts.CommitSHA)
	}
	if strings.HasPrefix(url, "-") {
		return Checkout{}, fmt.Errorf("invalid repository url %q", url)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Checkout{}, err
	}

	ref := opts.CommitSHA
	if ref == "" {
		ref = opts.Branch
		if ref == "" {
			ref = "HEAD"
		}
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", url},
		{"fetch", "--quiet", "--depth", "1", "--no-tags", "origin", ref},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := run(ctx, dir, url, opts.GitHubToken, args...); err != nil {
			return Checkout{}, err
		}
	}
	sha, err := run(ctx, dir, url, "", "rev-parse", "HEAD")
	if err != nil {
		return Checkout{}, err
	}
	return Checkout{Dir: dir, CommitSHA: strings.TrimSpace(sha)}, nil
}

// ApplyResult describes a patch applied with Apply.
type ApplyResult struct {
	ChangedFiles []string
	Additions    int
	Deletions    int
}

// Apply checks and applies a unified diff to the checkout in dir. A diff that
// does not apply cleanly returns an error describing why, and leaves the
// working tree untouched (git apply is atomic).
func Apply(ctx context.Context, dir, diff string) (ApplyResult, error) {
	if strings.TrimSpace(diff) == "" {
		return ApplyResult{}, fmt.Errorf("patch is empty")
	}
	stat, err := runInput(ctx, dir, diff, "apply", "--numstat", "--check", "-")
	if err != nil {
		return ApplyResult{}, err
	}
	// --check with --numstat validates without touching files and prints stats.
	if _, err := runInput(ctx, dir, diff, "apply", "--whitespace=nowarn", "-"); err != nil {
		return ApplyResult{}, err
	}
	var res ApplyResult
	for _, line := range strings.Split(strings.TrimSpace(stat), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		add, _ := strconv.Atoi(fields[0]) // "-" for binary files counts as 0
		del, _ := strconv.Atoi(fields[1])
		res.Additions += add
		res.Deletions += del
		res.ChangedFiles = append(res.ChangedFiles, fields[2])
	}
	return res, nil
}

func runInput(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed binary, no shell
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func run(ctx context.Context, dir, url, token string, args ...string) (string, error) {
	var full []string
	if token != "" && strings.HasPrefix(url, "https://github.com/") {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		full = append(full, "-c", "http.https://github.com/.extraheader=Authorization: Basic "+basic)
	}
	full = append(full, args...)
	// No shell is involved, the binary is fixed, and Clone validates the
	// only caller-controlled values (URL cannot start with "-", SHA is hex).
	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec
	cmd.Dir = dir
	// Never prompt for credentials; fail fast instead.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// args[0] only: never echo the auth header into errors or logs.
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
