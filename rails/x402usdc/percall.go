package x402usdc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/karamble/402sellerkit/seam"
)

// The per-call half: the SDK already splits VerifyPayment from
// SettlePayment, so the seam's verify -> run -> settle lifecycle maps
// directly - verification is facilitator-checked before the work runs, and
// the on-chain settle happens only after the work delivers.
var _ seam.PerCallRail = (*Rail)(nil)

// perCallPayment is the verified-but-unsettled per-call payment held across
// the work.
type perCallPayment struct {
	payload types.PaymentPayload
	matched types.PaymentRequirements
	usd     seam.USDMicros
}

// MCPAccepts contributes the USDC accepts entries for one payable call.
// Requirements are structural: the client's payment must echo them exactly
// or MCPVerify rejects it, which is the same amount binding the HTTP path
// enforces.
func (r *Rail) MCPAccepts(ctx context.Context, resource string, amount seam.USDMicros) ([]json.RawMessage, map[string]json.RawMessage, error) {
	reqs, err := r.requirementsFor(ctx, seam.ChallengeSpec{Purpose: "percall", Resource: resource, AmountUSDMicros: amount})
	if err != nil {
		return nil, nil, err
	}
	accepts := make([]json.RawMessage, 0, len(reqs))
	for _, req := range reqs {
		raw, err := json.Marshal(req)
		if err != nil {
			return nil, nil, err
		}
		accepts = append(accepts, raw)
	}
	return accepts, nil, nil
}

// MCPVerify claims EVM payment payloads (eip155 networks) and verifies them
// with the facilitator without settling. Foreign payloads are left for
// other rails (nil, nil).
func (r *Rail) MCPVerify(ctx context.Context, resource string, raw json.RawMessage, amount seam.USDMicros) (seam.PerCallPayment, error) {
	var payload types.PaymentPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil // unparseable here; another rail may understand it
	}
	if !strings.HasPrefix(string(payload.Accepted.Network), "eip155:") {
		return nil, nil // not an EVM payment
	}

	reqs, err := r.requirementsFor(ctx, seam.ChallengeSpec{Purpose: "percall", Resource: resource, AmountUSDMicros: amount})
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
	return &perCallPayment{payload: payload, matched: *matched, usd: amount}, nil
}

// MCPSettle settles the verified payment through the facilitator. The
// returned meta carries the x402/payment-response settlement object for the
// successful result's _meta.
func (r *Rail) MCPSettle(ctx context.Context, p seam.PerCallPayment) (*seam.Settlement, map[string]any, error) {
	pc, ok := p.(*perCallPayment)
	if !ok {
		return nil, nil, fmt.Errorf("x402usdc: not an x402usdc per-call payment: %T", p)
	}
	settle, err := r.rs.SettlePayment(ctx, pc.payload, pc.matched, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("x402usdc: settle: %w", err)
	}
	if !settle.Success {
		return nil, nil, fmt.Errorf("x402usdc: settlement failed: %s", settle.ErrorReason)
	}

	receipt, _ := json.Marshal(settle)
	ref := settle.Transaction
	if ref == "" {
		return nil, nil, fmt.Errorf("x402usdc: settlement succeeded without a transaction ref")
	}
	return &seam.Settlement{
		Rail:            RailName,
		Payer:           strings.ToLower(settle.Payer),
		AmountUSDMicros: pc.usd,
		ExternalRef:     ref,
		ReceiptJSON:     string(receipt),
	}, map[string]any{"x402/payment-response": json.RawMessage(receipt)}, nil
}
