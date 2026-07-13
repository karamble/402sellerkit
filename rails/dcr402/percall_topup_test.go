package dcr402rail

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	lib "github.com/karamble/dcr402/lib"
	"github.com/karamble/dcr402/lib/store"
	wire "github.com/karamble/dcr402/lib/x402"

	"github.com/karamble/402sellerkit/seam"
)

// fakeDualNode extends the fake Lightning node with a scriptable onchain
// backend, mirroring lib's own test double, so the dual-method top-up flow
// runs with no node and no chain.
type fakeDualNode struct {
	*fakeNode
	mu       sync.Mutex
	nextAddr int
	deposits map[string]lib.DepositStatus // txid -> status
}

func newFakeDualNode(t *testing.T) (*fakeDualNode, string) {
	t.Helper()
	ln, payTo := newFakeNode(t, lib.Simnet.Params)
	return &fakeDualNode{fakeNode: ln, deposits: map[string]lib.DepositStatus{}}, payTo
}

func (n *fakeDualNode) NewDepositAddress(context.Context) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nextAddr++
	return fmt.Sprintf("SsFakeDeposit%06d", n.nextAddr), nil
}

func (n *fakeDualNode) LookupDeposit(_ context.Context, txid, _ string) (lib.DepositStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.deposits[txid], nil
}

func (n *fakeDualNode) putDeposit(txid string, dep lib.DepositStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.deposits[txid] = dep
}

func newTestRail(t *testing.T, enableOnchain bool) (*Rail, *fakeDualNode) {
	t.Helper()
	node, payTo := newFakeDualNode(t)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "rail.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	rail, err := NewWithBackend(Config{
		Network:       "simnet",
		PayTo:         payTo,
		Service:       "test",
		Rate:          fixedRate(20),
		EnableOnchain: enableOnchain,
	}, node, st)
	if err != nil {
		t.Fatalf("NewWithBackend: %v", err)
	}
	t.Cleanup(func() { rail.Close() })
	return rail, node
}

// lightningPaymentRaw pays the given lightning accepts entry with the fake
// node's preimage and returns the raw PaymentPayload JSON.
func lightningPaymentRaw(t *testing.T, node *fakeDualNode, offered wire.PaymentRequirements) json.RawMessage {
	t.Helper()
	var extra wire.LightningExtra
	if err := json.Unmarshal(offered.Extra, &extra); err != nil {
		t.Fatalf("extra: %v", err)
	}
	hashRaw, err := hex.DecodeString(extra.PaymentHash)
	if err != nil || len(hashRaw) != 32 {
		t.Fatalf("bad payment hash %q", extra.PaymentHash)
	}
	var hash [32]byte
	copy(hash[:], hashRaw)
	preimage, ok := node.preimageFor(hash)
	if !ok {
		t.Fatal("node has no preimage for the minted hash")
	}
	lnPay, _ := json.Marshal(wire.LightningPayload{
		Preimage:    hex.EncodeToString(preimage[:]),
		PaymentHash: extra.PaymentHash,
	})
	raw, _ := json.Marshal(wire.PaymentPayload{X402Version: 2, Accepted: offered, Payload: lnPay})
	return raw
}

func acceptsByMethod(t *testing.T, pr wire.PaymentRequired) map[string]wire.PaymentRequirements {
	t.Helper()
	out := map[string]wire.PaymentRequirements{}
	for _, a := range pr.Accepts {
		out[a.TransferMethod()] = a
	}
	return out
}

// TestPerCallFlowNoNode exercises the seam.PerCallRail path: accepts mint,
// verify without consuming, settle after the work, replay detection at the
// gate, and the binding rejections.
func TestPerCallFlowNoNode(t *testing.T) {
	ctx := context.Background()
	rail, node := newTestRail(t, false)
	resource := "mcp://tool/generate"
	amount := seam.USDMicros(25000)

	accepts, _, err := rail.MCPAccepts(ctx, resource, amount)
	if err != nil {
		t.Fatalf("MCPAccepts: %v", err)
	}
	if len(accepts) != 1 {
		t.Fatalf("accepts = %d, want 1 (lightning)", len(accepts))
	}
	var offered wire.PaymentRequirements
	if err := json.Unmarshal(accepts[0], &offered); err != nil {
		t.Fatal(err)
	}
	if offered.TransferMethod() != wire.MethodLightning {
		t.Fatalf("per-call method = %q, want lightning", offered.TransferMethod())
	}
	raw := lightningPaymentRaw(t, node, offered)

	// Foreign payloads stay unclaimed.
	if p, err := rail.MCPVerify(ctx, resource, json.RawMessage(`{"accepted":{"network":"eip155:8453"}}`), amount); p != nil || err != nil {
		t.Fatalf("foreign payload: p=%v err=%v, want nil/nil", p, err)
	}
	// Wrong resource is rejected before the work runs.
	if _, err := rail.MCPVerify(ctx, "mcp://tool/other", raw, amount); err == nil {
		t.Fatal("want binding rejection for another resource")
	}
	// Wrong amount likewise.
	if _, err := rail.MCPVerify(ctx, resource, raw, amount+1); err == nil {
		t.Fatal("want binding rejection for another amount")
	}

	p, err := rail.MCPVerify(ctx, resource, raw, amount)
	if err != nil {
		t.Fatalf("MCPVerify: %v", err)
	}
	s, meta, err := rail.MCPSettle(ctx, p)
	if err != nil {
		t.Fatalf("MCPSettle: %v", err)
	}
	if s.Rail != RailName || s.AmountUSDMicros != amount {
		t.Fatalf("settlement = %+v", s)
	}
	if !strings.HasPrefix(s.ExternalRef, RailName+":") {
		t.Fatalf("ExternalRef = %q", s.ExternalRef)
	}
	if _, ok := meta[lib.MetaPaymentResponse]; !ok {
		t.Fatalf("meta lacks %s: %v", lib.MetaPaymentResponse, meta)
	}

	// Idempotent re-presentation settles again without error (the gate
	// answers from the stored response; bill-once is the seam's job).
	p2, err := rail.MCPVerify(ctx, resource, raw, amount)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	s2, _, err := rail.MCPSettle(ctx, p2)
	if err != nil {
		t.Fatalf("re-settle: %v", err)
	}
	if s2.ExternalRef != s.ExternalRef {
		t.Fatalf("replay ref = %q, want %q", s2.ExternalRef, s.ExternalRef)
	}
}

// TestTopupDualMethodNoNode exercises the buyer-chosen-amount top-up: both
// methods offered, the lightning proof settles through the ordinary
// TrySettle, and the challenge binds to the top-up site and amount.
func TestTopupDualMethodNoNode(t *testing.T) {
	ctx := context.Background()
	rail, node := newTestRail(t, true)
	spec := seam.ChallengeSpec{Purpose: "topup", Resource: "/api/topup", AmountUSDMicros: 5_000_000}

	cj, err := rail.TopupChallengeJSON(ctx, spec, 2)
	if err != nil {
		t.Fatalf("TopupChallengeJSON: %v", err)
	}
	var pr wire.PaymentRequired
	if err := json.Unmarshal(cj, &pr); err != nil {
		t.Fatal(err)
	}
	byMethod := acceptsByMethod(t, pr)
	if len(byMethod) != 2 {
		t.Fatalf("methods = %v, want lightning + onchain", byMethod)
	}

	raw := lightningPaymentRaw(t, node, byMethod[wire.MethodLightning])
	h := http.Header{}
	h.Set(HeaderPaymentSignature, base64.StdEncoding.EncodeToString(raw))

	// A drifted amount must not settle the challenge.
	badSpec := spec
	badSpec.AmountUSDMicros = 6_000_000
	if _, err := rail.TrySettle(ctx, h, badSpec); err == nil {
		t.Fatal("want binding rejection for a drifted top-up amount")
	}

	s, err := rail.TrySettle(ctx, h, spec)
	if err != nil {
		t.Fatalf("TrySettle: %v", err)
	}
	if s == nil || s.Rail != RailName || s.AmountUSDMicros != spec.AmountUSDMicros {
		t.Fatalf("settlement = %+v", s)
	}
}

// TestTopupOnchainSettleNoChain exercises the onchain half: deposit proof
// settles at depth, ErrPendingConfirmation below depth, distinct ref shape.
func TestTopupOnchainSettleNoChain(t *testing.T) {
	ctx := context.Background()
	rail, node := newTestRail(t, true)
	spec := seam.ChallengeSpec{Purpose: "topup", Resource: "/api/topup", AmountUSDMicros: 5_000_000}

	cj, err := rail.TopupChallengeJSON(ctx, spec, 2)
	if err != nil {
		t.Fatalf("TopupChallengeJSON: %v", err)
	}
	var pr wire.PaymentRequired
	if err := json.Unmarshal(cj, &pr); err != nil {
		t.Fatal(err)
	}
	offered, ok := acceptsByMethod(t, pr)[wire.MethodOnchain]
	if !ok {
		t.Fatal("no onchain method offered")
	}
	wantAtoms, err := AtomsForUSDMicros(int64(spec.AmountUSDMicros), 20)
	if err != nil {
		t.Fatal(err)
	}

	txid := strings.Repeat("ab", 32)
	ocPay, _ := json.Marshal(wire.OnchainPayload{TxID: txid})
	raw, _ := json.Marshal(wire.PaymentPayload{X402Version: 2, Accepted: offered, Payload: ocPay})
	h := http.Header{}
	h.Set(HeaderPaymentSignature, base64.StdEncoding.EncodeToString(raw))

	// One confirmation of two: pending, not invalid.
	node.putDeposit(txid, lib.DepositStatus{Found: true, Confirmations: 1, AmountToAddressAtoms: wantAtoms})
	if _, err := rail.TrySettle(ctx, h, spec); !errors.Is(err, ErrPendingConfirmation) {
		t.Fatalf("below depth: err = %v, want ErrPendingConfirmation", err)
	}

	// At depth: settles with the onchain ref shape.
	node.putDeposit(txid, lib.DepositStatus{Found: true, Confirmations: 2, AmountToAddressAtoms: wantAtoms})
	s, err := rail.TrySettle(ctx, h, spec)
	if err != nil {
		t.Fatalf("TrySettle: %v", err)
	}
	wantRef := RailName + ":onchain:" + txid
	if s == nil || s.ExternalRef != wantRef {
		t.Fatalf("settlement = %+v, want ref %q", s, wantRef)
	}
}

// TestEnableOnchainRequiresCapableBackend pins the constructor guard.
func TestEnableOnchainRequiresCapableBackend(t *testing.T) {
	node, payTo := newFakeNode(t, lib.Simnet.Params)
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "rail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := NewWithBackend(Config{
		Network: "simnet", PayTo: payTo, Service: "test", Rate: fixedRate(20),
		EnableOnchain: true,
	}, node, st); err == nil {
		t.Fatal("want rejection: fakeNode has no onchain half")
	}
}
