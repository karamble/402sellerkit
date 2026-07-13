package x402usdc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/karamble/402sellerkit/seam"
)

// stubFacilitator answers the SDK client's verify/settle/supported calls,
// so the per-call path runs with no chain and no real facilitator.
func stubFacilitator(t *testing.T, valid bool, txid string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/supported":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kinds": []map[string]any{{"x402Version": 2, "scheme": "exact", "network": NetworkBaseSepolia}},
			})
		case "/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"isValid": valid, "invalidReason": map[bool]string{true: "", false: "bad signature"}[valid],
				"payer": "0x00000000000000000000000000000000000000aa",
			})
		case "/settle":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "transaction": txid, "network": NetworkBaseSepolia,
				"payer": "0x00000000000000000000000000000000000000AA",
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestRailWithStub(t *testing.T, fac string) *Rail {
	t.Helper()
	rail, err := New(Config{
		Network:        NetworkBaseSepolia,
		PayTo:          "0x1111111111111111111111111111111111111111",
		FacilitatorURL: fac,
		Service:        "test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rail
}

// echoPayment builds the payment a buyer would present: the offered accepts
// entry echoed as accepted, with an opaque signature blob.
func echoPayment(t *testing.T, accepts []json.RawMessage) json.RawMessage {
	t.Helper()
	var req types.PaymentRequirements
	if err := json.Unmarshal(accepts[0], &req); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(types.PaymentPayload{
		X402Version: 2,
		Accepted:    req,
		Payload:     map[string]any{"signature": "0xsig", "authorization": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPerCallFlowStubFacilitator(t *testing.T) {
	ctx := context.Background()
	srv := stubFacilitator(t, true, "0xtx123")
	defer srv.Close()
	rail := newTestRailWithStub(t, srv.URL)
	resource := "mcp://tool/generate"
	amount := seam.USDMicros(50000)

	accepts, _, err := rail.MCPAccepts(ctx, resource, amount)
	if err != nil {
		t.Fatalf("MCPAccepts: %v", err)
	}
	if len(accepts) == 0 {
		t.Fatal("no accepts entries")
	}
	raw := echoPayment(t, accepts)

	// Foreign payloads stay unclaimed.
	if p, err := rail.MCPVerify(ctx, resource, json.RawMessage(`{"accepted":{"network":"bip122:mainnet"}}`), amount); p != nil || err != nil {
		t.Fatalf("foreign payload: p=%v err=%v, want nil/nil", p, err)
	}
	// A payment echoing another amount must not match.
	if _, err := rail.MCPVerify(ctx, resource, raw, amount+1); err == nil {
		t.Fatal("want mismatch rejection for another amount")
	}

	p, err := rail.MCPVerify(ctx, resource, raw, amount)
	if err != nil {
		t.Fatalf("MCPVerify: %v", err)
	}
	s, meta, err := rail.MCPSettle(ctx, p)
	if err != nil {
		t.Fatalf("MCPSettle: %v", err)
	}
	if s.Rail != RailName || s.ExternalRef != "0xtx123" || s.AmountUSDMicros != amount {
		t.Fatalf("settlement = %+v", s)
	}
	if s.Payer != strings.ToLower("0x00000000000000000000000000000000000000AA") {
		t.Fatalf("payer = %q", s.Payer)
	}
	if _, ok := meta["x402/payment-response"]; !ok {
		t.Fatalf("meta lacks payment-response: %v", meta)
	}
}

func TestPerCallVerifyRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	srv := stubFacilitator(t, false, "0xtx123")
	defer srv.Close()
	rail := newTestRailWithStub(t, srv.URL)

	accepts, _, err := rail.MCPAccepts(ctx, "mcp://tool/x", 50000)
	if err != nil {
		t.Fatal(err)
	}
	raw := echoPayment(t, accepts)
	if _, err := rail.MCPVerify(ctx, "mcp://tool/x", raw, 50000); err == nil {
		t.Fatal("want rejection when the facilitator says invalid")
	}
}
