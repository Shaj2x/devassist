// Package toolchain decides how to validate a repository: which sandbox
// image to use and which commands install dependencies, run the tests, scan
// for security issues, and run static analysis.
//
// Detection looks at marker files (pyproject.toml, go.mod, package.json).
// Every command can be overridden per repository (the `config` block of the
// patch.generated event, stored in repositories.config).
package toolchain

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Check names, matching the validation.completed payload.
const (
	Tests    = "tests"
	Security = "security"
	Static   = "static_analysis"
)

// Step is one command run inside the sandbox.
type Step struct {
	Name  string   // e.g. "pytest", "bandit"
	Cmd   []string // argv; run without a shell unless the override uses one
	Check string   // which check this step feeds ("" for install)
	// NeedsNetwork steps run in the network phase (dependency install, npm
	// audit). Everything else runs after the network is disconnected.
	NeedsNetwork bool
	// Parser names the output format the step produces (see package parse).
	Parser string
	// Optional steps are skipped silently when their tool is absent.
	Optional bool
}

// Plan is everything needed to validate one repository.
type Plan struct {
	Language string
	Image    string
	Install  []Step
	Steps    []Step
}

// Overrides come from repository configuration.
type Overrides struct {
	Language       string
	InstallCommand string
	TestCommand    string
}

// Images maps a language to its sandbox image.
type Images map[string]string

// DefaultImages are built by `make sandbox-images`.
var DefaultImages = Images{
	"python": "devassist/sandbox-python:latest",
	"go":     "devassist/sandbox-go:latest",
	"node":   "devassist/sandbox-node:latest",
}

// ErrUnsupported means no supported language was detected.
var ErrUnsupported = errors.New("no supported language detected (looked for pyproject.toml, setup.py, requirements.txt, go.mod, package.json)")

// Detect builds a Plan for the checkout in dir.
func Detect(dir string, o Overrides, images Images) (Plan, error) {
	lang := o.Language
	if lang == "" {
		lang = detectLanguage(dir)
	}
	var plan Plan
	switch lang {
	case "python":
		plan = pythonPlan(dir)
	case "go":
		plan = goPlan(dir)
	case "node", "javascript", "typescript":
		lang = "node"
		plan = nodePlan(dir)
	default:
		return Plan{}, ErrUnsupported
	}
	plan.Language, plan.Image = lang, images[lang]

	if o.InstallCommand != "" {
		plan.Install = []Step{{Name: "install", Cmd: shell(o.InstallCommand), NeedsNetwork: true}}
	}
	if o.TestCommand != "" {
		for i, s := range plan.Steps {
			if s.Check == Tests {
				plan.Steps[i] = Step{Name: "custom tests", Cmd: shell(o.TestCommand), Check: Tests, Parser: s.Parser}
			}
		}
	}
	return plan, nil
}

func detectLanguage(dir string) string {
	switch {
	case exists(dir, "go.mod"):
		return "go"
	case exists(dir, "pyproject.toml"), exists(dir, "setup.py"), exists(dir, "requirements.txt"):
		return "python"
	case exists(dir, "package.json"):
		return "node"
	}
	return ""
}

// JUnitPath is where pytest writes its report inside the sandbox.
const JUnitPath = "/tmp/devassist-junit.xml"

func pythonPlan(dir string) Plan {
	var install []Step
	for _, req := range []string{"requirements.txt", "requirements-dev.txt"} {
		if exists(dir, req) {
			install = append(install, Step{Name: "pip install " + req, NeedsNetwork: true,
				Cmd: []string{"pip", "install", "--user", "-r", req}})
		}
	}
	if len(install) == 0 && pyprojectHasDependencies(dir) {
		install = append(install, Step{Name: "pip install project", NeedsNetwork: true,
			Cmd: []string{"pip", "install", "--user", "-e", "."}})
	}
	return Plan{
		Install: install,
		Steps: []Step{
			{Name: "pytest", Check: Tests, Parser: "junit",
				Cmd: []string{"python", "-m", "pytest", "-q", "-p", "no:cacheprovider", "--junitxml=" + JUnitPath}},
			{Name: "bandit", Check: Security, Parser: "bandit",
				Cmd: []string{"bandit", "-r", ".", "-f", "json", "-q", "-x", "./tests,./test,./.venv,./node_modules"}},
			// --isolated: a fixed, explainable rule set rather than whatever the
			// repo configures. F = pyflakes errors, E9 = syntax errors, B = bugbear.
			{Name: "ruff", Check: Static, Parser: "ruff",
				Cmd: []string{"ruff", "check", ".", "--isolated", "--select", "F,E9,B", "--output-format", "json", "--exit-zero", "--no-cache"}},
		},
	}
}

func goPlan(_ string) Plan {
	return Plan{
		Install: []Step{{Name: "go mod download", NeedsNetwork: true, Cmd: []string{"go", "mod", "download"}}},
		Steps: []Step{
			{Name: "go test", Check: Tests, Parser: "gotest", Cmd: []string{"go", "test", "-json", "./..."}},
			{Name: "gosec", Check: Security, Parser: "gosec", Cmd: []string{"gosec", "-fmt=json", "-quiet", "-no-fail", "./..."}},
			{Name: "go vet", Check: Static, Parser: "govet", Cmd: []string{"go", "vet", "./..."}},
			{Name: "staticcheck", Check: Static, Parser: "staticcheck", Cmd: []string{"staticcheck", "-f", "json", "./..."}},
		},
	}
}

func nodePlan(dir string) Plan {
	pkg := readPackageJSON(dir)
	installCmd := []string{"npm", "install", "--ignore-scripts", "--no-audit"}
	if exists(dir, "package-lock.json") {
		installCmd = []string{"npm", "ci", "--ignore-scripts", "--no-audit"}
	}
	plan := Plan{
		Install: []Step{{Name: "npm install", NeedsNetwork: true, Cmd: installCmd}},
		// npm audit queries the registry, so it runs while the network is up.
		Steps: []Step{{Name: "npm audit", Check: Security, Parser: "npmaudit", NeedsNetwork: true,
			Cmd: []string{"npm", "audit", "--json"}}},
	}
	if test := pkg.Scripts["test"]; test != "" && !strings.Contains(test, "no test specified") {
		plan.Steps = append(plan.Steps, Step{Name: "npm test", Check: Tests, Parser: "exitcode", Cmd: []string{"npm", "test"}})
	}
	if pkg.has("eslint") {
		plan.Steps = append(plan.Steps, Step{Name: "eslint", Check: Static, Parser: "eslint",
			Cmd: []string{"npx", "--no-install", "eslint", ".", "-f", "json"}})
	}
	if pkg.has("typescript") && exists(dir, "tsconfig.json") {
		plan.Steps = append(plan.Steps, Step{Name: "tsc", Check: Static, Parser: "tsc",
			Cmd: []string{"npx", "--no-install", "tsc", "--noEmit", "--pretty", "false"}})
	}
	return plan
}

type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (p packageJSON) has(dep string) bool {
	_, a := p.Dependencies[dep]
	_, b := p.DevDependencies[dep]
	return a || b
}

func readPackageJSON(dir string) packageJSON {
	var p packageJSON
	//nolint:gosec // fixed file name inside our own checkout
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	return p
}

// pyprojectHasDependencies is a deliberately simple check for a non-empty
// `dependencies = [...]` under [project]; full TOML parsing is not needed to
// decide whether to run pip.
func pyprojectHasDependencies(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")) //nolint:gosec // fixed name in our checkout
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
		if strings.HasPrefix(trimmed, "dependencies=[") && trimmed != "dependencies=[]" {
			return true
		}
	}
	return false
}

func shell(cmd string) []string { return []string{"sh", "-c", cmd} }

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}
