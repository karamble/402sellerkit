package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds a single submit request.
const DefaultTimeout = 30 * time.Second

// Descriptor is one advertised resource in the x402 v2 discovery/Bazaar shape:
// what a service publishes so agents can find its paid endpoint. Accepts holds
// the rail accepts[] entries as raw JSON (the rails produce them), so this
// stays rail-agnostic and dependency-free.
type Descriptor struct {
	Resource    string            // URL, or mcp://tool/name; the index key
	Type        string            // "http" (default) or "mcp"
	Accepts     []json.RawMessage // the resource's accepts[] entries (>= 1)
	ServiceName string
	Description string
	MimeType    string
	Tags        []string
	IconURL     string
}

// Target is a bazaar-shaped discovery index that accepts submissions at
// <URL>/discovery/submit. APIKey is optional - empty for an open, self-hosted
// index such as dcrfac.
type Target struct {
	URL    string
	APIKey string
}

// submitBody is the POST /discovery/submit wire shape (matches dcrfac's
// SubmitRequest: the x402 v2 discovery submission shape).
type submitBody struct {
	Resource string            `json:"resource"`
	Type     string            `json:"type,omitempty"`
	Accepts  []json.RawMessage `json:"accepts"`
	Metadata submitMetadata    `json:"metadata"`
}

// submitMetadata mirrors x402 ResourceInfo's human-facing fields (its JSON
// tags), replicated here so core needs no rail/SDK type.
type submitMetadata struct {
	Description string   `json:"description,omitempty"`
	MimeType    string   `json:"mimeType,omitempty"`
	ServiceName string   `json:"serviceName,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	IconURL     string   `json:"iconUrl,omitempty"`
}

func (d Descriptor) body() submitBody {
	typ := d.Type
	if typ == "" {
		typ = "http"
	}
	return submitBody{
		Resource: d.Resource,
		Type:     typ,
		Accepts:  d.Accepts,
		Metadata: submitMetadata{
			Description: d.Description,
			MimeType:    d.MimeType,
			ServiceName: d.ServiceName,
			Tags:        d.Tags,
			IconURL:     d.IconURL,
		},
	}
}

func (d Descriptor) validate() error {
	if strings.TrimSpace(d.Resource) == "" {
		return fmt.Errorf("discovery: descriptor Resource is required")
	}
	if len(d.Accepts) == 0 {
		return fmt.Errorf("discovery: descriptor needs at least one accepts entry")
	}
	return nil
}

// AcceptsFromChallenge lifts the accepts[] entries out of an x402 v2
// PaymentRequired JSON (as produced by seam.Rail.ChallengeJSON), for building a
// Descriptor: a service advertises a resource by minting a representative
// challenge and publishing its accepts entries.
func AcceptsFromChallenge(challengeJSON []byte) ([]json.RawMessage, error) {
	var body struct {
		Accepts []json.RawMessage `json:"accepts"`
	}
	if err := json.Unmarshal(challengeJSON, &body); err != nil {
		return nil, fmt.Errorf("discovery: challenge JSON: %w", err)
	}
	if len(body.Accepts) == 0 {
		return nil, fmt.Errorf("discovery: challenge has no accepts entries")
	}
	return body.Accepts, nil
}

// Submit registers a descriptor with one bazaar-shaped index by POSTing to
// target.URL + "/discovery/submit". A nil client uses a default with
// DefaultTimeout.
func Submit(ctx context.Context, client *http.Client, target Target, d Descriptor) error {
	if err := d.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(target.URL) == "" {
		return fmt.Errorf("discovery: target URL is required")
	}
	raw, err := json.Marshal(d.body())
	if err != nil {
		return fmt.Errorf("discovery: marshal: %w", err)
	}
	endpoint := strings.TrimRight(target.URL, "/") + "/discovery/submit"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("discovery: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if target.APIKey != "" {
		req.Header.Set("X-API-Key", target.APIKey)
	}
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("discovery: post %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("discovery: %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// Registrar publishes a descriptor to a discovery index. A target with
// non-standard auth or a non-dcrfac shape can implement this to plug into a
// Multiplexer alongside the standard TargetRegistrar.
type Registrar interface {
	Register(ctx context.Context, d Descriptor) error
}

// TargetRegistrar submits to one Target via Submit.
type TargetRegistrar struct {
	Target Target
	Client *http.Client
}

// Register submits the descriptor to the registrar's target.
func (r TargetRegistrar) Register(ctx context.Context, d Descriptor) error {
	return Submit(ctx, r.Client, r.Target, d)
}

// Multiplexer fans a descriptor out to several indexes on launch. Listing is
// best-effort advertisement: a failed target does not fail the others, since
// the authoritative payment terms always come from the origin's live 402.
type Multiplexer struct {
	Registrars []Registrar
	Log        *slog.Logger
}

// NewMultiplexer builds a Multiplexer over the given targets (a default HTTP
// client each). A nil logger defaults to slog.Default.
func NewMultiplexer(log *slog.Logger, targets ...Target) *Multiplexer {
	if log == nil {
		log = slog.Default()
	}
	regs := make([]Registrar, 0, len(targets))
	for _, t := range targets {
		regs = append(regs, TargetRegistrar{Target: t})
	}
	return &Multiplexer{Registrars: regs, Log: log}
}

// RegisterAll publishes the descriptor to every registrar and returns the count
// that succeeded. Per-target failures are logged, not fatal.
func (m *Multiplexer) RegisterAll(ctx context.Context, d Descriptor) int {
	ok := 0
	for _, r := range m.Registrars {
		if err := r.Register(ctx, d); err != nil {
			m.Log.Warn("discovery submit failed", "err", err)
			continue
		}
		ok++
	}
	return ok
}
