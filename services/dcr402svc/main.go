// Command dcr402svc is a reference paid service on the Decred Lightning rail:
// it puts a priced HTTP endpoint behind dcr402 payment, minting and verifying
// preimage proofs against the seller's OWN dcrlnd node - no facilitator, no
// third party (the dcr402 default deployment). It wires 402sellerkit end to
// end: rail -> composer -> gated handler -> discovery self-listing.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	onchain := flag.Bool("onchain", false, "offer the onchain method on top-up challenges (deposit addresses via the same dcrlnd)")
	topupMin := flag.Int64("topup-min-usd-micros", 1_000_000, "smallest accepted top-up (default $1)")
	topupMax := flag.Int64("topup-max-usd-micros", 100_000_000, "largest accepted top-up (default $100)")
	rateURL := flag.String("rate-url", "", "dcrdata API base (\"\" = public dcrdata)")
	rateFallback := flag.Float64("rate-fallback", 0, "static USD-per-DCR floor when the rate source is down (0 = none)")
	pricesPath := flag.String("prices", "prices.json", "path to prices.json")
	index := flag.String("discovery", "", "bazaar index base URL to self-list on (optional)")
	baseURL := flag.String("base-url", "", "public base URL for discovery listings (e.g. https://api.example.com); empty advertises the relative path")
	flag.Parse()

	if *payTo == "" || *lndHost == "" || *lndCert == "" || *lndMac == "" {
		log.Fatal("dcr402svc: -pay-to, -lnd-host, -lnd-tlscert and -lnd-macaroon are required")
	}
	table, err := prices.Load(*pricesPath)
	if err != nil {
		log.Fatalf("dcr402svc: load prices: %v", err)
	}
	rail, err := dcr402rail.New(dcr402rail.Config{
		Network:       *network,
		PayTo:         *payTo,
		Service:       "dcr402svc",
		LNDHost:       *lndHost,
		LNDTLSCert:    *lndCert,
		LNDMacaroon:   *lndMac,
		StorePath:     *storePath,
		RateURL:       *rateURL,
		RateFallback:  *rateFallback,
		EnableOnchain: *onchain,
	})
	if err != nil {
		log.Fatalf("dcr402svc: %v", err)
	}
	defer rail.Close()

	comp := seam.NewComposer(table, seam.NewMemSeenRefs(), slog.Default(), rail)

	mux := http.NewServeMux()
	mux.Handle("/topup", topupHandler(rail, comp, *topupMin, *topupMax))
	mux.Handle("/hello", comp.Require("access", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world","paid":true}` + "\n"))
	})))

	if *index != "" {
		selfList(rail, table, *index, strings.TrimRight(*baseURL, "/"))
	}

	slog.Info("dcr402svc listening", "addr", *addr, "network", *network, "payTo", *payTo, "lnd", *lndHost)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// topupHandler is the buyer-chosen-amount credit top-up: an unpaid request
// (?usd_micros=N) receives the dual-method challenge - a Lightning invoice
// and, with -onchain, a deposit address - and a retry carrying
// PAYMENT-SIGNATURE settles through the composer. A real service credits
// its own ledger where this responds; the bill-once guard has already run.
func topupHandler(rail *dcr402rail.Rail, comp *seam.Composer, minUSD, maxUSD int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usd, err := strconv.ParseInt(r.FormValue("usd_micros"), 10, 64)
		if err != nil || usd < minUSD || usd > maxUSD {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "usd_micros required", "min": minUSD, "max": maxUSD,
			})
			return
		}
		spec := seam.ChallengeSpec{
			Purpose:         "topup",
			Resource:        "/topup",
			AmountUSDMicros: seam.USDMicros(usd),
			TTL:             time.Hour,
		}
		s, first, err := comp.SettleRequest(r.Context(), r.Header, spec)
		if err != nil {
			if errors.Is(err, dcr402rail.ErrPendingConfirmation) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending", "detail": err.Error()})
				return
			}
			http.Error(w, "invalid payment: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if s == nil {
			if err := rail.WriteTopup402(r.Context(), w.Header(), spec, 2); err != nil {
				http.Error(w, "minting top-up challenge: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusPaymentRequired)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "payment_required", "amount_usd_micros": usd,
				"detail": "pay any offered method and retry with PAYMENT-SIGNATURE",
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":            map[bool]string{true: "credited", false: "already_credited"}[first],
			"rail":              s.Rail,
			"amount_usd_micros": int64(s.AmountUSDMicros),
			"external_ref":      s.ExternalRef,
		})
	})
}

// selfList advertises /hello on a bazaar index by minting a representative
// challenge and publishing its accepts entries. Minting a Lightning challenge
// needs the node, so this is a no-op (logged) until dcrlnd is reachable.
func selfList(rail *dcr402rail.Rail, table *prices.Table, index, baseURL string) {
	// With a public base URL, the listing and the wire 402 advertise the
	// absolute endpoint; binding stays on the relative site either way.
	wireURL := ""
	resource := "/hello"
	if baseURL != "" {
		wireURL = baseURL + "/hello"
		resource = wireURL
	}
	q, ok := table.QuoteFor("/hello")
	if !ok {
		slog.Warn("dcr402svc: no price for /hello; skipping discovery")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cj, err := rail.ChallengeJSON(ctx, seam.ChallengeSpec{Purpose: "access", Resource: "/hello", WireURL: wireURL, AmountUSDMicros: q.USDMicros})
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
		Resource:    resource,
		Accepts:     accepts,
		ServiceName: "dcr402svc",
		Description: "reference paid hello",
		MimeType:    "application/json",
	})
	slog.Info("dcr402svc: self-listed in discovery", "indexes", n)
}
