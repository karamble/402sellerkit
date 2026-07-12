// Package prices is the single source of truth for what a service charges: a
// resource -> USD-micros table loaded from prices.json and enforced by the
// seam. Generalized from satportal's internal/pricing.
package prices

import (
	"encoding/json"
	"os"

	"github.com/karamble/402sellerkit/seam"
)

// Table maps a resource (URL path or mcp://tool/...) to its price in USD
// micros. It satisfies seam.Prices.
type Table struct {
	prices map[string]seam.USDMicros
}

// Quote is a priced resource: the resolved price for one site.
type Quote struct {
	Resource  string
	USDMicros seam.USDMicros
}

// Load reads a prices.json of {"<resource>": <usd_micros>, ...}.
func Load(path string) (*Table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]seam.USDMicros
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return &Table{prices: raw}, nil
}

// New builds a Table from an in-memory map (tests, embedded defaults).
func New(prices map[string]seam.USDMicros) *Table {
	return &Table{prices: prices}
}

// PriceFor returns the USD-micros price for a resource and whether it is priced.
func (t *Table) PriceFor(resource string) (seam.USDMicros, bool) {
	p, ok := t.prices[resource]
	return p, ok
}

// QuoteFor resolves a resource to a Quote.
func (t *Table) QuoteFor(resource string) (Quote, bool) {
	p, ok := t.prices[resource]
	return Quote{Resource: resource, USDMicros: p}, ok
}
