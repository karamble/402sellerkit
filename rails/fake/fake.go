// Package fake is a dependency-free stub Rail for development and for
// 402sellerkit's reference service. It settles on a plaintext X-Fake-Payment
// header so the seam lifecycle can be exercised end to end without a real
// payment network. Never enable it in production.
package fake

import (
	"context"
	"encoding/json"
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
