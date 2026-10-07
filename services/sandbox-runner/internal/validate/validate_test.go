package validate

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/sandbox"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
)

// fakeBox answers commands by their tool name and records the call order.
type fakeBox struct {
	mu        sync.Mutex
	calls     []string
	answers   map[string]sandbox.ExecResult
	report    string
	removed   bool
	connected bool
}

func (b *fakeBox) Exec(_ context.Context, cmd []string, _ time.Duration, _ io.Reader) (sandbox.ExecResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tool := cmd[0]
	if tool == "python" {
		tool = "pytest"
	}
	if tool == "pip" && !b.connected {
		return sandbox.ExecResult{ExitCode: 1, Stderr: "network unreachable"}, nil
	}
	b.calls = append(b.calls, tool)
	return b.answers[tool], nil
}

func (b *fakeBox) CopyDir(context.Context, string) error { return nil }
func (b *fakeBox) ReadFile(context.Context, string) (string, error) {
	return b.report, nil
}
func (b *fakeBox) DisconnectNetwork(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connected = false
	b.calls = append(b.calls, "DISCONNECT")
	return nil
}
func (b *fakeBox) Remove(context.Context) error { b.removed = true; return nil }

type fakeSandbox struct {
	box     *fakeBox
	started bool
	spec    sandbox.Spec
}

func (s *fakeSandbox) Start(_ context.Context, spec sandbox.Spec) (Box, error) {
	s.started, s.spec = true, spec
	s.box.connected = spec.Network != sandbox.NetworkNone
	return s.box, nil
}
func (s *fakeSandbox) ImageExists(context.Context, string) (bool, error) { return true, nil }

const passingJUnit = `<testsuites><testsuite><testcase classname="tests.test_a" name="test_ok"/></testsuite></testsuites>`

func cleanAnswers() map[string]sandbox.ExecResult {
	return map[string]sandbox.ExecResult{
		"pytest": {ExitCode: 0},
		"bandit": {ExitCode: 0, Stdout: `{"results":[]}`},
		"ruff":   {ExitCode: 0, Stdout: `[]`},
		"pip":    {ExitCode: 0},
	}
}

// pythonRepo creates a git repo with one module and returns its file:// URL.
func pythonRepo(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"pyproject.toml": "[project]\nname='x'\n", "app.py": "def f():\n    return 1\n"}
	for k, v := range extra {
		files[k] = v
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return "file://" + dir
}

const goodDiff = "--- a/app.py\n+++ b/app.py\n@@ -1,2 +1,2 @@\n def f():\n-    return 1\n+    return 2\n"

func newValidator(t *testing.T, sb Sandbox, egress string) *Validator {
	t.Helper()
	return New(sb, Options{
		Images: toolchain.DefaultImages, Limits: sandbox.DefaultLimits, EgressNetwork: egress,
		WorkDir: t.TempDir(), DefaultTimeout: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPassingPatch(t *testing.T) {
	box := &fakeBox{answers: cleanAnswers(), report: passingJUnit}
	sb := &fakeSandbox{box: box}
	res := newValidator(t, sb, "egress").Validate(context.Background(), Request{
		JobID: "j", PatchID: "p", Iteration: 1, CloneURL: pythonRepo(t, nil), Diff: goodDiff,
	})
	if res.Status != "passed" || res.Tests.Status != "passed" || res.Tests.Passed != 1 {
		t.Fatalf("result = %+v", res)
	}
	if sb.spec.Network != sandbox.NetworkNone {
		t.Errorf("no install needed, so no network expected; got %q", sb.spec.Network)
	}
	if !box.removed {
		t.Error("sandbox not removed")
	}
}

func TestPatchThatDoesNotApplyNeverStartsASandbox(t *testing.T) {
	sb := &fakeSandbox{box: &fakeBox{}}
	bad := "--- a/app.py\n+++ b/app.py\n@@ -1,2 +1,2 @@\n def nope():\n-    return 9\n+    return 2\n"
	res := newValidator(t, sb, "").Validate(context.Background(), Request{PatchID: "p", Iteration: 1, CloneURL: pythonRepo(t, nil), Diff: bad})
	if res.Status != "failed" || res.Error == nil || !strings.Contains(*res.Error, "patch does not apply") {
		t.Fatalf("result = %+v", res)
	}
	if sb.started {
		t.Error("sandbox started for a patch that does not apply")
	}
}

func TestTimeoutStillRemovesSandbox(t *testing.T) {
	answers := cleanAnswers()
	answers["pytest"] = sandbox.ExecResult{ExitCode: 124, TimedOut: true}
	box := &fakeBox{answers: answers}
	res := newValidator(t, &fakeSandbox{box: box}, "").Validate(context.Background(), Request{
		PatchID: "p", Iteration: 1, CloneURL: pythonRepo(t, nil), Diff: goodDiff,
	})
	if res.Status != "timeout" || res.Tests.Status != "error" || !strings.Contains(*res.Tests.Summary, "timed out") {
		t.Fatalf("result = %+v / tests %+v", res, res.Tests)
	}
	if !box.removed {
		t.Error("sandbox not removed after timeout")
	}
	for _, c := range box.calls {
		if c == "bandit" || c == "ruff" {
			t.Errorf("steps after a timeout should not run: %v", box.calls)
		}
	}
}

func TestFindingsAreLimitedToChangedFiles(t *testing.T) {
	answers := cleanAnswers()
	answers["ruff"] = sandbox.ExecResult{Stdout: `[
		{"code":"F821","message":"Undefined name x","filename":"/workspace/legacy.py","location":{"row":3}},
		{"code":"F401","message":"unused import","filename":"/workspace/app.py","location":{"row":1}}]`}
	repo := pythonRepo(t, map[string]string{"legacy.py": "print(x)\n"})

	res := newValidator(t, &fakeSandbox{box: &fakeBox{answers: answers, report: passingJUnit}}, "").Validate(
		context.Background(), Request{PatchID: "p", Iteration: 1, CloneURL: repo, Diff: goodDiff})

	st := res.StaticAnalysis
	if st.Status != "failed" || len(st.Findings) != 1 || *st.Findings[0].File != "app.py" {
		t.Fatalf("static = %+v", st)
	}
	if !strings.Contains(*st.Summary, "1 more in files this patch does not touch") {
		t.Errorf("summary = %s", *st.Summary)
	}
	if res.Status != "failed" {
		t.Errorf("status = %s", res.Status)
	}
}

func TestInstallRunsOnNetworkThenDisconnects(t *testing.T) {
	box := &fakeBox{answers: cleanAnswers(), report: passingJUnit}
	sb := &fakeSandbox{box: box}
	res := newValidator(t, sb, "devassist-sandbox-egress").Validate(context.Background(), Request{
		PatchID: "p", Iteration: 1, CloneURL: pythonRepo(t, map[string]string{"requirements.txt": "requests\n"}), Diff: goodDiff,
	})
	if res.Status != "passed" {
		t.Fatalf("result = %+v", res)
	}
	if sb.spec.Network != "devassist-sandbox-egress" {
		t.Errorf("network = %q", sb.spec.Network)
	}
	want := []string{"pip", "DISCONNECT", "pytest", "bandit", "ruff"}
	if strings.Join(box.calls, ",") != strings.Join(want, ",") {
		t.Errorf("call order = %v, want %v (patched code must only run offline)", box.calls, want)
	}
}

func TestInstallFailureIsAnError(t *testing.T) {
	answers := cleanAnswers()
	answers["pip"] = sandbox.ExecResult{ExitCode: 1, Stderr: "No matching distribution found for nope"}
	res := newValidator(t, &fakeSandbox{box: &fakeBox{answers: answers}}, "egress").Validate(context.Background(), Request{
		PatchID: "p", Iteration: 1, CloneURL: pythonRepo(t, map[string]string{"requirements.txt": "nope\n"}), Diff: goodDiff,
	})
	if res.Status != "error" || !strings.Contains(*res.Tests.Summary, "No matching distribution") {
		t.Fatalf("result = %+v", res)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	v := newValidator(t, &fakeSandbox{box: &fakeBox{}}, "")
	v.sem <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if res := v.Validate(ctx, Request{PatchID: "p", Iteration: 1}); res.Status != "error" {
		t.Fatalf("status = %s", res.Status)
	}
}
