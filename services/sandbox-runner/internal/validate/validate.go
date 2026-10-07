// Package validate runs one patch through the sandbox:
//
//	clone at commit -> git apply -> detect toolchain -> start sandbox
//	-> stream code in -> [network phase: install deps, npm audit]
//	-> disconnect network -> tests, security scan, static analysis
//	-> structured result -> always remove the sandbox
//
// The result is a validation.completed payload. A patch that does not apply,
// fails a check, or times out is a normal result, not an error: the
// orchestrator's Debugger agent needs that information to fix the patch.
package validate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/gitrepo"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/parse"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/sandbox"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
)

// Request is one patch to validate.
type Request struct {
	JobID     string
	PatchID   string
	Iteration int
	CloneURL  string
	CommitSHA string
	Diff      string
	Overrides toolchain.Overrides
	Timeout   time.Duration // 0 = Options.DefaultTimeout
}

// Sandbox and Box are the parts of package sandbox the validator uses, as
// interfaces so the orchestration logic is unit-testable without Docker.
type Sandbox interface {
	Start(ctx context.Context, s sandbox.Spec) (Box, error)
	ImageExists(ctx context.Context, image string) (bool, error)
}

type Box interface {
	Exec(ctx context.Context, cmd []string, limit time.Duration, stdin io.Reader) (sandbox.ExecResult, error)
	CopyDir(ctx context.Context, dir string) error
	ReadFile(ctx context.Context, path string) (string, error)
	DisconnectNetwork(ctx context.Context) error
	Remove(ctx context.Context) error
}

// Options configure every validation.
type Options struct {
	Images         toolchain.Images
	Limits         sandbox.Limits
	EgressNetwork  string // network for the install phase; "" disables it
	DefaultTimeout time.Duration
	StepTimeout    time.Duration
	WorkDir        string
	GitHubToken    string
	Concurrency    int // max sandboxes at once across all callers
}

type Validator struct {
	sb   Sandbox
	opts Options
	sem  chan struct{}
	log  *slog.Logger
}

func New(sb Sandbox, opts Options, log *slog.Logger) *Validator {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 5 * time.Minute
	}
	if opts.StepTimeout <= 0 {
		opts.StepTimeout = 3 * time.Minute
	}
	return &Validator{sb: sb, opts: opts, sem: make(chan struct{}, opts.Concurrency), log: log}
}

// Validate never returns an error: every failure mode is a result status.
func (v *Validator) Validate(ctx context.Context, req Request) events.ValidationCompletedPayload {
	started := time.Now()
	res := events.ValidationCompletedPayload{
		JobID: req.JobID, PatchID: req.PatchID, Iteration: req.Iteration,
		Tests: skipped("not run"), Security: skipped("not run"), StaticAnalysis: skipped("not run"),
	}
	finish := func(status, errMsg string) events.ValidationCompletedPayload {
		res.Status = status
		if errMsg != "" {
			res.Error = &errMsg
		}
		res.DurationMS = time.Since(started).Milliseconds()
		v.log.InfoContext(ctx, "validation finished", "patch_id", req.PatchID, "status", status,
			"tests", res.Tests.Status, "security", res.Security.Status, "static", res.StaticAnalysis.Status,
			"duration_ms", res.DurationMS)
		return res
	}

	// Bound concurrent sandboxes across Kafka consumers and HTTP callers.
	select {
	case v.sem <- struct{}{}:
		defer func() { <-v.sem }()
	case <-ctx.Done():
		return finish(events.CheckError, "cancelled while waiting for a sandbox slot")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = v.opts.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 1. Checkout and apply the patch on the host side: cheap, and a patch
	// that does not apply never costs a container.
	if err := os.MkdirAll(v.opts.WorkDir, 0o750); err != nil {
		return finish("error", err.Error())
	}
	dir, err := os.MkdirTemp(v.opts.WorkDir, "patch-*")
	if err != nil {
		return finish("error", err.Error())
	}
	defer os.RemoveAll(dir)

	if _, err := gitrepo.Clone(ctx, req.CloneURL, dir, gitrepo.Options{CommitSHA: req.CommitSHA, GitHubToken: v.opts.GitHubToken}); err != nil {
		return finish("error", "checkout failed: "+err.Error())
	}
	applied, err := gitrepo.Apply(ctx, dir, req.Diff)
	if err != nil {
		// The agent's fault, not the system's: report as a failed validation.
		return finish("failed", "patch does not apply: "+err.Error())
	}

	plan, err := toolchain.Detect(dir, req.Overrides, v.opts.Images)
	if err != nil {
		return finish("error", err.Error())
	}
	if ok, err := v.sb.ImageExists(ctx, plan.Image); err != nil || !ok {
		return finish("error", fmt.Sprintf("sandbox image %s is not available (run `make sandbox-images`)", plan.Image))
	}

	// 2. Start the sandbox. It is removed however we leave this function.
	network := sandbox.NetworkNone
	needsNetwork := len(plan.Install) > 0 || slices.ContainsFunc(plan.Steps, func(s toolchain.Step) bool { return s.NeedsNetwork })
	if needsNetwork && v.opts.EgressNetwork != "" {
		network = v.opts.EgressNetwork
	}
	box, err := v.sb.Start(ctx, sandbox.Spec{
		Image: plan.Image, Network: network, Limits: v.opts.Limits,
		Labels: map[string]string{"devassist.job_id": req.JobID, "devassist.patch_id": req.PatchID},
	})
	if err != nil {
		return finish("error", err.Error())
	}
	defer func() {
		if err := box.Remove(ctx); err != nil {
			v.log.ErrorContext(ctx, "sandbox removal failed; the reaper will retry", "error", err)
		}
	}()
	if err := box.CopyDir(ctx, dir); err != nil {
		return finish(timeoutOr(ctx, "error"), "copy workspace: "+err.Error())
	}

	run := &runner{v: v, box: box, ctx: ctx, results: map[string][]stepResult{}}

	// 3. Network phase: dependency install (and npm audit).
	if network != sandbox.NetworkNone {
		for _, step := range plan.Install {
			out, done := run.step(step)
			if !done {
				return finish("timeout", step.Name+" timed out")
			}
			if out.ExitCode != 0 {
				res.Tests = checkResult(parse.Outcome{Status: parse.Error,
					Summary: "dependency install failed: " + tail(out.Stderr+out.Stdout, 600)}, step.Name, nil)
				return finish("error", "dependency install failed")
			}
		}
		for _, step := range plan.Steps {
			if step.NeedsNetwork {
				if _, done := run.step(step); !done {
					return finish("timeout", step.Name+" timed out")
				}
			}
		}
	}
	if err := box.DisconnectNetwork(ctx); err != nil {
		return finish("error", err.Error())
	}

	// 4. Offline phase: everything that executes or analyses the patched code.
	for _, step := range plan.Steps {
		if step.NeedsNetwork {
			if network == sandbox.NetworkNone {
				run.skip(step, "requires network; disabled for this sandbox")
			}
			continue
		}
		if _, done := run.step(step); !done {
			res.Tests, res.Security, res.StaticAnalysis = run.summarize(applied.ChangedFiles)
			return finish("timeout", step.Name+" timed out")
		}
	}

	res.Tests, res.Security, res.StaticAnalysis = run.summarize(applied.ChangedFiles)
	return finish(overall(res.Tests, res.Security, res.StaticAnalysis), "")
}

// runner executes steps and accumulates their outcomes per check.
type runner struct {
	v       *Validator
	box     Box
	ctx     context.Context
	results map[string][]stepResult
}

type stepResult struct {
	name    string
	outcome parse.Outcome
	log     string
	took    time.Duration
}

// step runs one command. done=false means it timed out.
func (r *runner) step(s toolchain.Step) (sandbox.ExecResult, bool) {
	limit := r.v.opts.StepTimeout
	if dl, ok := r.ctx.Deadline(); ok {
		limit = min(limit, time.Until(dl))
	}
	out, err := r.box.Exec(r.ctx, s.Cmd, limit, nil)
	if out.TimedOut || errors.Is(err, context.DeadlineExceeded) || r.ctx.Err() != nil {
		r.record(s, stepResult{name: s.Name, took: out.Duration,
			outcome: parse.Outcome{Status: parse.Error, Summary: fmt.Sprintf("%s timed out after %s", s.Name, limit.Round(time.Second))}})
		return out, false
	}
	if err != nil {
		r.record(s, stepResult{name: s.Name, outcome: parse.Outcome{Status: parse.Error, Summary: err.Error()}})
		return out, true
	}
	if out.ExitCode == 127 && s.Optional {
		return out, true // tool not installed
	}
	po := parse.Output{ExitCode: out.ExitCode, Stdout: out.Stdout, Stderr: out.Stderr}
	if s.Parser == "junit" {
		po.Report, _ = r.box.ReadFile(r.ctx, toolchain.JUnitPath)
	}
	r.record(s, stepResult{name: s.Name, outcome: parse.Parse(s.Parser, po), log: out.Stdout + out.Stderr, took: out.Duration})
	return out, true
}

func (r *runner) skip(s toolchain.Step, why string) {
	r.record(s, stepResult{name: s.Name, outcome: parse.Outcome{Status: parse.Skipped, Summary: s.Name + ": " + why}})
}

func (r *runner) record(s toolchain.Step, sr stepResult) {
	if s.Check != "" {
		r.results[s.Check] = append(r.results[s.Check], sr)
	}
}

// summarize builds the three check results. Security and static findings are
// limited to files the patch touched: the reviewer cares about what this
// change introduces, not the repository's existing debt.
func (r *runner) summarize(changed []string) (tests, security, static events.CheckResult) {
	build := func(check string, filter bool) events.CheckResult {
		steps := r.results[check]
		if len(steps) == 0 {
			return skipped("no tool configured for this language")
		}
		var outcomes []parse.Outcome
		var names []string
		var logs strings.Builder
		var took time.Duration
		for _, s := range steps {
			o := s.outcome
			if filter && o.Status != parse.Error && o.Status != parse.Skipped {
				o = onlyChanged(s.name, o, changed)
			}
			outcomes = append(outcomes, o)
			names = append(names, s.name)
			if s.log != "" {
				fmt.Fprintf(&logs, "$ %s\n%s\n", s.name, s.log)
			}
			took += s.took
		}
		cr := checkResult(parse.Merge(outcomes...), strings.Join(names, " + "), &logs)
		cr.DurationMS = took.Milliseconds()
		return cr
	}
	return build(toolchain.Tests, false), build(toolchain.Security, true), build(toolchain.Static, true)
}

func onlyChanged(tool string, o parse.Outcome, changed []string) parse.Outcome {
	var kept []parse.Finding
	for _, f := range o.Findings {
		// Findings without a file (a vulnerable npm dependency) always count.
		if f.File == "" || f.File == "package.json" || slices.Contains(changed, f.File) {
			kept = append(kept, f)
		}
	}
	filtered := parse.FromFindings(tool, kept)
	if dropped := len(o.Findings) - len(kept); dropped > 0 {
		filtered.Summary += fmt.Sprintf(" (%d more in files this patch does not touch)", dropped)
	}
	return filtered
}

const maxFindings = 50

func checkResult(o parse.Outcome, tool string, logs *strings.Builder) events.CheckResult {
	cr := events.CheckResult{Status: o.Status, Passed: o.Passed, Failed: o.Failed, Skipped: o.Skipped,
		Findings: []events.Finding{}}
	if tool != "" {
		cr.Tool = &tool
	}
	if o.Summary != "" {
		cr.Summary = &o.Summary
	}
	if logs != nil && logs.Len() > 0 {
		l := tail(logs.String(), 8000)
		cr.Log = &l
	}
	for i, f := range o.Findings {
		if i == maxFindings {
			break
		}
		f := f
		ef := events.Finding{Message: f.Message, Tool: &f.Tool}
		if f.RuleID != "" {
			ef.RuleID = &f.RuleID
		}
		if f.Severity != "" {
			ef.Severity = &f.Severity
		}
		if f.File != "" {
			ef.File = &f.File
		}
		if f.Line > 0 {
			ef.Line = &f.Line
		}
		cr.Findings = append(cr.Findings, ef)
	}
	return cr
}

func skipped(why string) events.CheckResult {
	return events.CheckResult{Status: parse.Skipped, Summary: &why, Findings: []events.Finding{}}
}

// overall is "passed" only if every check passed or was skipped.
func overall(checks ...events.CheckResult) string {
	status := "passed"
	for _, c := range checks {
		switch c.Status {
		case parse.Error:
			return "error"
		case parse.Failed:
			status = "failed"
		}
	}
	return status
}

func timeoutOr(ctx context.Context, status string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return status
}

// tail keeps the end of s: the last lines of a log are the informative ones.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
