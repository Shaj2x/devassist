package toolchain

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func names(steps []Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

func TestPython(t *testing.T) {
	dir := repo(t, map[string]string{"pyproject.toml": "[project]\nname='x'\ndependencies = []\n"})
	p, err := Detect(dir, Overrides{}, DefaultImages)
	if err != nil {
		t.Fatal(err)
	}
	if p.Language != "python" || p.Image != "devassist/sandbox-python:latest" || len(p.Install) != 0 {
		t.Fatalf("plan = %+v", p)
	}
	if got := names(p.Steps); len(got) != 3 || got[0] != "pytest" || got[1] != "bandit" || got[2] != "ruff" {
		t.Errorf("steps = %v", got)
	}

	withDeps := repo(t, map[string]string{"pyproject.toml": "[project]\ndependencies = [\"requests\"]\n"})
	p, _ = Detect(withDeps, Overrides{}, DefaultImages)
	if len(p.Install) != 1 || !p.Install[0].NeedsNetwork {
		t.Errorf("install = %+v", p.Install)
	}
	reqs := repo(t, map[string]string{"requirements.txt": "flask\n"})
	p, _ = Detect(reqs, Overrides{}, DefaultImages)
	if p.Language != "python" || p.Install[0].Cmd[len(p.Install[0].Cmd)-1] != "requirements.txt" {
		t.Errorf("requirements plan = %+v", p)
	}
}

func TestGoTakesPrecedence(t *testing.T) {
	dir := repo(t, map[string]string{"go.mod": "module x\n", "package.json": "{}"})
	p, err := Detect(dir, Overrides{}, DefaultImages)
	if err != nil || p.Language != "go" || len(p.Steps) != 4 {
		t.Fatalf("plan = %+v, %v", p, err)
	}
}

func TestNodeUsesRepoTooling(t *testing.T) {
	dir := repo(t, map[string]string{
		"package.json":      `{"scripts":{"test":"jest"},"devDependencies":{"eslint":"^9","typescript":"^5"}}`,
		"package-lock.json": "{}",
		"tsconfig.json":     "{}",
	})
	p, _ := Detect(dir, Overrides{}, DefaultImages)
	if p.Install[0].Cmd[1] != "ci" {
		t.Errorf("lockfile should use npm ci: %v", p.Install[0].Cmd)
	}
	if got := names(p.Steps); len(got) != 4 {
		t.Errorf("steps = %v", got)
	}
	bare := repo(t, map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`})
	p, _ = Detect(bare, Overrides{}, DefaultImages)
	if got := names(p.Steps); len(got) != 1 || got[0] != "npm audit" {
		t.Errorf("placeholder test script should be ignored: %v", got)
	}
}

func TestOverrides(t *testing.T) {
	dir := repo(t, map[string]string{"setup.py": ""})
	p, err := Detect(dir, Overrides{InstallCommand: "make deps", TestCommand: "make test"}, DefaultImages)
	if err != nil {
		t.Fatal(err)
	}
	if p.Install[0].Cmd[2] != "make deps" || p.Steps[0].Cmd[2] != "make test" || p.Steps[0].Check != Tests {
		t.Errorf("plan = %+v", p)
	}
	forced, _ := Detect(repo(t, map[string]string{}), Overrides{Language: "go"}, DefaultImages)
	if forced.Language != "go" {
		t.Errorf("language override ignored: %s", forced.Language)
	}
}

func TestUnsupported(t *testing.T) {
	if _, err := Detect(repo(t, map[string]string{"main.rs": ""}), Overrides{}, DefaultImages); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}
