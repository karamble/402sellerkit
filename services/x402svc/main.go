// Command x402svc is a reference paid service: it puts a priced HTTP endpoint
// behind USDC-on-Base payment via the 402sellerkit x402usdc rail, settling
// through a SELF-HOSTED facilitator (never Coinbase/CDP, per ADR-0001). It
// wires the framework end to end - rail -> composer -> gated handler ->
// discovery self-listing - and is the template for a real paid service.
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
	"github.com/karamble/402sellerkit/rails/x402usdc"
	"github.com/karamble/402sellerkit/seam"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8443", "listen address")
	network := flag.String("network", x402usdc.NetworkBaseSepolia, "eip155:8453 (mainnet) | eip155:84532 (sepolia)")
	payTo := flag.String("pay-to", "", "receiving EVM address (required)")
	facilitator := flag.String("facilitator", "", "self-hosted facilitator URL (required on mainnet; sepolia defaults to the keyless testnet facilitator)")
	pricesPath := flag.String("prices", "prices.json", "path to prices.json")
	index := flag.String("discovery", "", "bazaar index base URL to self-list on (optional)")
	flag.Parse()

	if *payTo == "" {
		log.Fatal("x402svc: -pay-to is required (the receiving EVM address)")
	}
	table, err := prices.Load(*pricesPath)
	if err != nil {
		log.Fatalf("x402svc: load prices: %v", err)
	}
	rail, err := x402usdc.New(x402usdc.Config{
		Network:        *network,
		PayTo:          *payTo,
		FacilitatorURL: *facilitator,
		Service:        "x402svc",
	})
	if err != nil {
		log.Fatalf("x402svc: %v", err)
	}

	comp := seam.NewComposer(table, seam.NewMemSeenRefs(), slog.Default(), rail)

	mux := http.NewServeMux()
	mux.Handle("/hello", comp.Require("access", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world","paid":true}` + "\n"))
	})))

	if *index != "" {
		selfList(rail, table, *index)
	}

	shownFac := *facilitator
	if shownFac == "" {
		shownFac = "(default)"
	}
	slog.Info("x402svc listening", "addr", *addr, "network", *network, "payTo", *payTo, "facilitator", shownFac)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// selfList advertises /hello on a bazaar index by minting a representative
// challenge and publishing its accepts entries. Challenge minting is local, so
// this works even before the facilitator is reachable.
func selfList(rail *x402usdc.Rail, table *prices.Table, index string) {
	q, ok := table.QuoteFor("/hello")
	if !ok {
		slog.Warn("x402svc: no price for /hello; skipping discovery")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cj, err := rail.ChallengeJSON(ctx, seam.ChallengeSpec{Purpose: "access", Resource: "/hello", AmountUSDMicros: q.USDMicros})
	if err != nil {
		slog.Warn("x402svc: discovery mint challenge failed", "err", err)
		return
	}
	accepts, err := discovery.AcceptsFromChallenge(cj)
	if err != nil {
		slog.Warn("x402svc: discovery accepts", "err", err)
		return
	}
	n := discovery.NewMultiplexer(slog.Default(), discovery.Target{URL: index}).RegisterAll(ctx, discovery.Descriptor{
		Resource:    "/hello",
		Accepts:     accepts,
		ServiceName: "x402svc",
		Description: "reference paid hello",
		MimeType:    "application/json",
	})
	slog.Info("x402svc: self-listed in discovery", "indexes", n)
}
