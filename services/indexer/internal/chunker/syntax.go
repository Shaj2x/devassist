package chunker

import (
	"regexp"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/Shaj2x/devassist/services/indexer/internal/walker"
)

// langSpec describes which syntax nodes are definitions in one language.
type langSpec struct {
	language *sitter.Language
	// Node kind -> symbol kind for top-level definitions.
	defs map[string]string
	// Wrapper kinds (decorators, `export`) and the field holding the real node.
	wrappers map[string]string
	// Class-like kinds that are split into methods when too long, with the
	// field holding the body and the kinds of method nodes inside it.
	classBody   map[string]string
	methodKinds map[string]bool
}

var jsDefs = map[string]string{
	"function_declaration":           "function",
	"generator_function_declaration": "function",
	"class_declaration":              "class",
	"abstract_class_declaration":     "class",
	"interface_declaration":          "interface",
	"type_alias_declaration":         "type",
	"enum_declaration":               "enum",
	"lexical_declaration":            "function", // only when it binds a function; see definitionName
}

func jsSpec(lang *sitter.Language) langSpec {
	return langSpec{
		language:    lang,
		defs:        jsDefs,
		wrappers:    map[string]string{"export_statement": "declaration"},
		classBody:   map[string]string{"class_declaration": "body", "abstract_class_declaration": "body"},
		methodKinds: map[string]bool{"method_definition": true},
	}
}

var specs = map[string]langSpec{
	"python": {
		language:    sitter.NewLanguage(tspython.Language()),
		defs:        map[string]string{"function_definition": "function", "class_definition": "class"},
		wrappers:    map[string]string{"decorated_definition": "definition"},
		classBody:   map[string]string{"class_definition": "body"},
		methodKinds: map[string]bool{"function_definition": true, "decorated_definition": true},
	},
	"go": {
		language: sitter.NewLanguage(tsgo.Language()),
		defs: map[string]string{
			"function_declaration": "function",
			"method_declaration":   "method",
			"type_declaration":     "type",
		},
	},
	"javascript": jsSpec(sitter.NewLanguage(tsjs.Language())),
	"typescript": jsSpec(sitter.NewLanguage(tsts.LanguageTypescript())),
	"tsx":        jsSpec(sitter.NewLanguage(tsts.LanguageTSX())),
}

// syntaxChunks returns definition and module chunks, or ok=false if parsing
// failed (the caller then falls back to windows).
func (c *Chunker) syntaxChunks(f walker.File, lines []string, spec langSpec) ([]Chunk, bool) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(spec.language); err != nil {
		return nil, false
	}
	tree := parser.Parse(f.Content, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()
	root := tree.RootNode()

	var out []Chunk
	gapStart := 1 // first line of module code not yet emitted
	flushGap := func(before int) {
		if before > gapStart {
			out = append(out, c.windows(f, lines, gapStart, before-1, "", "module")...)
		}
	}

	for i := uint(0); i < root.NamedChildCount(); i++ {
		outer := root.NamedChild(i)
		def := unwrap(outer, spec)
		kind, isDef := spec.defs[def.Kind()]
		name := definitionName(def, f.Content)
		if !isDef || name == "" {
			continue // module-level code; picked up by the gap logic
		}
		start, end := lineRange(outer)
		flushGap(start)
		gapStart = end + 1

		if bodyField, ok := spec.classBody[def.Kind()]; ok && end-start+1 > c.opts.MaxLines {
			out = c.splitClass(out, f, lines, def, start, end, name, bodyField, spec)
			continue
		}
		out = c.emit(out, f, lines, start, end, name, kind)
	}
	flushGap(len(lines) + 1)
	return out, true
}

// splitClass emits the class header (everything before the first method) as
// a class chunk and each method as "Class.method".
func (c *Chunker) splitClass(out []Chunk, f walker.File, lines []string, class *sitter.Node,
	start, end int, className, bodyField string, spec langSpec) []Chunk {
	body := class.ChildByFieldName(bodyField)
	if body == nil {
		return c.emit(out, f, lines, start, end, className, "class")
	}
	headerEnd := end
	var methods []Chunk
	for i := uint(0); i < body.NamedChildCount(); i++ {
		outer := body.NamedChild(i)
		if !spec.methodKinds[outer.Kind()] {
			continue
		}
		method := unwrap(outer, spec)
		name := definitionName(method, f.Content)
		if name == "" {
			continue
		}
		mStart, mEnd := lineRange(outer)
		headerEnd = min(headerEnd, mStart-1)
		methods = c.emit(methods, f, lines, mStart, mEnd, className+"."+name, "method")
	}
	out = c.emit(out, f, lines, start, headerEnd, className, "class")
	return append(out, methods...)
}

func unwrap(n *sitter.Node, spec langSpec) *sitter.Node {
	for {
		field, ok := spec.wrappers[n.Kind()]
		if !ok {
			return n
		}
		inner := n.ChildByFieldName(field)
		if inner == nil {
			return n
		}
		n = inner
	}
}

var receiverType = regexp.MustCompile(`([A-Za-z_]\w*)\s*(?:\[[^\]]*\])?\s*\)\s*$`)

// definitionName returns the symbol name, or "" if the node is not a named
// definition (e.g. `const x = 5` is module code, not a function).
func definitionName(n *sitter.Node, src []byte) string {
	switch n.Kind() {
	case "type_declaration": // Go: type Foo struct{...}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if spec := n.NamedChild(i); spec.Kind() == "type_spec" || spec.Kind() == "type_alias" {
				if name := spec.ChildByFieldName("name"); name != nil {
					return name.Utf8Text(src)
				}
			}
		}
		return ""
	case "method_declaration": // Go: func (s *Server) Start()
		name := n.ChildByFieldName("name")
		recv := n.ChildByFieldName("receiver")
		if name == nil {
			return ""
		}
		if recv != nil {
			if m := receiverType.FindStringSubmatch(recv.Utf8Text(src)); m != nil {
				return m[1] + "." + name.Utf8Text(src)
			}
		}
		return name.Utf8Text(src)
	case "lexical_declaration": // JS/TS: const handler = async (req) => {...}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			decl := n.NamedChild(i)
			if decl.Kind() != "variable_declarator" {
				continue
			}
			value := decl.ChildByFieldName("value")
			name := decl.ChildByFieldName("name")
			if value != nil && name != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression" || value.Kind() == "function") {
				return name.Utf8Text(src)
			}
		}
		return ""
	}
	if name := n.ChildByFieldName("name"); name != nil {
		return name.Utf8Text(src)
	}
	return ""
}

// lineRange converts a node's span to 1-based inclusive lines.
func lineRange(n *sitter.Node) (int, int) {
	start := int(n.StartPosition().Row) + 1 //nolint:gosec // line numbers are far below MaxInt
	endPos := n.EndPosition()
	end := int(endPos.Row) + 1 //nolint:gosec // line numbers are far below MaxInt
	if endPos.Column == 0 && end > start {
		end-- // node ends at the very start of the next line
	}
	return start, end
}
