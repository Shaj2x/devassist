package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// makeRepo returns a file:// URL to a repo with two commits on main.
func makeRepo(t *testing.T) (url, first, second string) {
	t.Helper()
	src := t.TempDir()
	git(t, src, "init", "--quiet", "-b", "main")
	_ = os.WriteFile(filepath.Join(src, "a.py"), []byte("v1\n"), 0o600)
	git(t, src, "add", ".")
	git(t, src, "commit", "--quiet", "-m", "one")
	first = git(t, src, "rev-parse", "HEAD")
	_ = os.WriteFile(filepath.Join(src, "a.py"), []byte("v2\n"), 0o600)
	git(t, src, "commit", "--quiet", "-am", "two")
	second = git(t, src, "rev-parse", "HEAD")
	return "file://" + src, first, second
}

func TestCloneBranchHead(t *testing.T) {
	url, _, second := makeRepo(t)
	co, err := Clone(context.Background(), url, filepath.Join(t.TempDir(), "co"), Options{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if co.CommitSHA != second {
		t.Errorf("sha = %s, want %s", co.CommitSHA, second)
	}
	content, _ := os.ReadFile(filepath.Join(co.Dir, "a.py"))
	if string(content) != "v2\n" {
		t.Errorf("content = %q", content)
	}
}

func TestCloneSpecificCommit(t *testing.T) {
	url, first, _ := makeRepo(t)
	co, err := Clone(context.Background(), url, filepath.Join(t.TempDir(), "co"), Options{CommitSHA: first})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(co.Dir, "a.py"))
	if co.CommitSHA != first || string(content) != "v1\n" {
		t.Errorf("sha=%s content=%q", co.CommitSHA, content)
	}
}

func TestCloneRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, err := Clone(context.Background(), "file:///x", dir, Options{CommitSHA: "main; rm -rf /"}); err == nil {
		t.Error("expected invalid sha error")
	}
	if _, err := Clone(context.Background(), "--upload-pack=evil", dir, Options{}); err == nil {
		t.Error("expected invalid url error")
	}
	if _, err := Clone(context.Background(), "file:///does/not/exist", filepath.Join(dir, "x"), Options{}); err == nil {
		t.Error("expected fetch error")
	}
}

func TestApply(t *testing.T) {
	url, _, _ := makeRepo(t)
	co, err := Clone(context.Background(), url, filepath.Join(t.TempDir(), "co"), Options{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	good := "--- a/a.py\n+++ b/a.py\n@@ -1 +1,2 @@\n-v2\n+v3\n+extra\n"
	res, err := Apply(context.Background(), co.Dir, good)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ChangedFiles) != 1 || res.ChangedFiles[0] != "a.py" || res.Additions != 2 || res.Deletions != 1 {
		t.Errorf("result = %+v", res)
	}
	content, _ := os.ReadFile(filepath.Join(co.Dir, "a.py"))
	if string(content) != "v3\nextra\n" {
		t.Errorf("content = %q", content)
	}

	stale := "--- a/a.py\n+++ b/a.py\n@@ -1 +1 @@\n-v1\n+v9\n" // context no longer matches
	if _, err := Apply(context.Background(), co.Dir, stale); err == nil || !strings.Contains(err.Error(), "patch does not apply") {
		t.Errorf("stale patch: %v", err)
	}
	if _, err := Apply(context.Background(), co.Dir, "  "); err == nil {
		t.Error("empty patch should fail")
	}
}
