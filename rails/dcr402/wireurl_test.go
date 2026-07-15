package dcr402rail

import (
	"context"
	"encoding/json"
	"testing"

	wire "github.com/karamble/dcr402/lib/x402"

	"github.com/karamble/402sellerkit/seam"
)

// bazaarToolName lifts info.input.toolName from a challenge's bazaar
// extension.
func bazaarToolName(t *testing.T, pr wire.PaymentRequired) string {
	t.Helper()
	ext, ok := pr.Extensions["bazaar"]
	if !ok {
		t.Fatalf("challenge lacks the bazaar extension: %+v", pr.Extensions)
	}
	var info struct {
		Input struct {
			ToolName string `json:"toolName"`
		} `json:"input"`
	}
	if err := json.Unmarshal(ext.Info, &info); err != nil {
		t.Fatal(err)
	}
	return info.Input.ToolName
}

// TestMintWireURLAndDiscovery checks the wire/bind split on the rail's
// challenge path: the 402 advertises the spec's public WireURL and the
// bazaar discovery extension, while verification still binds to the
// per-tool resource and price.
func TestMintWireURLAndDiscovery(t *testing.T) {
	rail, node := newTestRail(t, false)
	ctx := context.Background()
	spec := seam.ChallengeSpec{
		Purpose:         "register",
		Resource:        "mcp://tool/register",
		WireURL:         "https://api.example.com/mcp",
		MCPTool:         "register",
		AmountUSDMicros: 1000,
	}
	cj, err := rail.ChallengeJSON(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	var pr wire.PaymentRequired
	if err := json.Unmarshal(cj, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Resource.URL != spec.WireURL {
		t.Fatalf("wire url = %q, want %q", pr.Resource.URL, spec.WireURL)
	}
	if got := bazaarToolName(t, pr); got != "register" {
		t.Fatalf("bazaar toolName = %q", got)
	}

	// The proof still binds to the per-tool key and price: the right pair
	// verifies, a foreign tool does not.
	raw := lightningPaymentRaw(t, node, pr.Accepts[0])
	if _, err := rail.MCPVerify(ctx, "mcp://tool/register", raw, 1000); err != nil {
		t.Fatalf("verify against the bind key: %v", err)
	}
	if _, err := rail.MCPVerify(ctx, "mcp://tool/other", raw, 1000); err == nil {
		t.Fatal("a proof for one tool must not verify for another")
	}
	if _, err := rail.MCPVerify(ctx, spec.WireURL, raw, 1000); err == nil {
		t.Fatal("the shared wire URL must not be a valid binding key")
	}
}

// TestMintTopupWireURLAndDiscovery checks the same split on the dual-method
// top-up path.
func TestMintTopupWireURLAndDiscovery(t *testing.T) {
	rail, _ := newTestRail(t, true)
	spec := seam.ChallengeSpec{
		Purpose:         "topup",
		Resource:        "mcp://tool/topup",
		WireURL:         "https://api.example.com/mcp",
		MCPTool:         "topup",
		AmountUSDMicros: 5_000_000,
	}
	cj, err := rail.TopupChallengeJSON(context.Background(), spec, 0)
	if err != nil {
		t.Fatal(err)
	}
	var pr wire.PaymentRequired
	if err := json.Unmarshal(cj, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Resource.URL != spec.WireURL {
		t.Fatalf("topup wire url = %q", pr.Resource.URL)
	}
	if len(pr.Accepts) != 2 {
		t.Fatalf("dual-method accepts = %d, want 2", len(pr.Accepts))
	}
	if got := bazaarToolName(t, pr); got != "topup" {
		t.Fatalf("bazaar toolName = %q", got)
	}
}
