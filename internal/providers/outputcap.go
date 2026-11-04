package providers

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// outputCap remembers, for one provider, the most output tokens its model
// takes. Prism asks for a generous limit (and twice that when a response is
// cut off), which some models refuse outright: Anthropic answers "max_tokens:
// 65536 > 64000, which is the maximum allowed", OpenAI "supports at most
// 32768 completion tokens". The cap in the refusal is learned and used for
// that request and every later one, so a model with a lower ceiling reviews
// at its ceiling instead of failing.
type outputCap struct {
	max atomic.Int64 // 0: none learned
}

// limit is maxTokens within the learned cap.
func (c *outputCap) limit(maxTokens int) int {
	if m := int(c.max.Load()); m > 0 && maxTokens > m {
		return m
	}
	return maxTokens
}

// sendCapped sends req with its MaxTokens within the cap, and when the
// model refuses that limit and names its own, learns it and sends again at
// it, at most twice (a refusal can name an exclusive bound).
func sendCapped(req ReviewRequest, c *outputCap, send func(ReviewRequest) (ReviewResponse, error)) (ReviewResponse, error) {
	req.MaxTokens = c.limit(req.MaxTokens)
	resp, err := send(req)
	for range 2 {
		capTokens, ok := refusedOutputLimit(err, req.MaxTokens)
		if !ok {
			break
		}
		c.max.Store(int64(capTokens))
		req.MaxTokens = capTokens
		resp, err = send(req)
	}
	return resp, err
}

// outputLimitFields are the request fields an output-limit refusal names:
// OpenAI's two, Anthropic's, Gemini's (in either case).
var outputLimitFields = []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maxoutputtokens"}

var (
	limitNumber = regexp.MustCompile(`\d[\d,]*`)
	// limitRange is "[1, 8193)": its upper bound is exclusive.
	limitRange = regexp.MustCompile(`\[\s*\d[\d,]*\s*,\s*(\d[\d,]*)\s*\)`)
)

// refusedOutputLimit reports whether err is the model refusing requested as
// its output limit, and the most it takes: the largest number in the refusal
// below requested (one less when it is the exclusive end of a range,
// "[1, 8193)"). Numbers under 256 are model names and versions, not limits;
// no model with a smaller cap could answer a review anyway.
func refusedOutputLimit(err error, requested int) (int, bool) {
	var re *requestError
	if !errors.As(err, &re) || (re.status != 400 && re.status != 422) || requested <= 0 {
		return 0, false
	}
	lower := strings.ToLower(re.body)
	named := false
	for _, f := range outputLimitFields {
		if strings.Contains(lower, f) {
			named = true
			break
		}
	}
	if !named {
		return 0, false
	}
	exclusive := map[int]bool{} // start offsets of half-open ranges' ends
	for _, m := range limitRange.FindAllStringSubmatchIndex(re.body, -1) {
		exclusive[m[2]] = true
	}
	best := 0
	for _, m := range limitNumber.FindAllStringIndex(re.body, -1) {
		n, err := strconv.Atoi(strings.ReplaceAll(re.body[m[0]:m[1]], ",", ""))
		if err != nil {
			continue
		}
		if exclusive[m[0]] {
			n--
		}
		if n >= 256 && n < requested {
			best = max(best, n)
		}
	}
	return best, best > 0
}
