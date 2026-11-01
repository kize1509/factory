package gateway

import (
	"strings"
	"sync"
)

// Price is US dollars per million tokens.
type Price struct{ In, Out, CacheRead, CacheWrite float64 }

// live holds the prices synced from models.dev (or set by hand in Admin →
// Prices), keyed by model id without the provider.
var (
	mu   sync.RWMutex
	live = map[string]Price{}
)

// SetPrices replaces the synced prices; keys are "provider/id" or "id".
func SetPrices(list map[string]Price) {
	m := map[string]Price{}
	for k, p := range list {
		if _, id, ok := strings.Cut(k, "/"); ok {
			k = id
		}
		m[k] = p
	}
	mu.Lock()
	live = m
	mu.Unlock()
}

// fallback is only used for a model the price source doesn't list yet,
// matched by name prefix. Values are models.dev's on 2026-11-01.
var fallback = map[string]Price{
	"claude-opus":   {4, 20, 0.2, 5},
	"claude-sonnet": {2, 10, 0.1, 2.5},
	"claude-haiku":  {0.1, 0.5, 0.01, 0.125},
	"claude-fable":  {10, 50, 0.25, 12.5},
	"gpt-6.1-sol":   {2, 10, 0.1, 2.5},
	"gpt-6.1-terra": {1, 5, 0.05, 1.25},
	"gpt-6-luna":    {0.1, 0.5, 0.01, 0.125},
}

// PriceFor finds a model's price: the synced price for the exact model,
// else the fallback with the longest matching prefix.
func PriceFor(model string) Price {
	mu.RLock()
	p, ok := live[model]
	mu.RUnlock()
	if ok {
		return p
	}
	best, n := Price{}, 0
	for prefix, fp := range fallback {
		if strings.HasPrefix(model, prefix) && len(prefix) > n {
			best, n = fp, len(prefix)
		}
	}
	return best
}

type Usage struct {
	Model                                string
	Input, Output, CacheRead, CacheWrite int
}

func (u Usage) USD() float64 {
	return Cost(u.Model, float64(u.Input), float64(u.Output), float64(u.CacheRead), float64(u.CacheWrite))
}

// Cost is the dollars for a number of tokens of each kind.
func Cost(model string, in, out, cacheRead, cacheWrite float64) float64 {
	p := PriceFor(model)
	return (in*p.In + out*p.Out + cacheRead*p.CacheRead + cacheWrite*p.CacheWrite) / 1e6
}
