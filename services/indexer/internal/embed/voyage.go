package embed

import (
	"context"
	"net/http"
	"strings"
)

// Voyage calls Voyage AI's /embeddings endpoint (voyage-code-3 is trained
// for code retrieval). It distinguishes queries from documents.
type Voyage struct {
	baseURL, apiKey, model string
	dims                   int
	opts                   HTTPOptions
	client                 *http.Client
}

func NewVoyage(baseURL, apiKey, model string, dims int, opts HTTPOptions) *Voyage {
	return &Voyage{
		baseURL: strings.TrimSuffix(baseURL, "/"), apiKey: apiKey, model: model, dims: dims,
		opts: opts.withDefaults(), client: &http.Client{},
	}
}

func (v *Voyage) Model() string   { return "voyage/" + v.model }
func (v *Voyage) Dimensions() int { return v.dims }

func (v *Voyage) Embed(ctx context.Context, texts []string, inputType InputType) ([][]float32, error) {
	req := map[string]any{
		"model": v.model, "input": texts, "input_type": string(inputType), "output_dimension": v.dims,
	}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := postJSON(ctx, v.client, v.opts, v.baseURL+"/embeddings", v.apiKey, req, &resp); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index >= 0 && d.Index < len(out) {
			normalize(d.Embedding)
			out[d.Index] = d.Embedding
		}
	}
	return out, checkShape(out, len(texts), v.dims)
}
