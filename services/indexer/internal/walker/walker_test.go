package walker

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWalkSelectsSourceAndSkipsNoise(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app/main.py", "print('hi')\n")
	write(t, root, "cmd/server/main.go", "package main\n")
	write(t, root, "web/App.tsx", "export const A = 1\n")
	write(t, root, "README.md", "# Demo\n")
	write(t, root, "Dockerfile", "FROM scratch\n")
	write(t, root, "node_modules/lib/index.js", "module.exports = 1\n")
	write(t, root, ".git/config", "[core]\n")
	write(t, root, ".github/workflows/ci.yml", "on: push\n")
	write(t, root, "package-lock.json", "{}\n")
	write(t, root, "static/app.min.js", "var a=1\n")
	write(t, root, "logo.png", "\x89PNG\x00\x00")
	write(t, root, "data.bin.py", "abc\x00def")
	write(t, root, "big.py", string(make([]byte, 2048)))
	write(t, root, "empty.py", "")
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "link.py")); err != nil {
		t.Fatal(err)
	}

	files, err := Walk(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Language
	}
	want := map[string]string{
		"Dockerfile":         "dockerfile",
		"README.md":          "markdown",
		"app/main.py":        "python",
		"cmd/server/main.go": "go",
		"web/App.tsx":        "tsx",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for path, lang := range want {
		if got[path] != lang {
			t.Errorf("%s: language %q, want %q", path, got[path], lang)
		}
	}
	if files[0].Path != "Dockerfile" {
		t.Errorf("files not sorted: first is %s", files[0].Path)
	}
}

func TestLanguage(t *testing.T) {
	cases := map[string]string{"a.PY": "python", "x.mjs": "javascript", "Makefile": "makefile", "a.exe": "", "jquery.min.js": ""}
	for path, want := range cases {
		if got := Language(path); got != want {
			t.Errorf("Language(%q) = %q, want %q", path, got, want)
		}
	}
}
