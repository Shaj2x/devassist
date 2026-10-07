//go:build integration

// Integration tests against a real Docker daemon and the sandbox images
// (make sandbox-images). They attack the sandbox: each escape attempt is a
// test that passes only if the escape works, so a secure sandbox makes every
// one of them fail.
//
//	go test -tags integration ./internal/validate
package validate

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/sandbox"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

func gitRepo(t *testing.T, files map[string]string) (url, sha string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return "file://" + dir, run("rev-parse", "HEAD")
}

func realValidator(t *testing.T, limits sandbox.Limits, timeout time.Duration) *Validator {
	t.Helper()
	d, err := sandbox.NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if ok, _ := d.ImageExists(context.Background(), toolchain.DefaultImages["python"]); !ok {
		t.Skip("sandbox-python image missing; run make sandbox-images")
	}
	return New(FromDocker(d), Options{
		Images: toolchain.DefaultImages, Limits: limits, WorkDir: t.TempDir(),
		DefaultTimeout: timeout, StepTimeout: timeout,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func sandboxCount(t *testing.T) int {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	list, err := cli.ContainerList(context.Background(), container.ListOptions{
		All: true, Filters: filters.NewArgs(filters.Arg("label", sandbox.Label+"=true"))})
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

var baseRepo = map[string]string{
	"pyproject.toml":    "[project]\nname = \"victim\"\n\n[tool.pytest.ini_options]\npythonpath = [\".\"]\n",
	"app.py":            "def add(a, b):\n    return a + b\n",
	"tests/test_app.py": "from app import add\n\n\ndef test_add():\n    assert add(2, 2) == 4\n",
}

// escapeTests: each test PASSES only if the sandbox is broken.
const escapeTests = `--- /dev/null
+++ b/tests/test_escape.py
@@ -0,0 +1,41 @@
+import os
+import socket
+import subprocess
+import sys
+
+
+def test_reach_the_internet():
+    socket.create_connection(("1.1.1.1", 53), timeout=3)
+
+
+def test_resolve_dns():
+    socket.getaddrinfo("example.com", 80)
+
+
+def test_write_outside_workspace():
+    with open("/usr/local/pwned", "w") as f:
+        f.write("x")
+
+
+def test_run_as_root():
+    assert os.getuid() == 0
+
+
+def test_read_host_docker_socket():
+    assert os.path.exists("/var/run/docker.sock")
+
+
+def test_allocate_more_than_memory_limit():
+    # In a child process, so the OOM kill does not take pytest with it.
+    hog = "b = bytearray(600 * 1024 * 1024); b[::4096] = b'x' * len(b[::4096])"
+    assert subprocess.run([sys.executable, "-c", hog]).returncode == 0
+
+
+def test_fork_unbounded():
+    pids = []
+    for _ in range(400):
+        pid = os.fork()
+        if pid == 0:
+            os._exit(0)
+        pids.append(pid)
+    assert len(pids) == 400
`

func TestEscapeAttemptsAllFail(t *testing.T) {
	url, sha := gitRepo(t, baseRepo)
	limits := sandbox.Limits{MemoryBytes: 256 << 20, NanoCPUs: 1e9, PIDs: 128, TmpfsBytes: 128 << 20}
	v := realValidator(t, limits, 2*time.Minute)
	before := sandboxCount(t)

	res := v.Validate(context.Background(), Request{JobID: "it", PatchID: "escape", Iteration: 1,
		CloneURL: url, CommitSHA: sha, Diff: escapeTests})

	if res.Tests.Status != "failed" || res.Tests.Passed != 1 {
		t.Fatalf("tests = %s %s (error=%s)", res.Tests.Status, deref(res.Tests.Summary), deref(res.Error))
	}
	var failed []string
	for _, f := range res.Tests.Findings {
		failed = append(failed, strings.TrimPrefix(*f.RuleID, "tests.test_escape::"))
	}
	for _, escape := range []string{
		"test_reach_the_internet", "test_resolve_dns", "test_write_outside_workspace",
		"test_run_as_root", "test_read_host_docker_socket", "test_allocate_more_than_memory_limit",
		"test_fork_unbounded",
	} {
		if !slices.Contains(failed, escape) {
			t.Errorf("escape attempt %s SUCCEEDED; failed tests were %v", escape, failed)
		}
	}
	if after := sandboxCount(t); after != before {
		t.Errorf("sandbox not cleaned up: %d -> %d containers", before, after)
	}
}

func TestInfiniteLoopTimesOutAndIsCleanedUp(t *testing.T) {
	url, sha := gitRepo(t, baseRepo)
	v := realValidator(t, sandbox.DefaultLimits, 15*time.Second)
	before := sandboxCount(t)
	hang := "--- /dev/null\n+++ b/tests/test_hang.py\n@@ -0,0 +1,3 @@\n+def test_hang():\n+    while True:\n+        pass\n"

	start := time.Now()
	res := v.Validate(context.Background(), Request{JobID: "it", PatchID: "hang", Iteration: 1,
		CloneURL: url, CommitSHA: sha, Diff: hang})

	if res.Status != "timeout" {
		t.Fatalf("status = %s, tests = %+v", res.Status, res.Tests)
	}
	if took := time.Since(start); took > 45*time.Second {
		t.Errorf("timeout took %s to take effect", took)
	}
	if after := sandboxCount(t); after != before {
		t.Errorf("timed-out sandbox not removed: %d -> %d", before, after)
	}
}

func TestGoRepository(t *testing.T) {
	d, _ := sandbox.NewDocker()
	if ok, _ := d.ImageExists(context.Background(), toolchain.DefaultImages["go"]); !ok {
		t.Skip("sandbox-go image missing")
	}
	url, sha := gitRepo(t, map[string]string{
		"go.mod":    "module example.com/m\n\ngo 1.24\n",
		"m.go":      "package m\n\nfunc Add(a, b int) int { return a + b }\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	})
	v := realValidator(t, sandbox.DefaultLimits, 3*time.Minute)
	broken := "--- a/m.go\n+++ b/m.go\n@@ -1,3 +1,3 @@\n package m\n \n-func Add(a, b int) int { return a + b }\n+func Add(a, b int) int { return a - b }\n"

	res := v.Validate(context.Background(), Request{JobID: "it", PatchID: "go", Iteration: 1,
		CloneURL: url, CommitSHA: sha, Diff: broken})
	if res.Tests.Status != "failed" || res.Tests.Failed != 1 || res.StaticAnalysis.Status != "passed" {
		t.Fatalf("result: status=%s tests=%+v static=%+v err=%v", res.Status, res.Tests, res.StaticAnalysis, res.Error)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
