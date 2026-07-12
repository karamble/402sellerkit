// Command dcr402svc is a reference paid service on the Decred Lightning rail:
// it puts a priced HTTP endpoint behind dcr402 payment, minting and verifying
// preimage proofs against the seller's OWN dcrlnd node - no facilitator, no
// third party (the dcr402 default deployment). It wires 402sellerkit end to
// end: rail -> composer -> gated handler -> discovery self-listing.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/karamble/402sellerkit/discovery"
	"github.com/karamble/402sellerkit/prices"
	dcr402rail "github.com/karamble/402sellerkit/rails/dcr402"
	"github.com/karamble/402sellerkit/seam"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8444", "listen address")
	network := flag.String("network", "mainnet", "mainnet | testnet3 | simnet")
	payTo := flag.String("pay-to", "", "seller dcrlnd identity pubkey hex, 33-byte compressed (required)")
	lndHost := flag.String("lnd-host", "", "dcrlnd gRPC host:port (required)")
	lndCert := flag.String("lnd-tlscert", "", "dcrlnd tls.cert path (required)")
	lndMac := flag.String("lnd-macaroon", "", "dcrlnd invoice.macaroon path (required)")
	storePath := flag.String("store", "dcr402svc.db", "gate challenge/settlement sqlite path")
	rateURL := flag.String("rate-url", "", "dcrdata API base (\"\" = public dcrdata)")
	rateFallback := flag.Float64("rate-fallback", 0, "static USD-per-DCR floor when the rate source is down (0 = none)")
	pricesPath := flag.String("prices", "prices.json", "path to prices.json")
	index := flag.String("discovery", "", "bazaar index base URL to self-list on (optional)")
	flag.Parse()

	if *payTo == "" || *lndHost == "" || *lndCert == "" || *lndMac == "" {
		log.Fatal("dcr402svc: -pay-to, -lnd-host, -lnd-tlscert and -lnd-macaroon are required")
	}
	table, err := prices.Load(*pricesPath)
	if err != nil {
		log.Fatalf("dcr402svc: load prices: %v", err)
	}
	rail, err := dcr402rail.New(dcr402rail.Config{
		Network:      *network,
		PayTo:        *payTo,
		Service:      "dcr402svc",
		LNDHost:      *lndHost,
		LNDTLSCert:   *lndCert,
		LNDMacaroon:  *lndMac,
		StorePath:    *storePath,
		RateURL:      *rateURL,
		RateFallback: *rateFallback,
	})
	if err != nil {
		log.Fatalf("dcr402svc: %v", err)
	}
	defer rail.Close()

	comp := seam.NewComposer(table, seam.NewMemSeenRefs(), slog.Default(), rail)

	mux := http.NewServeMux()
	mux.Handle("/hello", comp.Require("access", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world","paid":true}` + "\n"))
	})))

	if *index != "" {
		selfList(rail, table, *index)
	}

	slog.Info("dcr402svc listening", "addr", *addr, "network", *network, "payTo", *payTo, "lnd", *lndHost)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// selfList advertises /hello on a bazaar index by minting a representative
// challenge and publishing its accepts entries. Minting a Lightning challenge
// needs the node, so this is a no-op (logged) until dcrlnd is reachable.
func selfList(rail *dcr402rail.Rail, table *prices.Table, index string) {
	q, ok := table.QuoteFor("/hello")
	if !ok {
		slog.Warn("dcr402svc: no price for /hello; skipping discovery")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cj, err := rail.ChallengeJSON(ctx, seam.ChallengeSpec{Purpose: "access", Resource: "/hello", AmountUSDMicros: q.USDMicros})
	if err != nil {
		slog.Warn("dcr402svc: discovery mint challenge failed", "err", err)
		return
	}
	accepts, err := discovery.AcceptsFromChallenge(cj)
	if err != nil {
		slog.Warn("dcr402svc: discovery accepts", "err", err)
		return
	}
	n := discovery.NewMultiplexer(slog.Default(), discovery.Target{URL: index}).RegisterAll(ctx, discovery.Descriptor{
		Resource:    "/hello",
		Accepts:     accepts,
		ServiceName: "dcr402svc",
		Description: "reference paid hello",
		MimeType:    "application/json",
	})
	slog.Info("dcr402svc: self-listed in discovery", "indexes", n)
}
