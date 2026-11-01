// Package prices pulls current model prices (US dollars per million tokens)
// from models.dev, with LiteLLM's price list as a fallback.
package prices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"factory/internal/store"
)

const (
	ModelsDev = "https://models.dev/api.json"
	LiteLLM   = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
)

var providers = []string{"anthropic", "openai"}

func get(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Fetch returns prices for every Anthropic and OpenAI model the source lists.
func Fetch(ctx context.Context) ([]store.Price, string, error) {
	if list, err := fromModelsDev(ctx); err == nil && len(list) > 0 {
		return list, "models.dev", nil
	}
	list, err := fromLiteLLM(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("no price source answered: %w", err)
	}
	return list, "litellm", nil
}

func fromModelsDev(ctx context.Context) ([]store.Price, error) {
	body, err := get(ctx, ModelsDev)
	if err != nil {
		return nil, err
	}
	var all map[string]struct {
		Models map[string]struct {
			Cost struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cache_read"`
				CacheWrite float64 `json:"cache_write"`
			} `json:"cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, err
	}
	var out []store.Price
	for _, p := range providers {
		for id, m := range all[p].Models {
			if m.Cost.Input == 0 && m.Cost.Output == 0 {
				continue
			}
			out = append(out, store.Price{Model: p + "/" + id, Input: m.Cost.Input, Output: m.Cost.Output,
				CacheRead: m.Cost.CacheRead, CacheWrite: m.Cost.CacheWrite, Source: "models.dev"})
		}
	}
	return out, nil
}

func fromLiteLLM(ctx context.Context) ([]store.Price, error) {
	body, err := get(ctx, LiteLLM)
	if err != nil {
		return nil, err
	}
	var all map[string]struct {
		Provider   string  `json:"litellm_provider"`
		In         float64 `json:"input_cost_per_token"`
		Out        float64 `json:"output_cost_per_token"`
		CacheRead  float64 `json:"cache_read_input_token_cost"`
		CacheWrite float64 `json:"cache_creation_input_token_cost"`
	}
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, err
	}
	var out []store.Price
	for id, m := range all {
		if (m.Provider != "anthropic" && m.Provider != "openai") || m.In == 0 {
			continue
		}
		out = append(out, store.Price{Model: m.Provider + "/" + id, Input: m.In * 1e6, Output: m.Out * 1e6,
			CacheRead: m.CacheRead * 1e6, CacheWrite: m.CacheWrite * 1e6, Source: "litellm"})
	}
	return out, nil
}
