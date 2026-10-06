package chunker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Shaj2x/devassist/services/indexer/internal/walker"
)

type want struct {
	name, kind string
	start, end int
}

func summarize(chunks []Chunk) []want {
	out := make([]want, len(chunks))
	for i, c := range chunks {
		out[i] = want{c.SymbolName, c.SymbolKind, c.StartLine, c.EndLine}
	}
	return out
}

func assertChunks(t *testing.T, got []Chunk, expected []want) {
	t.Helper()
	g := summarize(got)
	if fmt.Sprint(g) != fmt.Sprint(expected) {
		t.Fatalf("chunks:\n got  %v\n want %v", g, expected)
	}
}

func TestPythonDefinitions(t *testing.T) {
	src := `"""Date helpers."""
import datetime

MAX_YEAR = 9999


def is_leap_year(year: int) -> bool:
    return year % 4 == 0


@functools.cache
def days_in_month(year: int, month: int) -> int:
    return 29 if month == 2 and is_leap_year(year) else 30


class Calendar:
    def __init__(self, year):
        self.year = year

    def leap(self):
        return is_leap_year(self.year)

if __name__ == "__main__":
    print(is_leap_year(2024))
`
	chunks := New(Options{}).Chunk(walker.File{Path: "dates.py", Language: "python", Content: []byte(src)})
	assertChunks(t, chunks, []want{
		{"", "module", 1, 4},
		{"is_leap_year", "function", 7, 8},
		{"days_in_month", "function", 11, 13},
		{"Calendar", "class", 16, 21},
		{"", "module", 23, 24},
	})
	if !strings.HasPrefix(chunks[2].Content, "@functools.cache") {
		t.Errorf("decorator not included: %q", chunks[2].Content)
	}
}

func TestLargePythonClassIsSplitIntoMethods(t *testing.T) {
	var b strings.Builder
	b.WriteString("class Service:\n    \"\"\"Does things.\"\"\"\n    retries = 3\n\n")
	for _, m := range []string{"start", "stop"} {
		fmt.Fprintf(&b, "    def %s(self):\n", m)
		for i := 0; i < 8; i++ {
			fmt.Fprintf(&b, "        step_%d()\n", i)
		}
		b.WriteString("\n")
	}
	chunks := New(Options{MaxLines: 10}).Chunk(walker.File{Path: "svc.py", Language: "python", Content: []byte(b.String())})
	assertChunks(t, chunks, []want{
		{"Service", "class", 1, 3},
		{"Service.start", "method", 5, 13},
		{"Service.stop", "method", 15, 23},
	})
}

func TestGoDefinitions(t *testing.T) {
	src := `package server

import "net/http"

// Server serves HTTP.
type Server struct {
	addr string
}

type Stack[T any] []T

func New(addr string) *Server {
	return &Server{addr: addr}
}

func (s *Server) Start() error {
	return http.ListenAndServe(s.addr, nil)
}

func (s *Stack[T]) Push(v T) { *s = append(*s, v) }
`
	chunks := New(Options{}).Chunk(walker.File{Path: "server.go", Language: "go", Content: []byte(src)})
	assertChunks(t, chunks, []want{
		{"", "module", 1, 5},
		{"Server", "type", 6, 8},
		{"Stack", "type", 10, 10},
		{"New", "function", 12, 14},
		{"Server.Start", "method", 16, 18},
		{"Stack.Push", "method", 20, 20},
	})
}

func TestTypeScriptDefinitions(t *testing.T) {
	src := `import { Request } from "express";

export interface User {
  id: string;
}

export type Role = "admin" | "member";

export const login = async (req: Request) => {
  return req.body;
};

const LIMIT = 5;

export class RateLimiter {
  allow(key: string): boolean {
    return true;
  }
}

export default function handler() {}
`
	chunks := New(Options{}).Chunk(walker.File{Path: "auth.ts", Language: "typescript", Content: []byte(src)})
	assertChunks(t, chunks, []want{
		{"", "module", 1, 1},
		{"User", "interface", 3, 5},
		{"Role", "type", 7, 7},
		{"login", "function", 9, 11},
		{"", "module", 13, 13},
		{"RateLimiter", "class", 15, 19},
		{"handler", "function", 21, 21},
	})
}

func TestJavaScriptAndTSX(t *testing.T) {
	js := "function add(a, b) {\n  return a + b;\n}\n"
	tsx := "export function App() {\n  return <div>hi</div>;\n}\n"
	c := New(Options{})
	assertChunks(t, c.Chunk(walker.File{Path: "m.js", Language: "javascript", Content: []byte(js)}),
		[]want{{"add", "function", 1, 3}})
	assertChunks(t, c.Chunk(walker.File{Path: "App.tsx", Language: "tsx", Content: []byte(tsx)}),
		[]want{{"App", "function", 1, 3}})
}

func TestOversizedFunctionIsWindowed(t *testing.T) {
	var b strings.Builder
	b.WriteString("def huge():\n")
	for i := 0; i < 29; i++ {
		fmt.Fprintf(&b, "    x = %d\n", i)
	}
	chunks := New(Options{MaxLines: 20, WindowLines: 12, Overlap: 2}).Chunk(
		walker.File{Path: "h.py", Language: "python", Content: []byte(b.String())})
	assertChunks(t, chunks, []want{
		{"huge", "function", 1, 12},
		{"huge", "function", 11, 22},
		{"huge", "function", 21, 30},
	})
}

func TestFallbackWindows(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	chunks := New(Options{WindowLines: 10, Overlap: 3}).Chunk(
		walker.File{Path: "notes.md", Language: "markdown", Content: []byte(b.String())})
	assertChunks(t, chunks, []want{
		{"", "window", 1, 10},
		{"", "window", 8, 17},
		{"", "window", 15, 24},
		{"", "window", 22, 25},
	})
}

func TestHashCoversPathSymbolAndContent(t *testing.T) {
	a := Chunk{FilePath: "a.py", SymbolName: "f", SymbolKind: "function", Content: "def f(): pass"}
	b := a
	if a.Hash() != b.Hash() {
		t.Fatal("identical chunks must hash equally")
	}
	b.FilePath = "b.py"
	if a.Hash() == b.Hash() {
		t.Fatal("moving a function changes its embedding input, so its hash")
	}
	if !strings.Contains(a.EmbeddingText(), "function: f") {
		t.Errorf("embedding text lacks symbol header: %q", a.EmbeddingText())
	}
}

func TestEmptyAndBrokenInput(t *testing.T) {
	c := New(Options{})
	if got := c.Chunk(walker.File{Path: "e.py", Language: "python", Content: []byte("\n\n")}); len(got) != 0 {
		t.Errorf("blank file produced %v", summarize(got))
	}
	// tree-sitter recovers from syntax errors; we still get chunks.
	broken := "def ok():\n    return 1\n\ndef broken(:\n    pass\n"
	if got := c.Chunk(walker.File{Path: "b.py", Language: "python", Content: []byte(broken)}); len(got) == 0 {
		t.Error("broken file produced no chunks")
	}
}
