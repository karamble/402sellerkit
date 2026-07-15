package x402usdc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	evmexact "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/server"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/karamble/402sellerkit/present/challenge"
	"github.com/karamble/402sellerkit/seam"
)

// RailName is the rail name this plugin settles as (the x402 protocol name;
// buyers probe for it in accepts entries).
const RailName = "x402"

// x402 v2 wire headers (the SDK does not export the names).
const (
	HeaderPaymentRequired  = "PAYMENT-REQUIRED"
	HeaderPaymentSignature = "PAYMENT-SIGNATURE"
	HeaderPaymentResponse  = "PAYMENT-RESPONSE"
)

// Supported Base networks.
const (
	NetworkBaseMainnet = "eip155:8453"
	NetworkBaseSepolia = "eip155:84532"

	// keylessTestnetFacilitator is the public, account-free testnet facilitator.
	keylessTestnetFacilitator = "https://x402.org/facilitator"
)

// Config wires the rail.
type Config struct {
	Network        string // eip155:8453 (Base mainnet) | eip155:84532 (Base Sepolia)
	PayTo          string // receiving EVM address; the private key never touches the server
	FacilitatorURL string // self-hosted verify/settle endpoint; REQUIRED on mainnet
	Service        string // service name advertised in challenges ("" = 402sellerkit)
	Logger         *slog.Logger
}

// validate checks the config and resolves the facilitator URL. It enforces
// ADR-0001: mainnet USDC settles ONLY through a self-hosted facilitator, never
// Coinbase/CDP.
func (cfg Config) validate() (Config, error) {
	if !strings.HasPrefix(cfg.PayTo, "0x") || len(cfg.PayTo) != 42 {
		return cfg, fmt.Errorf("x402usdc: PayTo %q is not an EVM address", cfg.PayTo)
	}
	if strings.Contains(cfg.FacilitatorURL, "cdp.coinbase.com") {
		return cfg, fmt.Errorf("x402usdc: the Coinbase/CDP facilitator is not permitted (ADR-0001); run a self-hosted facilitator")
	}
	switch cfg.Network {
	case NetworkBaseSepolia:
		if cfg.FacilitatorURL == "" {
			cfg.FacilitatorURL = keylessTestnetFacilitator
		}
	case NetworkBaseMainnet:
		if cfg.FacilitatorURL == "" {
			return cfg, fmt.Errorf("x402usdc: mainnet requires an explicit self-hosted FacilitatorURL (ADR-0001: no CDP)")
		}
		if strings.Contains(cfg.FacilitatorURL, "x402.org") {
			return cfg, fmt.Errorf("x402usdc: mainnet paired with the keyless testnet facilitator %q", cfg.FacilitatorURL)
		}
	default:
		return cfg, fmt.Errorf("x402usdc: unsupported network %q (use %s or %s)", cfg.Network, NetworkBaseMainnet, NetworkBaseSepolia)
	}
	if cfg.Service == "" {
		cfg.Service = "402sellerkit"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return cfg, nil
}

// Rail is the x402 USDC-on-Base payment rail. It satisfies seam.Rail.
type Rail struct {
	cfg Config
	rs  *x402.X402ResourceServer
	log *slog.Logger
}

var _ seam.Rail = (*Rail)(nil)

// New validates the config (ADR-0001 boot guard) and assembles the resource
// server against a self-hosted facilitator.
func New(cfg Config) (*Rail, error) {
	cfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	rs := x402.Newx402ResourceServer(
		x402.WithFacilitatorClient(x402http.NewHTTPFacilitatorClient(&x402http.FacilitatorConfig{URL: cfg.FacilitatorURL})),
	)
	rs.Register(x402.Network(cfg.Network), evmexact.NewExactEvmScheme())
	r := &Rail{cfg: cfg, rs: rs, log: cfg.Logger.With("component", "x402usdc")}

	// Sync supported kinds from the facilitator. Failure must not block boot;
	// payments stay 402-retryable and a background loop keeps trying.
	initCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	initErr := rs.Initialize(initCtx)
	cancel()
	if initErr != nil {
		r.log.Warn("facilitator sync failed at boot; retrying in background", "facilitator", cfg.FacilitatorURL, "err", initErr)
		go r.retryInitialize()
	}
	return r, nil
}

func (r *Rail) retryInitialize() {
	for {
		time.Sleep(time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := r.rs.Initialize(ctx)
		cancel()
		if err == nil {
			r.log.Info("facilitator sync succeeded")
			return
		}
		r.log.Warn("facilitator sync still failing", "err", err)
	}
}

// Resource exposes the underlying resource server (for an MCP per-call
// integration built on the same engine).
func (r *Rail) Resource() *x402.X402ResourceServer { return r.rs }

// Names reports the single rail name "x402".
func (r *Rail) Names() []string { return []string{RailName} }

// requirementsFor builds the enforceable payment requirements for a spec.
// Amount binding is structural: a client payment must exactly match these or
// FindMatchingRequirements rejects it, so a proof for one price cannot settle
// another.
func (r *Rail) requirementsFor(ctx context.Context, spec seam.ChallengeSpec) ([]types.PaymentRequirements, error) {
	reqs, err := r.rs.BuildPaymentRequirementsFromConfig(ctx, x402.ResourceConfig{
		Scheme:            "exact",
		Network:           x402.Network(r.cfg.Network),
		PayTo:             r.cfg.PayTo,
		Price:             float64(spec.AmountUSDMicros) / seam.MicrosPerUSD,
		MaxTimeoutSeconds: 300,
	})
	if err != nil {
		return nil, fmt.Errorf("x402usdc: build requirements: %w", err)
	}
	return reqs, nil
}

// paymentRequiredFor renders the v2 PaymentRequired object for a spec.
func (r *Rail) paymentRequiredFor(ctx context.Context, spec seam.ChallengeSpec) (*types.PaymentRequired, error) {
	reqs, err := r.requirementsFor(ctx, spec)
	if err != nil {
		return nil, err
	}
	resourceURL := spec.Resource
	if spec.WireURL != "" {
		resourceURL = spec.WireURL
	}
	return &types.PaymentRequired{
		X402Version: 2,
		Error:       "payment required",
		Resource: &types.ResourceInfo{
			URL:         resourceURL,
			Description: fmt.Sprintf("%s %s", r.cfg.Service, spec.Purpose),
			MimeType:    "application/json",
			ServiceName: r.cfg.Service,
		},
		Accepts: reqs,
	}, nil
}

// ChallengeJSON renders the in-band challenge object.
func (r *Rail) ChallengeJSON(ctx context.Context, spec seam.ChallengeSpec) (json.RawMessage, error) {
	pr, err := r.paymentRequiredFor(ctx, spec)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pr)
}

// Write402 contributes the USDC challenge to a pending 402. If another rail has
// already set the Payment-Required header, this rail's accepts entries are
// merged in (order-independent), so one header carries every rail's method.
func (r *Rail) Write402(ctx context.Context, h http.Header, spec seam.ChallengeSpec) error {
	pr, err := r.paymentRequiredFor(ctx, spec)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(pr)
	if err != nil {
		return err
	}
	return challenge.SetOrMerge(h, HeaderPaymentRequired, raw)
}

// TrySettle inspects the request for a PAYMENT-SIGNATURE header, verifies the
// payment against the spec's requirements and settles it. Foreign payloads
// (non-eip155 networks) are left for the other rails: (nil, nil).
func (r *Rail) TrySettle(ctx context.Context, h http.Header, spec seam.ChallengeSpec) (*seam.Settlement, error) {
	encoded := h.Get(HeaderPaymentSignature)
	if encoded == "" {
		return nil, nil
	}
	payloadRaw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("x402usdc: payment header is not base64: %w", err)
	}
	var payload types.PaymentPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return nil, fmt.Errorf("x402usdc: payment payload: %w", err)
	}
	if !strings.HasPrefix(string(payload.Accepted.Network), "eip155:") {
		return nil, nil // not an EVM payment; another rail may claim it
	}

	reqs, err := r.requirementsFor(ctx, spec)
	if err != nil {
		return nil, err
	}
	matched := r.rs.FindMatchingRequirements(reqs, payload)
	if matched == nil {
		return nil, fmt.Errorf("x402usdc: payment does not match the required amount/asset/network - request a fresh challenge")
	}
	verify, err := r.rs.VerifyPayment(ctx, payload, *matched)
	if err != nil {
		return nil, fmt.Errorf("x402usdc: verify: %w", err)
	}
	if !verify.IsValid {
		return nil, fmt.Errorf("x402usdc: payment invalid: %s", verify.InvalidReason)
	}
	settle, err := r.rs.SettlePayment(ctx, payload, *matched, nil)
	if err != nil {
		return nil, fmt.Errorf("x402usdc: settle: %w", err)
	}
	if !settle.Success {
		return nil, fmt.Errorf("x402usdc: settlement failed: %s", settle.ErrorReason)
	}

	receipt, _ := json.Marshal(settle)
	respHeaders := http.Header{}
	respHeaders.Set(HeaderPaymentResponse, base64.StdEncoding.EncodeToString(receipt))
	ref := settle.Transaction
	if ref == "" {
		// Should not happen on success; fall back to the payload fingerprint so
		// the bill-once anchor still holds.
		sum := sha256.Sum256(payloadRaw)
		ref = "x402:" + hex.EncodeToString(sum[:16])
	}
	return &seam.Settlement{
		Rail:            RailName,
		Payer:           strings.ToLower(settle.Payer),
		AmountUSDMicros: spec.AmountUSDMicros,
		ExternalRef:     ref,
		ReceiptJSON:     string(receipt),
		RespHeaders:     respHeaders,
	}, nil
}
