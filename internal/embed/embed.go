// Package embed is a thin client for OpenAI's embeddings API, used to power
// flightdeck's semantic search. It is intentionally optional: with no API key
// configured the client reports Enabled()==false and the rest of the system
// degrades to lexical (FTS + trigram) search.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Dims is the embedding dimensionality stored in the items.embedding column.
// It must match the vector(N) size in the migration. text-embedding-3-small is
// natively 1536-dimensional.
const Dims = 1536

const defaultModel = "text-embedding-3-small"

// Client embeds text via OpenAI. The zero/disabled client is safe to call:
// Embed returns an error only when Enabled.
type Client struct {
	apiKey string
	model  string
	base   string
	hc     *http.Client
}

// NewFromEnv builds a Client from OPENAI_API_KEY and FLIGHTDECK_EMBED_MODEL
// (default text-embedding-3-small). When the key is empty the client is
// disabled.
func NewFromEnv() *Client {
	model := os.Getenv("FLIGHTDECK_EMBED_MODEL")
	if model == "" {
		model = defaultModel
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &Client{
		apiKey: os.Getenv("OPENAI_API_KEY"),
		model:  model,
		base:   base,
		hc:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool { return c != nil && c.apiKey != "" }

// Model returns the configured embedding model name (stored alongside vectors so
// a later model change can be detected and re-embedded).
func (c *Client) Model() string { return c.model }

// APIError is a non-2xx response from the embeddings API, typed so callers can
// distinguish rejected input (4xx — retrying is pointless) from transient
// failures (5xx, rate limits, network) that should be retried.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("embed: openai status %d: %s", e.StatusCode, e.Message)
}

// IsPoison reports whether err means the input itself was rejected (a 4xx other
// than 429), i.e. retrying the same content will never succeed.
func IsPoison(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) &&
		apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 &&
		apiErr.StatusCode != http.StatusTooManyRequests
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Embed returns one embedding per input string, index-aligned with inputs.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("embed: no OPENAI_API_KEY configured")
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: c.model, Input: inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := "unknown error"
		if out.Error != nil {
			msg = out.Error.Message
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	if len(out.Data) != len(inputs) {
		return nil, fmt.Errorf("embed: expected %d embeddings, got %d", len(inputs), len(out.Data))
	}
	// data is index-aligned per the API contract, but sort defensively.
	vecs := make([][]float32, len(inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("embed: out-of-range index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}
