package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures in testdata/ were captured from the real tools running in the
// sandbox images (see services/sandbox-runner/images).
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPytestJUnit(t *testing.T) {
	o := Parse("junit", Output{ExitCode: 1, Report: fixture(t, "pytest-junit.xml")})
	if o.Status != Failed || o.Passed != 13 || o.Failed != 1 {
		t.Fatalf("outcome = %+v", o)
	}
	f := o.Findings[0]
	if f.RuleID != "tests.test_calendar::test_century_years" || !strings.Contains(f.Message, "assert not True") {
		t.Errorf("finding = %+v", f)
	}
	if got := Parse("junit", Output{ExitCode: 5}); got.Status != Skipped {
		t.Errorf("no tests collected: %s", got.Status)
	}
	if got := Parse("junit", Output{ExitCode: 2, Stderr: "ImportError"}); got.Status != Failed {
		t.Errorf("collection error without report: %s", got.Status)
	}
}

func TestGoTestFailingTest(t *testing.T) {
	o := Parse("gotest", Output{ExitCode: 1, Stdout: fixture(t, "gotest-failing-test.json")})
	if o.Status != Failed || o.Passed != 1 || o.Failed != 1 || o.Skipped != 1 {
		t.Fatalf("outcome = %+v", o)
	}
	if len(o.Findings) != 1 || !strings.Contains(o.Findings[0].Message, "Add(2, 2) = 4, want 5") {
		t.Errorf("findings = %+v", o.Findings)
	}
}

func TestGoTestBuildFailure(t *testing.T) {
	o := Parse("gotest", Output{ExitCode: 1, Stdout: fixture(t, "gotest-build-failure.json")})
	if o.Status != Failed || o.Failed != 2 {
		t.Fatalf("outcome = %+v", o)
	}
	joined := o.Findings[0].Message + o.Findings[1].Message
	if !strings.Contains(joined, "declared and not used") || !strings.Contains(joined, "cannot use \"string\"") {
		t.Errorf("compiler errors missing from findings: %q", joined)
	}
}

func TestBandit(t *testing.T) {
	o := Parse("bandit", Output{ExitCode: 1, Stdout: fixture(t, "bandit.json")})
	if o.Status != Failed || len(o.Findings) != 3 {
		t.Fatalf("outcome = %+v", o)
	}
	var high Finding
	for _, f := range o.Findings {
		if f.Blocking {
			high = f
		}
	}
	if high.RuleID != "B602" || high.File != "datekit/unsafe.py" || high.Line != 5 {
		t.Errorf("blocking finding = %+v", high)
	}
}

func TestRuff(t *testing.T) {
	o := Parse("ruff", Output{Stdout: fixture(t, "ruff.json")})
	if o.Status != Failed || len(o.Findings) != 1 || o.Findings[0].RuleID != "F821" || o.Findings[0].File != "datekit/unsafe.py" {
		t.Fatalf("outcome = %+v", o)
	}
	if clean := Parse("ruff", Output{Stdout: "[]"}); clean.Status != Passed {
		t.Errorf("clean = %+v", clean)
	}
}

func TestGosecMediumIsNotBlocking(t *testing.T) {
	o := Parse("gosec", Output{Stdout: fixture(t, "gosec.json")})
	if o.Status != Passed || len(o.Findings) != 2 || o.Findings[0].File != "calc/calc.go" || o.Findings[0].Line == 0 {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestGoVetAndStaticcheck(t *testing.T) {
	vet := Parse("govet", Output{ExitCode: 1, Stderr: fixture(t, "govet.txt")})
	if vet.Status != Failed || vet.Findings[0].File != "calc/calc.go" || vet.Findings[0].Line != 13 {
		t.Fatalf("vet = %+v", vet)
	}
	sc := Parse("staticcheck", Output{ExitCode: 1, Stdout: fixture(t, "staticcheck.json")})
	if sc.Status != Failed {
		t.Fatalf("staticcheck = %+v", sc)
	}
	merged := Merge(vet, sc, Outcome{Status: Passed})
	if merged.Status != Failed || len(merged.Findings) != len(vet.Findings)+len(sc.Findings) {
		t.Errorf("merged = %+v", merged)
	}
}

func TestNpmAuditEslintTsc(t *testing.T) {
	audit := Parse("npmaudit", Output{ExitCode: 1, Stdout: `{"vulnerabilities":{"lodash":{"severity":"high"},"minimist":{"severity":"low"}}}`})
	if audit.Status != Failed || len(audit.Findings) != 2 || audit.Findings[0].RuleID != "lodash" {
		t.Fatalf("audit = %+v", audit)
	}
	if offline := Parse("npmaudit", Output{ExitCode: 1, Stderr: "request to registry failed"}); offline.Status != Error {
		t.Errorf("offline audit = %+v", offline)
	}

	lint := Parse("eslint", Output{ExitCode: 1, Stdout: `[{"filePath":"/workspace/src/a.ts","messages":[
		{"ruleId":"no-unused-vars","severity":1,"message":"x is unused","line":3},
		{"ruleId":"no-undef","severity":2,"message":"y is not defined","line":7}]}]`})
	if lint.Status != Failed || lint.Findings[1].File != "src/a.ts" || !lint.Findings[1].Blocking || lint.Findings[0].Blocking {
		t.Fatalf("eslint = %+v", lint)
	}

	ts := Parse("tsc", Output{ExitCode: 2, Stdout: "src/a.ts(4,7): error TS2322: Type 'string' is not assignable to type 'number'.\n"})
	if ts.Status != Failed || ts.Findings[0].RuleID != "TS2322" || ts.Findings[0].Line != 4 {
		t.Fatalf("tsc = %+v", ts)
	}
}

func TestUnreadableOutputIsAnError(t *testing.T) {
	for _, p := range []string{"bandit", "ruff", "gosec", "eslint"} {
		if o := Parse(p, Output{ExitCode: 2, Stderr: "Traceback..."}); o.Status != Error {
			t.Errorf("%s: status = %s", p, o.Status)
		}
	}
	if o := Parse("nope", Output{}); o.Status != Error {
		t.Errorf("unknown parser: %s", o.Status)
	}
}

func TestExitCode(t *testing.T) {
	if Parse("exitcode", Output{}).Status != Passed || Parse("exitcode", Output{ExitCode: 1}).Status != Failed {
		t.Fatal("exit code mapping wrong")
	}
}
