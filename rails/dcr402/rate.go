package dcr402rail

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateSource reports the current DCR/USD exchange rate (USD per 1 DCR).
type RateSource interface {
	USDPerDCR(ctx context.Context) (float64, error)
}

// DefaultRateURL is the public dcrdata API base used when no override is
// configured.
const DefaultRateURL = "https://explorer.dcrdata.org/api"

// DcrdataRate fetches the rate from a dcrdata instance's exchangerate endpoint.
type DcrdataRate struct {
	// BaseURL is the dcrdata API base (e.g. https://explorer.dcrdata.org/api).
	BaseURL string
	// HTTP is the transport; default http.Client with a 10s timeout.
	HTTP *http.Client
}

func (d *DcrdataRate) USDPerDCR(ctx context.Context) (float64, error) {
	base := strings.TrimRight(d.BaseURL, "/")
	if base == "" {
		base = DefaultRateURL
	}
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/exchangerate", nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("dcr402: rate fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("dcr402: rate source answered %d", resp.StatusCode)
	}
	var out struct {
		Index    string  `json:"index"`
		DCRPrice float64 `json:"dcrPrice"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("dcr402: rate response: %w", err)
	}
	if out.DCRPrice <= 0 {
		return 0, fmt.Errorf("dcr402: rate source returned %f", out.DCRPrice)
	}
	if out.Index != "" && out.Index != "USD" {
		return 0, fmt.Errorf("dcr402: rate index %q, want USD", out.Index)
	}
	return out.DCRPrice, nil
}

// CachedRate serves a TTL-cached rate, holding the last good value when the
// source errs and falling back to a static floor when no good value exists.
type CachedRate struct {
	Source RateSource
	// TTL bounds staleness of a cached value (default 5m).
	TTL time.Duration
	// Fallback is the static USD-per-DCR floor used when the source has never
	// succeeded (0 = none, errors instead).
	Fallback float64
	Now      func() time.Time

	mu      sync.Mutex
	rate    float64
	fetched time.Time
}

func (c *CachedRate) USDPerDCR(ctx context.Context) (float64, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rate > 0 && now().Sub(c.fetched) < ttl {
		return c.rate, nil
	}
	rate, err := c.Source.USDPerDCR(ctx)
	if err == nil && rate > 0 {
		c.rate, c.fetched = rate, now()
		return rate, nil
	}
	// Source unavailable: serve the last good value regardless of age, then the
	// configured floor.
	if c.rate > 0 {
		return c.rate, nil
	}
	if c.Fallback > 0 {
		return c.Fallback, nil
	}
	if err == nil {
		err = fmt.Errorf("dcr402: rate source returned %f", rate)
	}
	return 0, err
}

// dustFloorAtoms is the minimum invoice size; sub-dust conversions round up to
// it so every challenge is payable.
const dustFloorAtoms = 1_000

// AtomsForUSDMicros converts a USD amount (micros, 1e-6 USD) to DCR atoms
// (1e-8 DCR) at usdPerDCR, rounding up in the seller's favor.
func AtomsForUSDMicros(usdMicros int64, usdPerDCR float64) (int64, error) {
	if usdMicros <= 0 {
		return 0, fmt.Errorf("dcr402: amount must be positive, got %d", usdMicros)
	}
	if usdPerDCR <= 0 {
		return 0, fmt.Errorf("dcr402: exchange rate must be positive, got %f", usdPerDCR)
	}
	atoms := int64(math.Ceil(float64(usdMicros) * 100 / usdPerDCR))
	if atoms < dustFloorAtoms {
		atoms = dustFloorAtoms
	}
	return atoms, nil
}
