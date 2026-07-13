package dcr402rail

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	lib "github.com/karamble/dcr402/lib"
	wire "github.com/karamble/dcr402/lib/x402"

	"github.com/karamble/402sellerkit/seam"
)

// The per-call half: verify before the work, settle after it delivers, so a
// failed settle withholds the result. Per-call payments are lightning-only
// by scheme (scheme_exact_dcr.md: onchain funds balances, never per-call).
var _ seam.PerCallRail = (*Rail)(nil)

// perCallPayment is the verified-but-unsettled per-call payment held across
// the work.
type perCallPayment struct {
	pp    wire.PaymentPayload
	atoms int64 // the stored challenge's exact amount, re-enforced at settle
	usd   seam.USDMicros
	hash  string // lowercase payment hash hex
}

// MCPAccepts mints a fresh lightning challenge for one payable call and
// contributes its accepts entries and extensions to the merged
// payment-required object.
func (r *Rail) MCPAccepts(ctx context.Context, resource string, amount seam.USDMicros) ([]json.RawMessage, map[string]json.RawMessage, error) {
	pr, err := r.mint(ctx, seam.ChallengeSpec{Purpose: "percall", Resource: resource, AmountUSDMicros: amount})
	if err != nil {
		return nil, nil, err
	}
	accepts := make([]json.RawMessage, 0, len(pr.Accepts))
	for _, a := range pr.Accepts {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, nil, err
		}
		accepts = append(accepts, raw)
	}
	var exts map[string]json.RawMessage
	if len(pr.Extensions) > 0 {
		exts = make(map[string]json.RawMessage, len(pr.Extensions))
		for k, v := range pr.Extensions {
			raw, err := json.Marshal(v)
			if err != nil {
				return nil, nil, err
			}
			exts[k] = raw
		}
	}
	return accepts, exts, nil
}

// MCPVerify claims Decred payment payloads (bip122 networks) and verifies
// the proof against the stored challenge without consuming it: challenge
// known, bound to this resource and amount, method lightning. Foreign
// payloads are left for other rails (nil, nil).
func (r *Rail) MCPVerify(ctx context.Context, resource string, raw json.RawMessage, amount seam.USDMicros) (seam.PerCallPayment, error) {
	var probe struct {
		Accepted struct {
			Network string `json:"network"`
		} `json:"accepted"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
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
	if err := json.Unmarshal(raw, &pp); err != nil {
		return nil, fmt.Errorf("dcr402: payment payload: %w", err)
	}
	if m := pp.Accepted.TransferMethod(); m != wire.MethodLightning {
		return nil, fmt.Errorf("dcr402: per-call payments are lightning-only, got method %q", m)
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
	// proof for another tool or price is rejected before the work runs.
	ch, err := r.store.GetChallenge(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("dcr402: unknown or expired challenge - request a fresh one")
	}
	if want := boundResource(resource, int64(amount)); ch.Resource != want {
		return nil, fmt.Errorf("dcr402: payment was minted for %q, not this call - request a fresh challenge", ch.Resource)
	}
	return &perCallPayment{pp: pp, atoms: ch.AmountAtoms, usd: amount,
		hash: strings.ToLower(lightning.PaymentHash)}, nil
}

// MCPSettle consumes the verified payment. The returned meta carries the
// x402/payment-response receipt to attach to the successful result's _meta.
func (r *Rail) MCPSettle(ctx context.Context, p seam.PerCallPayment) (*seam.Settlement, map[string]any, error) {
	pc, ok := p.(*perCallPayment)
	if !ok {
		return nil, nil, fmt.Errorf("dcr402: not a dcr402 per-call payment: %T", p)
	}
	settle, already, vErr := r.gate.Settle(ctx, pc.pp, pc.atoms)
	if vErr != nil {
		return nil, nil, fmt.Errorf("dcr402: payment invalid: %s", vErr.Reason)
	}
	if already {
		r.log.Info("per-call settlement replayed", "hash", pc.hash)
	}
	receiptMeta, err := lib.EncodeMetaPaymentResponse(settle)
	if err != nil {
		return nil, nil, err
	}
	meta := make(map[string]any, len(receiptMeta))
	for k, v := range receiptMeta {
		meta[k] = v
	}
	receipt, _ := json.Marshal(settle)
	ref := RailName + ":" + pc.hash
	return &seam.Settlement{
		Rail:            RailName,
		Payer:           ref, // LN reveals no payer identity; the hash is the unique key
		AmountUSDMicros: pc.usd,
		ExternalRef:     ref,
		ReceiptJSON:     string(receipt),
	}, meta, nil
}
