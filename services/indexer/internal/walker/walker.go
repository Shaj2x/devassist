// Package walker finds the source files worth indexing in a checkout.
package walker

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File is one source file selected for indexing.
type File struct {
	Path     string // slash-separated, relative to the repository root
	Language string
	Content  []byte
}

// Directories that never contain first-party source.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".venv": true, "venv": true, "__pycache__": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, "target": true, ".next": true,
	"coverage": true, ".idea": true, ".vscode": true, ".tox": true,
}

// Generated files that add noise to search.
var skipFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"uv.lock": true, "poetry.lock": true, "go.sum": true, "Cargo.lock": true,
}

var extLanguages = map[string]string{
	".py": "python", ".go": "go",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".tsx": "tsx",
	".java": "java", ".rb": "ruby", ".rs": "rust", ".c": "c", ".h": "c",
	".cpp": "cpp", ".cc": "cpp", ".hpp": "cpp", ".cs": "csharp", ".php": "php",
	".kt": "kotlin", ".swift": "swift", ".scala": "scala",
	".sh": "shell", ".bash": "shell", ".sql": "sql",
	".md": "markdown", ".rst": "text", ".txt": "text",
	".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".json": "json",
	".html": "html", ".css": "css", ".scss": "css",
}

var nameLanguages = map[string]string{"Dockerfile": "dockerfile", "Makefile": "makefile"}

// Language returns the language for a path, or "" if it is not indexed.
func Language(path string) string {
	base := filepath.Base(path)
	if lang, ok := nameLanguages[base]; ok {
		return lang
	}
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return ""
	}
	return extLanguages[strings.ToLower(filepath.Ext(base))]
}

// Walk returns indexable files under root, sorted by path. Files larger than
// maxBytes and files that look binary are skipped.
func Walk(root string, maxBytes int) ([]File, error) {
	var files []File
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipFiles[d.Name()] {
			return nil // skips symlinks too: they could point outside the repo
		}
		lang := Language(d.Name())
		if lang == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > int64(maxBytes) || info.Size() == 0 {
			return nil
		}
		content, err := os.ReadFile(path) //nolint:gosec // path comes from WalkDir under root; symlinks are skipped
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if isBinary(content) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, File{Path: filepath.ToSlash(rel), Language: lang, Content: content})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

// isBinary uses git's heuristic: a NUL byte in the first 8 KB.
func isBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), 8000)], 0) >= 0
}
