package review

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/dshills/prism/internal/config"
	"github.com/dshills/prism/internal/providers"
)

// TokenUsage is the tokens one model used in a review, with an estimated
// cost when its price is known. Agents run prism on every commit and every
// pass of a fix loop, so cost adds up unseen without it.
type TokenUsage struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	providers.Usage
	// CostUSD estimates the cost in US dollars from the model's price per
	// million tokens; absent when the price is not known. Cached input is
	// counted at the full input price, so it is an upper bound.
	CostUSD *float64 `json:"costUSD,omitempty"`
}

// usageLedger sums token usage by model ("provider:model").
type usageLedger map[string]*TokenUsage

func (l *usageLedger) add(provider, model string, u providers.Usage) {
	if u == (providers.Usage{}) {
		return
	}
	if *l == nil {
		*l = usageLedger{}
	}
	key := provider + ":" + model
	t := (*l)[key]
	if t == nil {
		t = &TokenUsage{Provider: provider, Model: model}
		(*l)[key] = t
	}
	t.Add(u)
}

func (l *usageLedger) merge(o usageLedger) {
	for _, t := range o {
		l.add(t.Provider, t.Model, t.Usage)
	}
}

// list is the ledger's entries in a stable order.
func (l usageLedger) list() []TokenUsage {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]TokenUsage, 0, len(keys))
	for _, k := range keys {
		out = append(out, *l[k])
	}
	return out
}

// mergeTokens adds b's entries to a, model by model, summing the costs when
// both are known.
func mergeTokens(a, b []TokenUsage) []TokenUsage {
	for _, t := range b {
		i := slices.IndexFunc(a, func(x TokenUsage) bool { return x.Provider == t.Provider && x.Model == t.Model })
		if i < 0 {
			a = append(a, t)
			continue
		}
		a[i].Add(t.Usage)
		if a[i].CostUSD != nil && t.CostUSD != nil {
			sum := *a[i].CostUSD + *t.CostUSD
			a[i].CostUSD = &sum
		} else {
			a[i].CostUSD = nil
		}
	}
	return a
}

// builtinPrices are the prices prism knows, in US dollars per million tokens
// (input, output), keyed by "provider:model". The Claude prices are
// Anthropic's first-party API rates as of 2026-09-25. Other providers'
// models are priced through the prices setting.
var builtinPrices = map[string]config.Price{
	"anthropic:claude-fable-5-1":  {Input: 10, Output: 50},
	"anthropic:claude-fable-5":    {Input: 10, Output: 50},
	"anthropic:claude-opus-5-5":   {Input: 4, Output: 20},
	"anthropic:claude-opus-5":     {Input: 5, Output: 25},
	"anthropic:claude-opus-4-8":   {Input: 5, Output: 25},
	"anthropic:claude-opus-4-7":   {Input: 5, Output: 25},
	"anthropic:claude-opus-4-6":   {Input: 5, Output: 25},
	"anthropic:claude-sonnet-5-5": {Input: 2, Output: 10},
	"anthropic:claude-sonnet-5":   {Input: 2, Output: 10},
	"anthropic:claude-sonnet-4-6": {Input: 3, Output: 15},
	"anthropic:claude-haiku-4-5":  {Input: 1, Output: 5},
}

// dateSuffix is a dated snapshot's suffix, as in claude-haiku-4-5-20251001.
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// priceOf is a model's price: configured, then built in (dated snapshots
// priced as their model), and free for a local Ollama or LM Studio model.
func priceOf(provider, model string, configured map[string]config.Price) (config.Price, bool) {
	for _, key := range []string{provider + ":" + model, provider + ":" + dateSuffix.ReplaceAllString(model, "")} {
		if p, ok := configured[key]; ok {
			return p, true
		}
		if p, ok := builtinPrices[key]; ok {
			return p, true
		}
	}
	if provider == "ollama" || provider == "lmstudio" {
		return config.Price{}, true
	}
	return config.Price{}, false
}

// priceTokens sets each entry's estimated cost where its price is known.
func priceTokens(tokens []TokenUsage, configured map[string]config.Price) {
	for i, t := range tokens {
		p, ok := priceOf(t.Provider, t.Model, configured)
		if !ok {
			tokens[i].CostUSD = nil
			continue
		}
		cost := (float64(t.InputTokens)*p.Input + float64(t.OutputTokens)*p.Output) / 1e6
		tokens[i].CostUSD = &cost
	}
}

// TokensLine is the one-line token statement text and markdown reports
// print, or "" when no tokens were used (nothing sent, or all from cache).
// The cost is shown only when every model's price is known.
func (c Coverage) TokensLine() string {
	var total providers.Usage
	cost, priced := 0.0, true
	for _, t := range c.Tokens {
		total.Add(t.Usage)
		if t.CostUSD == nil {
			priced = false
		} else {
			cost += *t.CostUSD
		}
	}
	if total == (providers.Usage{}) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tokens: %s in", thousands(total.InputTokens))
	if total.CachedInputTokens > 0 {
		fmt.Fprintf(&b, " (%s cached)", thousands(total.CachedInputTokens))
	}
	fmt.Fprintf(&b, " / %s out", thousands(total.OutputTokens))
	if total.ReasoningTokens > 0 {
		fmt.Fprintf(&b, " (%s reasoning)", thousands(total.ReasoningTokens))
	}
	if priced {
		fmt.Fprintf(&b, " — ~$%.2f", cost)
	}
	return b.String()
}

// thousands formats n with comma separators.
func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
