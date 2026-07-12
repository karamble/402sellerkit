package dcr402rail

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	lib "github.com/karamble/dcr402/lib"
	"github.com/karamble/dcr402/lib/dcrlnd"
	"github.com/karamble/dcr402/lib/store"
	wire "github.com/karamble/dcr402/lib/x402"

	"github.com/karamble/402sellerkit/seam"
)

// RailName is the rail name this plugin settles as.
const RailName = "dcr402"

// x402 v2 wire headers (HTTP canonicalization makes these case-insensitive).
const (
	HeaderPaymentRequired  = "PAYMENT-REQUIRED"
	HeaderPaymentSignature = "PAYMENT-SIGNATURE"
	HeaderPaymentResponse  = "PAYMENT-RESPONSE"
)

// Config wires the rail against a reachable dcrlnd node.
type Config struct {
	Network      string // mainnet | testnet3 | simnet
	PayTo        string // seller dcrlnd identity pubkey (hex)
	Service      string // service name advertised in challenges ("" = 402sellerkit)
	LNDHost      string // dcrlnd gRPC host:port
	LNDTLSCert   string // dcrlnd tls.cert path
	LNDMacaroon  string // dcrlnd invoice.macaroon path
	StorePath    string // gate challenge/settlement sqlite path
	RateURL      string // dcrdata API base ("" = DefaultRateURL)
	RateFallback float64
	Logger       *slog.Logger
}

// Rail is the Decred Lightning payment rail. It satisfies seam.Rail.
type Rail struct {
	gate  *lib.Gate
	store store.Store
	rate  RateSource
	net   lib.Network
	svc   string
	log   *slog.Logger
}

var _ seam.Rail = (*Rail)(nil)

// New assembles the rail against a reachable dcrlnd node.
func New(cfg Config) (*Rail, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Service == "" {
		cfg.Service = "402sellerkit"
	}
	n, err := networkFor(cfg.Network)
	if err != nil {
		return nil, err
	}
	if cfg.LNDHost == "" || cfg.LNDTLSCert == "" || cfg.LNDMacaroon == "" {
		return nil, fmt.Errorf("dcr402: LNDHost, LNDTLSCert and LNDMacaroon are required")
	}
	backend, err := dcrlnd.New(dcrlnd.Config{
		Host:         cfg.LNDHost,
		TLSCertPath:  cfg.LNDTLSCert,
		MacaroonPath: cfg.LNDMacaroon,
		ChainParams:  n.Params,
	})
	if err != nil {
		return nil, fmt.Errorf("dcr402: dcrlnd backend: %w", err)
	}
	st, err := store.OpenSQLite(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("dcr402: store: %w", err)
	}
	gate, err := lib.New(lib.Config{
		Backend: backend,
		Store:   st,
		Network: n,
		PayTo:   cfg.PayTo,
		Service: cfg.Service,
	})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("dcr402: gate: %w", err)
	}
	rate := &CachedRate{
		Source:   &DcrdataRate{BaseURL: cfg.RateURL},
		Fallback: cfg.RateFallback,
	}
	return newRail(gate, st, rate, n, cfg.Service, cfg.Logger), nil
}

// newRail is the assembly seam shared with tests (no node required).
func newRail(gate *lib.Gate, st store.Store, rate RateSource, n lib.Network, svc string, log *slog.Logger) *Rail {
	return &Rail{gate: gate, store: st, rate: rate, net: n, svc: svc,
		log: log.With("component", "dcr402")}
}

func networkFor(name string) (lib.Network, error) {
	switch name {
	case "mainnet":
		return lib.Mainnet, nil
	case "testnet3":
		return lib.Testnet3, nil
	case "simnet":
		// Development only; a publicly reachable service must not advertise
		// simnet challenges.
		return lib.Simnet, nil
	default:
		return lib.Network{}, fmt.Errorf("dcr402: unsupported network %q (use mainnet, testnet3 or simnet)", name)
	}
}

// Close releases the gate store.
func (r *Rail) Close() error { return r.store.Close() }

// NetworkCAIP2 reports the rail's CAIP-2 network identifier.
func (r *Rail) NetworkCAIP2() string { return r.net.CAIP2 }

// Names reports the single rail name "dcr402".
func (r *Rail) Names() []string { return []string{RailName} }

// boundResource appends the USD amount to the site URL, binding every minted
// challenge to its site and price: settlement recomputes the same URL from the
// current spec and requires equality with the stored challenge, so a proof
// minted for one site or amount cannot settle another.
func boundResource(resource string, usdMicros int64) string {
	sep := "?"
	if strings.Contains(resource, "?") {
		sep = "&"
	}
	return resource + sep + "usd_micros=" + strconv.FormatInt(usdMicros, 10)
}

// mint converts the USD amount to atoms at the current rate and mints a
// challenge for the bound resource.
func (r *Rail) mint(ctx context.Context, spec seam.ChallengeSpec) (wire.PaymentRequired, error) {
	rate, err := r.rate.USDPerDCR(ctx)
	if err != nil {
		return wire.PaymentRequired{}, fmt.Errorf("dcr402: exchange rate: %w", err)
	}
	usdMicros := int64(spec.AmountUSDMicros)
	atoms, err := AtomsForUSDMicros(usdMicros, rate)
	if err != nil {
		return wire.PaymentRequired{}, err
	}
	res := wire.ResourceInfo{
		URL:         boundResource(spec.Resource, usdMicros),
		Description: fmt.Sprintf("%s %s", r.svc, spec.Purpose),
		MimeType:    "application/json",
		ServiceName: r.svc,
	}
	return r.gate.Challenge(ctx, res, atoms, nil)
}

// ChallengeJSON renders the in-band Lightning challenge object.
func (r *Rail) ChallengeJSON(ctx context.Context, spec seam.ChallengeSpec) (json.RawMessage, error) {
	pr, err := r.mint(ctx, spec)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pr)
}

// Write402 contributes the Lightning challenge to a pending 402. When another
// rail already set the Payment-Required header, its accepts and extensions are
// preserved and the Lightning entry merged in, so one header carries every
// rail's method.
func (r *Rail) Write402(ctx context.Context, h http.Header, spec seam.ChallengeSpec) error {
	pr, err := r.mint(ctx, spec)
	if err != nil {
		return err
	}
	if existing := h.Get(HeaderPaymentRequired); existing != "" {
		merged, err := MergeChallengeHeader(existing, pr)
		if err != nil {
			return err
		}
		h.Set(HeaderPaymentRequired, merged)
		return nil
	}
	raw, err := json.Marshal(pr)
	if err != nil {
		return err
	}
	h.Set(HeaderPaymentRequired, base64.StdEncoding.EncodeToString(raw))
	return nil
}

// MergeChallengeHeader merges a dcr402 challenge into an existing base64
// Payment-Required header value at the JSON level, appending accepts entries
// and extension keys without depending on the other rail's types.
func MergeChallengeHeader(existingB64 string, pr wire.PaymentRequired) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(existingB64)
	if err != nil {
		return "", fmt.Errorf("dcr402: existing challenge header: %w", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("dcr402: existing challenge body: %w", err)
	}

	var accepts []json.RawMessage
	if a, ok := body["accepts"]; ok {
		if err := json.Unmarshal(a, &accepts); err != nil {
			return "", fmt.Errorf("dcr402: existing accepts: %w", err)
		}
	}
	for _, entry := range pr.Accepts {
		raw, err := json.Marshal(entry)
		if err != nil {
			return "", err
		}
		accepts = append(accepts, raw)
	}
	if body["accepts"], err = json.Marshal(accepts); err != nil {
		return "", err
	}

	if len(pr.Extensions) > 0 {
		exts := map[string]json.RawMessage{}
		if e, ok := body["extensions"]; ok {
			if err := json.Unmarshal(e, &exts); err != nil {
				return "", fmt.Errorf("dcr402: existing extensions: %w", err)
			}
		}
		for k, v := range pr.Extensions {
			if _, taken := exts[k]; taken {
				continue
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			exts[k] = raw
		}
		if body["extensions"], err = json.Marshal(exts); err != nil {
			return "", err
		}
	}

	merged, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(merged), nil
}

// TrySettle inspects the request for a Decred payment proof in the
// Payment-Signature header. Foreign payloads (non-bip122 networks) are left for
// the other rails: (nil, nil). A present-but-invalid proof returns an error.
func (r *Rail) TrySettle(ctx context.Context, h http.Header, spec seam.ChallengeSpec) (*seam.Settlement, error) {
	encoded := h.Get(HeaderPaymentSignature)
	if encoded == "" {
		return nil, nil
	}
	payloadRaw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("dcr402: payment header is not base64: %w", err)
	}

	var probe struct {
		Accepted struct {
			Network string `json:"network"`
		} `json:"accepted"`
	}
	if err := json.Unmarshal(payloadRaw, &probe); err != nil {
		return nil, nil // unparseable here; another rail may understand it
	}
	if !strings.HasPrefix(probe.Accepted.Network, "bip122:") {
		return nil, nil // not a Decred payment
	}
	if probe.Accepted.Network != r.net.CAIP2 {
		return nil, fmt.Errorf("dcr402: payment network %q, this service is on %q (%s)",
			probe.Accepted.Network, r.net.CAIP2, r.net.Name)
	}

	var pp wire.PaymentPayload
	if err := json.Unmarshal(payloadRaw, &pp); err != nil {
		return nil, fmt.Errorf("dcr402: payment payload: %w", err)
	}
	var lightning wire.LightningPayload
	if err := json.Unmarshal(pp.Payload, &lightning); err != nil {
		return nil, fmt.Errorf("dcr402: lightning payload: %w", err)
	}
	hashRaw, err := hex.DecodeString(lightning.PaymentHash)
	if err != nil || len(hashRaw) != 32 {
		return nil, fmt.Errorf("dcr402: payment hash %q", lightning.PaymentHash)
	}
	var hash [32]byte
	copy(hash[:], hashRaw)

	// The stored challenge binds the payment to its site and USD amount; a
	// proof for another site or price is rejected before settlement.
	ch, err := r.store.GetChallenge(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("dcr402: unknown or expired challenge - request a fresh one")
	}
	if want := boundResource(spec.Resource, int64(spec.AmountUSDMicros)); ch.Resource != want {
		return nil, fmt.Errorf("dcr402: payment was minted for %q, not this site - request a fresh challenge", ch.Resource)
	}

	settle, already, vErr := r.gate.Settle(ctx, pp, ch.AmountAtoms)
	if vErr != nil {
		return nil, fmt.Errorf("dcr402: payment invalid: %s", vErr.Reason)
	}
	if already {
		r.log.Info("settlement replayed", "hash", lightning.PaymentHash)
	}

	receipt, _ := json.Marshal(settle)
	respHeaders := http.Header{}
	respHeaders.Set(HeaderPaymentResponse, base64.StdEncoding.EncodeToString(receipt))
	ref := RailName + ":" + strings.ToLower(lightning.PaymentHash)
	return &seam.Settlement{
		Rail:            RailName,
		Payer:           ref, // LN reveals no payer identity; the hash is the unique key
		AmountUSDMicros: spec.AmountUSDMicros,
		ExternalRef:     ref,
		ReceiptJSON:     string(receipt),
		RespHeaders:     respHeaders,
	}, nil
}
