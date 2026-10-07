// Package parse turns raw tool output into structured outcomes: pass/fail
// counts and findings with file, line, rule and severity.
//
// Each parser also decides what "failed" means for its tool, so policy lives
// in one readable place:
//
//	tests     any failing or erroring test
//	security  any HIGH (or critical) severity finding
//	static    any error-level finding (ruff F/E9, go vet, staticcheck
//	          errors, eslint errors, tsc errors); style issues are warnings
package parse

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Status values, matching the event contract.
const (
	Passed  = "passed"
	Failed  = "failed"
	Skipped = "skipped"
	Error   = "error"
)

// Output is what a sandbox step produced.
type Output struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Report   string // side-channel file contents (pytest's JUnit XML)
}

// Finding is one issue. Severity is "error" (blocking) or "warning", or the
// tool's own level for security scanners ("high", "medium", "low").
type Finding struct {
	Tool     string `json:"tool,omitempty"`
	RuleID   string `json:"rule_id,omitempty"`
	Severity string `json:"severity,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
	Blocking bool   `json:"-"`
}

// Outcome is one step's parsed result.
type Outcome struct {
	Status   string
	Passed   int
	Failed   int
	Skipped  int
	Findings []Finding
	Summary  string
}

// Parse dispatches on the parser name from toolchain.Step.
func Parse(parser string, out Output) Outcome {
	switch parser {
	case "junit":
		return junit(out)
	case "gotest":
		return goTest(out)
	case "exitcode":
		return exitCode(out)
	case "bandit":
		return bandit(out)
	case "ruff":
		return ruff(out)
	case "gosec":
		return gosec(out)
	case "govet":
		return goVet(out)
	case "staticcheck":
		return staticcheck(out)
	case "npmaudit":
		return npmAudit(out)
	case "eslint":
		return eslint(out)
	case "tsc":
		return tsc(out)
	}
	return Outcome{Status: Error, Summary: "unknown parser " + parser}
}

// rank orders statuses from best to worst for merging.
var rank = map[string]int{Skipped: 0, Passed: 1, Failed: 2, Error: 3}

// Merge combines outcomes of several steps feeding one check (go vet +
// staticcheck): worst status wins, counts and findings add up.
func Merge(outcomes ...Outcome) Outcome {
	var m Outcome
	m.Status = Skipped
	var summaries []string
	for _, o := range outcomes {
		if rank[o.Status] > rank[m.Status] {
			m.Status = o.Status
		}
		m.Passed += o.Passed
		m.Failed += o.Failed
		m.Skipped += o.Skipped
		m.Findings = append(m.Findings, o.Findings...)
		if o.Summary != "" {
			summaries = append(summaries, o.Summary)
		}
	}
	m.Summary = strings.Join(summaries, "; ")
	return m
}

// FromFindings derives a status from findings: failed if any is blocking.
func FromFindings(tool string, findings []Finding) Outcome {
	o := Outcome{Status: Passed, Findings: findings}
	blocking := 0
	for _, f := range findings {
		if f.Blocking {
			blocking++
		}
	}
	if blocking > 0 {
		o.Status = Failed
	}
	o.Summary = fmt.Sprintf("%s: %d finding(s), %d blocking", tool, len(findings), blocking)
	return o
}

// RelPath strips the sandbox workspace prefix so paths match the diff.
func RelPath(p string) string {
	p = strings.TrimPrefix(p, "/workspace/")
	return strings.TrimPrefix(p, "./")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

type junitCase struct {
	Name      string `xml:"name,attr"`
	Classname string `xml:"classname,attr"`
	Failure   *struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	} `xml:"failure"`
	Error *struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	} `xml:"error"`
	Skipped *struct{} `xml:"skipped"`
}

type junitSuite struct {
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

func (s junitSuite) all() []junitCase {
	out := s.Cases
	for _, child := range s.Suites {
		out = append(out, child.all()...)
	}
	return out
}

// junit parses pytest's report. pytest exit codes: 0 ok, 1 tests failed,
// 2 interrupted/collection error, 5 no tests collected.
func junit(out Output) Outcome {
	if out.ExitCode == 5 {
		return Outcome{Status: Skipped, Summary: "no tests collected"}
	}
	var root junitSuite
	if out.Report == "" || xml.Unmarshal([]byte(out.Report), &root) != nil {
		return exitCode(out) // custom test command without a report
	}
	o := Outcome{}
	for _, c := range root.all() {
		name := strings.TrimSuffix(c.Classname, ".") + "::" + c.Name
		switch {
		case c.Failure != nil:
			o.Failed++
			o.Findings = append(o.Findings, Finding{Tool: "pytest", RuleID: name, Severity: "error", Blocking: true,
				Message: firstLines(c.Failure.Message+"\n"+c.Failure.Text, 12)})
		case c.Error != nil:
			o.Failed++
			o.Findings = append(o.Findings, Finding{Tool: "pytest", RuleID: name, Severity: "error", Blocking: true,
				Message: firstLines(c.Error.Message+"\n"+c.Error.Text, 12)})
		case c.Skipped != nil:
			o.Skipped++
		default:
			o.Passed++
		}
	}
	switch {
	case o.Failed > 0 || (out.ExitCode != 0 && o.Passed+o.Skipped == 0):
		o.Status = Failed
	case o.Passed+o.Skipped == 0:
		o.Status = Skipped
	default:
		o.Status = Passed
	}
	o.Summary = fmt.Sprintf("%d passed, %d failed, %d skipped", o.Passed, o.Failed, o.Skipped)
	return o
}

type goTestEvent struct {
	Action     string `json:"Action"`
	Package    string `json:"Package"`
	ImportPath string `json:"ImportPath"` // set on build-output events
	Test       string `json:"Test"`
	Output     string `json:"Output"`
}

// goTest parses `go test -json`. A package that fails to build has no
// per-test events, only a package-level fail, which counts as one failure.
func goTest(out Output) Outcome {
	o := Outcome{}
	outputs := map[string]*strings.Builder{}
	scanner := bufio.NewScanner(strings.NewReader(out.Stdout))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var ev goTestEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		key := ev.Package + "." + ev.Test
		switch ev.Action {
		case "build-output":
			// Compiler errors; ImportPath may be "pkg [pkg.test]".
			pkg := strings.Fields(ev.ImportPath + " ")[0]
			appendOutput(outputs, pkg+".", ev.Output)
		case "output":
			appendOutput(outputs, key, ev.Output)
		case "pass":
			if ev.Test != "" {
				o.Passed++
			}
		case "skip":
			if ev.Test != "" {
				o.Skipped++
			}
		case "fail":
			name := ev.Test
			if name == "" {
				// Package-level failure: only report it if no test inside failed.
				if hasFailedTest(o.Findings, ev.Package) {
					continue
				}
				name = "(build failed)"
			}
			o.Failed++
			msg := ""
			if b := outputs[key]; b != nil {
				msg = firstLines(b.String(), 15)
			}
			o.Findings = append(o.Findings, Finding{Tool: "go test", RuleID: ev.Package + " " + name,
				Severity: "error", Blocking: true, Message: msg})
		}
	}
	switch {
	case o.Failed > 0:
		o.Status = Failed
	case out.ExitCode != 0:
		o.Status = Failed
		o.Findings = append(o.Findings, Finding{Tool: "go test", Severity: "error", Blocking: true,
			Message: firstLines(out.Stderr+out.Stdout, 15)})
	case o.Passed+o.Skipped == 0:
		o.Status = Skipped
	default:
		o.Status = Passed
	}
	o.Summary = fmt.Sprintf("%d passed, %d failed, %d skipped", o.Passed, o.Failed, o.Skipped)
	return o
}

func appendOutput(outputs map[string]*strings.Builder, key, text string) {
	if outputs[key] == nil {
		outputs[key] = &strings.Builder{}
	}
	outputs[key].WriteString(text) //nolint:gosec // strings.Builder writes never fail
}

func hasFailedTest(findings []Finding, pkg string) bool {
	for _, f := range findings {
		if strings.HasPrefix(f.RuleID, pkg+" ") {
			return true
		}
	}
	return false
}

func exitCode(out Output) Outcome {
	if out.ExitCode == 0 {
		return Outcome{Status: Passed, Summary: "exit code 0"}
	}
	summary := fmt.Sprintf("exit code %d", out.ExitCode)
	if out.ExitCode == 137 {
		summary += " (killed: likely exceeded the sandbox memory limit)"
	}
	return Outcome{Status: Failed, Failed: 1, Summary: summary,
		Findings: []Finding{{Severity: "error", Blocking: true, Message: firstLines(out.Stderr+"\n"+out.Stdout, 20)}}}
}

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

func bandit(out Output) Outcome {
	var report struct {
		Results []struct {
			Filename string `json:"filename"`
			Line     int    `json:"line_number"`
			TestID   string `json:"test_id"`
			Severity string `json:"issue_severity"`
			Text     string `json:"issue_text"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &report); err != nil {
		return toolError("bandit", out)
	}
	var findings []Finding
	for _, r := range report.Results {
		sev := strings.ToLower(r.Severity)
		findings = append(findings, Finding{Tool: "bandit", RuleID: r.TestID, Severity: sev,
			File: RelPath(r.Filename), Line: r.Line, Message: r.Text, Blocking: sev == "high"})
	}
	return FromFindings("bandit", findings)
}

func gosec(out Output) Outcome {
	var report struct {
		Issues []struct {
			Severity string `json:"severity"`
			RuleID   string `json:"rule_id"`
			Details  string `json:"details"`
			File     string `json:"file"`
			Line     string `json:"line"` // e.g. "12" or "12-14"
		} `json:"Issues"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &report); err != nil {
		return toolError("gosec", out)
	}
	var findings []Finding
	for _, i := range report.Issues {
		line, _ := strconv.Atoi(strings.SplitN(i.Line, "-", 2)[0])
		sev := strings.ToLower(i.Severity)
		findings = append(findings, Finding{Tool: "gosec", RuleID: i.RuleID, Severity: sev,
			File: RelPath(i.File), Line: line, Message: i.Details, Blocking: sev == "high"})
	}
	return FromFindings("gosec", findings)
}

// npmAudit reports vulnerable dependencies (no file/line: they are packages).
func npmAudit(out Output) Outcome {
	var report struct {
		Vulnerabilities map[string]struct {
			Severity string `json:"severity"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &report); err != nil || report.Vulnerabilities == nil {
		return toolError("npm audit", out)
	}
	names := make([]string, 0, len(report.Vulnerabilities))
	for name := range report.Vulnerabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	var findings []Finding
	for _, name := range names {
		sev := report.Vulnerabilities[name].Severity
		findings = append(findings, Finding{Tool: "npm audit", RuleID: name, Severity: sev,
			File: "package.json", Message: fmt.Sprintf("%s severity vulnerability in dependency %s", sev, name),
			Blocking: sev == "high" || sev == "critical"})
	}
	return FromFindings("npm audit", findings)
}

// ---------------------------------------------------------------------------
// Static analysis
// ---------------------------------------------------------------------------

func ruff(out Output) Outcome {
	var items []struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Filename string `json:"filename"`
		Location struct {
			Row int `json:"row"`
		} `json:"location"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &items); err != nil {
		return toolError("ruff", out)
	}
	var findings []Finding
	for _, it := range items {
		blocking := strings.HasPrefix(it.Code, "F") || strings.HasPrefix(it.Code, "E9")
		findings = append(findings, Finding{Tool: "ruff", RuleID: it.Code, Severity: severity(blocking),
			File: RelPath(it.Filename), Line: it.Location.Row, Message: it.Message, Blocking: blocking})
	}
	return FromFindings("ruff", findings)
}

var vetLine = regexp.MustCompile(`^(?:vet: )?(\S+?\.go):(\d+):(?:\d+:)?\s*(.+)$`)

func goVet(out Output) Outcome {
	var findings []Finding
	for _, line := range strings.Split(out.Stderr, "\n") {
		if m := vetLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.Atoi(m[2])
			findings = append(findings, Finding{Tool: "go vet", Severity: "error", Blocking: true,
				File: RelPath(m[1]), Line: n, Message: m[3]})
		}
	}
	if out.ExitCode != 0 && len(findings) == 0 {
		findings = append(findings, Finding{Tool: "go vet", Severity: "error", Blocking: true,
			Message: firstLines(out.Stderr, 10)})
	}
	return FromFindings("go vet", findings)
}

func staticcheck(out Output) Outcome {
	var findings []Finding
	for _, line := range strings.Split(out.Stdout, "\n") {
		var item struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Location struct {
				File string `json:"file"`
				Line int    `json:"line"`
			} `json:"location"`
		}
		if json.Unmarshal([]byte(line), &item) != nil || item.Code == "" {
			continue
		}
		blocking := item.Severity == "error"
		findings = append(findings, Finding{Tool: "staticcheck", RuleID: item.Code, Severity: severity(blocking),
			File: RelPath(item.Location.File), Line: item.Location.Line, Message: item.Message, Blocking: blocking})
	}
	if out.ExitCode != 0 && len(findings) == 0 {
		return toolError("staticcheck", out)
	}
	return FromFindings("staticcheck", findings)
}

func eslint(out Output) Outcome {
	var files []struct {
		FilePath string `json:"filePath"`
		Messages []struct {
			RuleID   string `json:"ruleId"`
			Severity int    `json:"severity"` // 1 warn, 2 error
			Message  string `json:"message"`
			Line     int    `json:"line"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(out.Stdout), &files); err != nil {
		return toolError("eslint", out)
	}
	var findings []Finding
	for _, f := range files {
		for _, m := range f.Messages {
			findings = append(findings, Finding{Tool: "eslint", RuleID: m.RuleID, Severity: severity(m.Severity == 2),
				File: RelPath(f.FilePath), Line: m.Line, Message: m.Message, Blocking: m.Severity == 2})
		}
	}
	return FromFindings("eslint", findings)
}

var tscLine = regexp.MustCompile(`^(.+?)\((\d+),\d+\): error (TS\d+): (.+)$`)

func tsc(out Output) Outcome {
	var findings []Finding
	for _, line := range strings.Split(out.Stdout, "\n") {
		if m := tscLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.Atoi(m[2])
			findings = append(findings, Finding{Tool: "tsc", RuleID: m[3], Severity: "error", Blocking: true,
				File: RelPath(m[1]), Line: n, Message: m[4]})
		}
	}
	if out.ExitCode != 0 && len(findings) == 0 {
		return toolError("tsc", out)
	}
	return FromFindings("tsc", findings)
}

// ---------------------------------------------------------------------------

func toolError(tool string, out Output) Outcome {
	return Outcome{Status: Error, Summary: fmt.Sprintf("%s produced unreadable output (exit code %d): %s",
		tool, out.ExitCode, firstLines(out.Stderr+out.Stdout, 3))}
}

func severity(blocking bool) string {
	if blocking {
		return "error"
	}
	return "warning"
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("... (%d more lines)", len(lines)-n))
	}
	return strings.Join(lines, "\n")
}
