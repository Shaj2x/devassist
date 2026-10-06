// Package chunker splits source files into search-sized pieces.
//
// For Python, Go, JavaScript and TypeScript it parses the file with
// tree-sitter and emits one chunk per top-level definition (function, class,
// method, type), so a search hit is a meaningful unit with a name and exact
// line range. Code between definitions (imports, constants, scripts) becomes
// "module" chunks. Oversized definitions are split into overlapping windows,
// and large classes are split into their methods. Every other language falls
// back to fixed-size overlapping line windows.
package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Shaj2x/devassist/services/indexer/internal/walker"
)

// Chunk is one indexable piece of a file. Lines are 1-based and inclusive.
type Chunk struct {
	FilePath   string
	Language   string
	SymbolName string // "" for module code and windows
	SymbolKind string // function, method, class, type, interface, enum, module, window
	StartLine  int
	EndLine    int
	Content    string
}

// EmbeddingText is what gets embedded: a short header giving the model the
// file and symbol context, then the code itself.
func (c Chunk) EmbeddingText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\n", c.FilePath)
	if c.SymbolName != "" {
		fmt.Fprintf(&b, "%s: %s\n", c.SymbolKind, c.SymbolName)
	}
	b.WriteString("\n")
	b.WriteString(c.Content)
	return b.String()
}

// Hash identifies the embedding input. Two chunks with the same hash have the
// same embedding, which is what makes incremental reindexing cheap.
func (c Chunk) Hash() string {
	sum := sha256.Sum256([]byte(c.EmbeddingText()))
	return hex.EncodeToString(sum[:])
}

// TokenEstimate is a cheap approximation (about 4 characters per token).
func (c Chunk) TokenEstimate() int {
	return (len(c.Content) + 3) / 4
}

// Options tune chunk sizes.
type Options struct {
	MaxLines    int // definitions longer than this are split (default 120)
	WindowLines int // window size for fallback and splitting (default 60)
	Overlap     int // lines shared by consecutive windows (default 10)
}

func (o Options) withDefaults() Options {
	if o.MaxLines <= 0 {
		o.MaxLines = 120
	}
	if o.WindowLines <= 0 {
		o.WindowLines = 60
	}
	if o.Overlap < 0 || o.Overlap >= o.WindowLines {
		o.Overlap = 10
	}
	return o
}

// Chunker is safe for concurrent use; each call creates its own parser.
type Chunker struct {
	opts Options
}

func New(opts Options) *Chunker {
	return &Chunker{opts: opts.withDefaults()}
}

// Chunk splits one file.
func (c *Chunker) Chunk(f walker.File) []Chunk {
	lines := splitLines(string(f.Content))
	if spec, ok := specs[f.Language]; ok {
		if chunks, ok := c.syntaxChunks(f, lines, spec); ok {
			return chunks
		}
	}
	return c.windows(f, lines, 1, len(lines), "", "window")
}

// windows splits lines[start..end] (1-based, inclusive) into overlapping
// windows. Blank-only windows are dropped.
func (c *Chunker) windows(f walker.File, lines []string, start, end int, name, kind string) []Chunk {
	var out []Chunk
	step := c.opts.WindowLines - c.opts.Overlap
	for from := start; from <= end; from += step {
		to := min(from+c.opts.WindowLines-1, end)
		if ch, ok := makeChunk(f, lines, from, to, name, kind); ok {
			out = append(out, ch)
		}
		if to == end {
			break
		}
	}
	return out
}

// emit adds one definition, splitting it into windows if it is too long.
func (c *Chunker) emit(out []Chunk, f walker.File, lines []string, start, end int, name, kind string) []Chunk {
	if end-start+1 > c.opts.MaxLines {
		return append(out, c.windows(f, lines, start, end, name, kind)...)
	}
	if ch, ok := makeChunk(f, lines, start, end, name, kind); ok {
		out = append(out, ch)
	}
	return out
}

func makeChunk(f walker.File, lines []string, start, end int, name, kind string) (Chunk, bool) {
	start, end = max(start, 1), min(end, len(lines))
	// Tighten the range so reported line numbers point at code, not blanks.
	for start <= end && strings.TrimSpace(lines[start-1]) == "" {
		start++
	}
	for end >= start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start > end {
		return Chunk{}, false
	}
	content := strings.Join(lines[start-1:end], "\n")
	return Chunk{
		FilePath: f.Path, Language: f.Language, SymbolName: name, SymbolKind: kind,
		StartLine: start, EndLine: end, Content: content,
	}, true
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
