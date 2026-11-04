package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Config configures a Prober.
type Config struct {
	// TimeoutMs bounds each probe, in milliseconds.
	TimeoutMs int
}

// Prober checks that endpoints answer.
type Prober struct {
	cfg    Config
	client *http.Client
}

// NewProber returns a prober with cfg.
func NewProber(cfg Config) *Prober {
	return &Prober{cfg: cfg, client: &http.Client{}}
}

func (p *Prober) probe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

// ProbeAll probes every endpoint at once and returns each one's error (nil
// when healthy). A slow endpoint no longer delays the others.
func (p *Prober) ProbeAll(parent context.Context, endpoints []string) map[string]error {
	timeout := time.Duration(p.cfg.TimeoutMs) * time.Second
	results := make(map[string]error, len(endpoints))
	var wg sync.WaitGroup
	for _, ep := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, timeout)
			defer cancel()
			results[ep] = p.probe(ctx, ep)
		}()
	}
	wg.Wait()
	return results
}
