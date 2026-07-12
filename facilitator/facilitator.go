package facilitator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds a single facilitator request.
const DefaultTimeout = 30 * time.Second

// maxRespBytes caps a facilitator response body.
const maxRespBytes = 1 << 20

// Request is the POST /verify and POST /settle body (x402 v2 facilitator API).
// The payload and requirements are rail-specific, so they pass through as raw
// JSON and this package needs no rail or SDK types.
type Request struct {
	PaymentPayload      json.RawMessage `json:"paymentPayload"`
	PaymentRequirements json.RawMessage `json:"paymentRequirements"`
}

// VerifyResponse is the POST /verify response: the scheme's stateless checks.
type VerifyResponse struct {
	IsValid        bool   `json:"isValid"`
	InvalidReason  string `json:"invalidReason,omitempty"`
	InvalidMessage string `json:"invalidMessage,omitempty"`
	Payer          string `json:"payer,omitempty"`
}

// SettleResponse is the POST /settle response.
type SettleResponse struct {
	Success     bool            `json:"success"`
	ErrorReason string          `json:"errorReason,omitempty"`
	Payer       string          `json:"payer,omitempty"`
	Transaction string          `json:"transaction"`
	Network     string          `json:"network"`
	Amount      string          `json:"amount,omitempty"`
	Extensions  json.RawMessage `json:"extensions,omitempty"`
}

// SupportedKind is one (scheme, network) pair a facilitator supports.
type SupportedKind struct {
	X402Version int             `json:"x402Version"`
	Scheme      string          `json:"scheme"`
	Network     string          `json:"network"`
	Extra       json.RawMessage `json:"extra,omitempty"`
}

// SupportedResponse is the GET /supported body: the kinds a facilitator
// verifies and settles, the protocol extensions it implements, and its signers.
type SupportedResponse struct {
	Kinds      []SupportedKind     `json:"kinds"`
	Extensions []string            `json:"extensions"`
	Signers    map[string][]string `json:"signers"`
}

// Client speaks the x402 v2 facilitator protocol.
type Client interface {
	Verify(ctx context.Context, req Request) (*VerifyResponse, error)
	Settle(ctx context.Context, req Request) (*SettleResponse, error)
	Supported(ctx context.Context) (*SupportedResponse, error)
}

// HTTP is a Client that calls a self-hosted facilitator over HTTP.
type HTTP struct {
	URL    string       // facilitator base URL
	APIKey string       // optional; sent as X-API-Key (dcrfac gates verify/settle)
	Client *http.Client // nil = a default with DefaultTimeout
}

var _ Client = (*HTTP)(nil)

// NewHTTP builds an HTTP facilitator client. apiKey may be empty for an open,
// self-hosted facilitator.
func NewHTTP(url, apiKey string) *HTTP { return &HTTP{URL: url, APIKey: apiKey} }

// Verify runs the stateless cryptographic checks on a payment.
func (h *HTTP) Verify(ctx context.Context, req Request) (*VerifyResponse, error) {
	var out VerifyResponse
	if err := h.post(ctx, "/verify", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Settle notarizes/executes a verified payment.
func (h *HTTP) Settle(ctx context.Context, req Request) (*SettleResponse, error) {
	var out SettleResponse
	if err := h.post(ctx, "/settle", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Supported reports the (scheme, network) kinds the facilitator handles.
func (h *HTTP) Supported(ctx context.Context) (*SupportedResponse, error) {
	var out SupportedResponse
	if err := h.do(ctx, http.MethodGet, "/supported", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (h *HTTP) post(ctx context.Context, path string, req Request, out any) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("facilitator: marshal: %w", err)
	}
	return h.do(ctx, http.MethodPost, path, bytes.NewReader(raw), out)
}

func (h *HTTP) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	if strings.TrimSpace(h.URL) == "" {
		return fmt.Errorf("facilitator: URL is required")
	}
	endpoint := strings.TrimRight(h.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("facilitator: request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.APIKey != "" {
		req.Header.Set("X-API-Key", h.APIKey)
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("facilitator: %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("facilitator: %s %s: %s: %s", method, endpoint, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("facilitator: decode %s: %w", endpoint, err)
		}
	}
	return nil
}

// Inline is a Client that runs verify/settle in-process instead of calling a
// remote service - the maximum-sovereignty (T1) topology, where the seller
// trusts no third party. The funcs typically wrap a rail's own node; a nil func
// means that operation is unsupported inline.
type Inline struct {
	VerifyFunc    func(ctx context.Context, req Request) (*VerifyResponse, error)
	SettleFunc    func(ctx context.Context, req Request) (*SettleResponse, error)
	SupportedFunc func(ctx context.Context) (*SupportedResponse, error)
}

var _ Client = Inline{}

// Verify runs the inline verify func.
func (i Inline) Verify(ctx context.Context, req Request) (*VerifyResponse, error) {
	if i.VerifyFunc == nil {
		return nil, fmt.Errorf("facilitator: inline verify not configured")
	}
	return i.VerifyFunc(ctx, req)
}

// Settle runs the inline settle func.
func (i Inline) Settle(ctx context.Context, req Request) (*SettleResponse, error) {
	if i.SettleFunc == nil {
		return nil, fmt.Errorf("facilitator: inline settle not configured")
	}
	return i.SettleFunc(ctx, req)
}

// Supported runs the inline supported func.
func (i Inline) Supported(ctx context.Context) (*SupportedResponse, error) {
	if i.SupportedFunc == nil {
		return nil, fmt.Errorf("facilitator: inline supported not configured")
	}
	return i.SupportedFunc(ctx)
}
