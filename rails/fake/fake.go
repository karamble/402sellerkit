// Package fake is a dependency-free stub Rail for development and for
// 402sellerkit's reference service. It settles on a plaintext X-Fake-Payment
// header so the seam lifecycle can be exercised end to end without a real
// payment network. Never enable it in production.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/karamble/402sellerkit/seam"
)

// Rail is the fake payment rail.
type Rail struct{}

// New returns a fake rail.
func New() *Rail { return &Rail{} }

// Names reports the single rail name "fake".
func (r *Rail) Names() []string { return []string{"fake"} }

// ChallengeJSON renders a trivial challenge object describing how to pay: send
// any X-Fake-Payment header to settle.
func (r *Rail) ChallengeJSON(_ context.Context, spec seam.ChallengeSpec) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"rail":              "fake",
		"pay_with":          "X-Fake-Payment: <any-ref>",
		"amount_usd_micros": int64(spec.AmountUSDMicros),
		"resource":          spec.Resource,
	})
}

// Write402 advertises the fake rail via an X-Fake-Challenge header.
func (r *Rail) Write402(_ context.Context, h http.Header, _ seam.ChallengeSpec) error {
	h.Set("X-Fake-Challenge", "pay via X-Fake-Payment header")
	return nil
}

// TrySettle settles when an X-Fake-Payment header is present, using its value
// as the external reference. Absent header -> (nil, nil): not this rail's
// payload.
func (r *Rail) TrySettle(_ context.Context, h http.Header, spec seam.ChallengeSpec) (*seam.Settlement, error) {
	ref := h.Get("X-Fake-Payment")
	if ref == "" {
		return nil, nil
	}
	return &seam.Settlement{
		Rail:            "fake",
		Payer:           "fake:" + ref,
		AmountUSDMicros: spec.AmountUSDMicros,
		ExternalRef:     ref,
		ReceiptJSON:     `{"rail":"fake"}`,
	}, nil
}

// --- PerCallRail -----------------------------------------------------------
//
// The per-call half mirrors the wire conventions of the real rails so the
// seam's verify -> run -> settle lifecycle is exercisable offline: the
// payment payload is {"rail":"fake","ref":"..."} and a payload naming
// another rail is left unclaimed.

var _ seam.PerCallRail = (*Rail)(nil)

// fakePayment holds the verified-but-unsettled per-call payment.
type fakePayment struct {
	ref    string
	amount seam.USDMicros
}

// MCPAccepts contributes the fake accepts entry for one payable call.
func (r *Rail) MCPAccepts(_ context.Context, resource string, amount seam.USDMicros) ([]json.RawMessage, map[string]json.RawMessage, error) {
	accept, err := json.Marshal(map[string]any{
		"rail":              "fake",
		"pay_with":          `payment payload {"rail":"fake","ref":"<any-ref>"}`,
		"amount_usd_micros": int64(amount),
		"resource":          resource,
	})
	if err != nil {
		return nil, nil, err
	}
	return []json.RawMessage{accept}, nil, nil
}

// MCPVerify claims payloads whose rail field is "fake"; anything else is
// another rail's payload (nil, nil). An empty ref is present-but-invalid.
func (r *Rail) MCPVerify(_ context.Context, _ string, raw json.RawMessage, amount seam.USDMicros) (seam.PerCallPayment, error) {
	var probe struct {
		Rail string `json:"rail"`
		Ref  string `json:"ref"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Rail != "fake" {
		return nil, nil
	}
	if probe.Ref == "" {
		return nil, fmt.Errorf("fake: payment payload has no ref")
	}
	return &fakePayment{ref: probe.Ref, amount: amount}, nil
}

// MCPSettle consumes the verified payment.
func (r *Rail) MCPSettle(_ context.Context, p seam.PerCallPayment) (*seam.Settlement, map[string]any, error) {
	fp, ok := p.(*fakePayment)
	if !ok {
		return nil, nil, fmt.Errorf("fake: not a fake per-call payment: %T", p)
	}
	return &seam.Settlement{
			Rail:            "fake",
			Payer:           "fake:" + fp.ref,
			AmountUSDMicros: fp.amount,
			ExternalRef:     fp.ref,
			ReceiptJSON:     `{"rail":"fake"}`,
		}, map[string]any{
			"x402/payment-response": map[string]any{"success": true, "transaction": fp.ref},
		}, nil
}
