package embed

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Local is a feature-hashing embedder: it splits text into identifier
// tokens (camelCase and snake_case aware), hashes each token and each pair
// of adjacent tokens into a fixed-size vector with a random sign, applies
// sublinear term frequency, and L2-normalizes.
//
// It captures lexical overlap ("leap year" finds is_leap_year) but not
// meaning ("authentication" will not find login). It exists so the whole
// system runs offline and in CI with no API key; use a real provider for
// quality.
type Local struct{ dims int }

func NewLocal(dims int) *Local { return &Local{dims: dims} }

func (l *Local) Model() string   { return "local/hash-v1" }
func (l *Local) Dimensions() int { return l.dims }

func (l *Local) Embed(_ context.Context, texts []string, _ InputType) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = l.embedOne(t)
	}
	return out, nil
}

func (l *Local) embedOne(text string) []float32 {
	counts := map[string]float64{}
	tokens := Tokenize(text)
	for i, tok := range tokens {
		counts[tok]++
		if i > 0 {
			counts[tokens[i-1]+" "+tok] += 0.5
		}
	}
	v := make([]float32, l.dims)
	for feature, tf := range counts {
		h := fnv.New64a()
		_, _ = h.Write([]byte(feature))
		sum := h.Sum64()
		idx := int(sum % uint64(l.dims)) //nolint:gosec // dims is a small positive config value
		sign := float32(1)
		if sum>>63 == 1 {
			sign = -1
		}
		v[idx] += sign * float32(1+math.Log(tf))
	}
	normalize(v)
	return v
}

// stopwords are tokens too common in code to carry signal.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "to": true,
	"in": true, "is": true, "for": true, "def": true, "return": true, "self": true,
	"func": true, "function": true, "const": true, "let": true, "var": true, "if": true,
	"else": true, "import": true, "from": true, "package": true, "file": true,
}

// Tokenize lowercases and splits on non-alphanumerics and camelCase
// boundaries: "parseISODate(dateStr)" -> parse, iso, date, date, str.
func Tokenize(text string) []string {
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) > 1 {
			tok := strings.ToLower(string(cur))
			if !stopwords[tok] {
				tokens = append(tokens, tok)
			}
		}
		cur = cur[:0]
	}
	runes := []rune(text)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) > 0 && unicode.IsUpper(r) {
			prev := cur[len(cur)-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			// fooBar -> foo|Bar ; ISODate -> ISO|Date
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return tokens
}
