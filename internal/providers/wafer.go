package providers

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultWaferURL is Wafer's OpenAI-compatible API (docs.wafer.ai).
const defaultWaferURL = "https://pass.wafer.ai/v1"

// NewWafer creates a provider for Wafer (wafer.ai), which serves many open
// models (GLM, DeepSeek, Kimi, Qwen) through the OpenAI chat completions
// API: structured output by response_format, and reasoning_effort. It needs
// WAFER_API_KEY; WAFER_API_URL overrides the API's base URL. Model ids are
// Wafer's own ("GLM-5.3", "DeepSeek-V4.1-Flash"), sent as given; Wafer
// matches them without regard to case.
func NewWafer(model string) (*OpenAI, error) {
	key := os.Getenv("WAFER_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("WAFER_API_KEY environment variable is not set")
	}
	return &OpenAI{
		name:    "wafer",
		apiKey:  key,
		model:   model,
		baseURL: waferChatURL(os.Getenv("WAFER_API_URL")),
		// Reasoning models can think for minutes before answering.
		client: &http.Client{Timeout: 300 * time.Second},
	}, nil
}

// waferChatURL is the chat completions endpoint for a WAFER_API_URL, which
// may be the API's base ("https://pass.wafer.ai/v1") or the endpoint itself.
func waferChatURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultWaferURL
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}
