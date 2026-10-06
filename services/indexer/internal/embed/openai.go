package embed

import (
	"context"
	"net/http"
	"strings"
)

// OpenAI calls the /embeddings endpoint. text-embedding-3 models accept a
// `dimensions` parameter, so we request exactly the column size.
type OpenAI struct {
	baseURL, apiKey, model string
	dims                   int
	opts                   HTTPOptions
	client                 *http.Client
}

func NewOpenAI(baseURL, apiKey, model string, dims int, opts HTTPOptions) *OpenAI {
	return &OpenAI{
		baseURL: strings.TrimSuffix(baseURL, "/"), apiKey: apiKey, model: model, dims: dims,
		opts: opts.withDefaults(), client: &http.Client{},
	}
}

func (o *OpenAI) Model() string   { return "openai/" + o.model }
func (o *OpenAI) Dimensions() int { return o.dims }

func (o *OpenAI) Embed(ctx context.Context, texts []string, _ InputType) ([][]float32, error) {
	req := map[string]any{"model": o.model, "input": texts, "dimensions": o.dims}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := postJSON(ctx, o.client, o.opts, o.baseURL+"/embeddings", o.apiKey, req, &resp); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index >= 0 && d.Index < len(out) {
			normalize(d.Embedding)
			out[d.Index] = d.Embedding
		}
	}
	return out, checkShape(out, len(texts), o.dims)
}
