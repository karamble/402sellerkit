package dcr402rail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	chaincfg "github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/zpay32"

	lib "github.com/karamble/dcr402/lib"
	"github.com/karamble/dcr402/lib/store"
	wire "github.com/karamble/dcr402/lib/x402"

	"github.com/karamble/402sellerkit/seam"
)

// fakeNode is an in-process stand-in for the seller's dcrlnd: it holds a key,
// signs real zpay32 invoices, and remembers each preimage so the test can
// present it (as a real buyer would after paying). No network, no node.
type fakeNode struct {
	params    *chaincfg.Params
	key       *secp256k1.PrivateKey
	mu        sync.Mutex
	preimages map[[32]byte][32]byte
}

func newFakeNode(t *testing.T, params *chaincfg.Params) (*fakeNode, string) {
	t.Helper()
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	payTo := hex.EncodeToString(key.PubKey().SerializeCompressed())
	return &fakeNode{params: params, key: key, preimages: map[[32]byte][32]byte{}}, payTo
}

func (n *fakeNode) CreateInvoice(_ context.Context, amountAtoms int64, memo string, expiry time.Duration) (string, [32]byte, error) {
	var preimage [32]byte
	if _, err := rand.Read(preimage[:]); err != nil {
		return "", [32]byte{}, err
	}
	hash := sha256.Sum256(preimage[:])
	inv, err := zpay32.NewInvoice(n.params, hash, time.Now(),
		zpay32.Amount(lnwire.MilliAtom(amountAtoms*1000)),
		zpay32.Destination(n.key.PubKey()),
		zpay32.Description(memo),
		zpay32.Expiry(expiry),
	)
	if err != nil {
		return "", [32]byte{}, err
	}
	encoded, err := inv.Encode(zpay32.MessageSigner{
		SignCompact: func(msg []byte) ([]byte, error) {
			h := chainhash.HashB(msg)
			return ecdsa.SignCompact(n.key, h, true), nil
		},
	})
	if err != nil {
		return "", [32]byte{}, err
	}
	n.mu.Lock()
	n.preimages[hash] = preimage
	n.mu.Unlock()
	return encoded, hash, nil
}

func (n *fakeNode) LookupInvoice(_ context.Context, hash [32]byte) (lib.InvoiceStatus, error) {
	n.mu.Lock()
	_, ok := n.preimages[hash]
	n.mu.Unlock()
	return lib.InvoiceStatus{Settled: ok}, nil // pretend the buyer paid
}

func (n *fakeNode) preimageFor(hash [32]byte) ([32]byte, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p, ok := n.preimages[hash]
	return p, ok
}

// fixedRate is a deterministic RateSource so the test needs no dcrdata call.
type fixedRate float64

func (r fixedRate) USDPerDCR(context.Context) (float64, error) { return float64(r), nil }

// TestFullSettleFlowNoNode exercises the whole dcr402 payment path - mint a
// challenge, present the preimage, settle - through NewWithBackend against a
// fake node, with no dcrlnd and no network.
func TestFullSettleFlowNoNode(t *testing.T) {
	ctx := context.Background()
	node, payTo := newFakeNode(t, lib.Simnet.Params)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "settle.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	rail, err := NewWithBackend(Config{
		Network: "simnet",
		PayTo:   payTo,
		Service: "test",
		Rate:    fixedRate(20),
	}, node, st)
	if err != nil {
		t.Fatalf("NewWithBackend: %v", err)
	}
	defer rail.Close()

	spec := seam.ChallengeSpec{Purpose: "access", Resource: "/hello", AmountUSDMicros: 10000}

	// 1) Mint a challenge (persists it; the fake node signs a real invoice).
	cj, err := rail.ChallengeJSON(ctx, spec)
	if err != nil {
		t.Fatalf("ChallengeJSON: %v", err)
	}
	var pr wire.PaymentRequired
	if err := json.Unmarshal(cj, &pr); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}
	if len(pr.Accepts) != 1 {
		t.Fatalf("accepts = %d, want 1", len(pr.Accepts))
	}
	offered := pr.Accepts[0]

	// 2) Recover the payment hash and the preimage the node generated.
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

	// 3) Build the payment payload a buyer would present.
	present := func(preimageHex string) http.Header {
		lnPay, _ := json.Marshal(wire.LightningPayload{Preimage: preimageHex, PaymentHash: extra.PaymentHash})
		ppRaw, _ := json.Marshal(wire.PaymentPayload{X402Version: 2, Accepted: offered, Payload: lnPay})
		h := http.Header{}
		h.Set(HeaderPaymentSignature, base64.StdEncoding.EncodeToString(ppRaw))
		return h
	}

	// 4) Settle - full cryptographic verify (preimage, invoice decode,
	// destination, amount) plus the mint-of-record check, no node.
	settlement, err := rail.TrySettle(ctx, present(hex.EncodeToString(preimage[:])), spec)
	if err != nil {
		t.Fatalf("TrySettle: %v", err)
	}
	if settlement == nil {
		t.Fatal("expected a settlement, got nil")
	}
	if settlement.Rail != RailName {
		t.Errorf("Rail = %q, want %q", settlement.Rail, RailName)
	}
	if settlement.AmountUSDMicros != spec.AmountUSDMicros {
		t.Errorf("amount = %d, want %d", settlement.AmountUSDMicros, spec.AmountUSDMicros)
	}
	if !strings.HasPrefix(settlement.ExternalRef, RailName+":") {
		t.Errorf("ExternalRef = %q, want %s: prefix", settlement.ExternalRef, RailName)
	}

	// 5) A wrong preimage for the same challenge must be rejected.
	if _, err := rail.TrySettle(ctx, present(hex.EncodeToString(make([]byte, 32))), spec); err == nil {
		t.Error("expected rejection of a wrong preimage")
	}
}
