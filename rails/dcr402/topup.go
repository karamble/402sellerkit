package dcr402rail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	wire "github.com/karamble/dcr402/lib/x402"

	"github.com/karamble/402sellerkit/present/challenge"
	"github.com/karamble/402sellerkit/seam"
)

// ErrPendingConfirmation reports an onchain deposit that exists but has not
// reached the required depth yet. It is retryable, not invalid: the buyer
// re-presents the same proof once the transaction confirms, so a top-up
// handler should answer "pending, retry later" rather than 401.
var ErrPendingConfirmation = errors.New("dcr402: deposit pending confirmation - re-present the proof once confirmed")

// TopupChallengeJSON mints the dual-method top-up challenge for a
// buyer-chosen amount: a lightning entry and, when the rail has an onchain
// backend, an onchain entry. The spec's amount is the buyer's choice, bound
// to the resource exactly like fixed-price sites, so the settlement path is
// the ordinary TrySettle. confirmations <= 0 uses the scheme default of 2.
func (r *Rail) TopupChallengeJSON(ctx context.Context, spec seam.ChallengeSpec, confirmations int32) (json.RawMessage, error) {
	pr, err := r.mintTopup(ctx, spec, confirmations)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pr)
}

// WriteTopup402 contributes the dual-method top-up challenge to a pending
// 402, merging into any header another rail already set.
func (r *Rail) WriteTopup402(ctx context.Context, h http.Header, spec seam.ChallengeSpec, confirmations int32) error {
	pr, err := r.mintTopup(ctx, spec, confirmations)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(pr)
	if err != nil {
		return err
	}
	return challenge.SetOrMerge(h, HeaderPaymentRequired, raw)
}

// mintTopup converts the buyer-chosen USD amount to atoms at the current
// rate and mints the ledger-free dual-method challenge for the bound
// resource.
func (r *Rail) mintTopup(ctx context.Context, spec seam.ChallengeSpec, confirmations int32) (wire.PaymentRequired, error) {
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
	return r.gate.TopupChallenge(ctx, res, atoms, confirmations)
}
