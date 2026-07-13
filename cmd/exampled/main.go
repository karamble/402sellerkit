// Command exampled is the 402sellerkit reference service: the copy-paste
// template for putting an endpoint behind payment. It gates a trivial /hello
// route with the fake rail so the seam lifecycle runs end to end, and now
// demonstrates the other two payment shapes as well: a buyer-chosen-amount
// top-up (/topup) and a per-call verify -> run -> settle site (/percall/hello).
// Swap the fake rail for a real one (dcr402, x402/USDC) and point prices.json
// at real prices to ship a paid service.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/karamble/402sellerkit/prices"
	"github.com/karamble/402sellerkit/rails/fake"
	"github.com/karamble/402sellerkit/seam"
)

func main() {
	addr := flag.String("addr", ":8402", "listen address")
	pricesPath := flag.String("prices", "cmd/exampled/prices.json", "path to prices.json")
	flag.Parse()

	table, err := prices.Load(*pricesPath)
	if err != nil {
		log.Fatalf("load prices: %v", err)
	}

	rail := fake.New()
	seen := seam.NewMemSeenRefs()
	comp := seam.NewComposer(table, seen, slog.Default(), rail)

	hello := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world","paid":true}` + "\n"))
	})

	mux := http.NewServeMux()
	mux.Handle("/hello", comp.Require("access", hello))
	mux.Handle("/topup", topupHandler(comp))
	mux.Handle("/percall/hello", perCallHandler(seam.NewPerCall("exampled", seen, slog.Default(), rail)))

	slog.Info("exampled listening", "addr", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// topupHandler is the variable-amount site: the buyer names the amount
// (?usd_micros=N) instead of the price table, so it builds its own
// ChallengeSpec and uses WriteChallenge/SettleRequest directly rather than
// Require. A real service would credit its own ledger where this prints.
func topupHandler(comp *seam.Composer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usd, err := strconv.ParseInt(r.FormValue("usd_micros"), 10, 64)
		if err != nil || usd <= 0 {
			http.Error(w, `{"error":"usd_micros parameter required"}`, http.StatusBadRequest)
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
			http.Error(w, "invalid payment: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if s == nil {
			comp.WriteChallenge(r.Context(), w, spec)
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

// perCallHandler is the verify -> run -> settle site, transport-flattened
// for the demo: the payment payload rides a JSON body field where an MCP
// server would read params._meta["x402/payment"]. No payment -> the merged
// payment-required object as an error body; a payment -> verify, run the
// work, settle, and only then deliver.
func perCallHandler(pc *seam.PerCall) http.Handler {
	const price = seam.USDMicros(25_000) // $0.025 per call
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Payment json.RawMessage `json:"payment"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		if len(req.Payment) == 0 {
			challenge, err := pc.Challenge(r.Context(), "mcp://tool/hello", price)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write(challenge)
			return
		}

		vc, err := pc.Verify(r.Context(), "mcp://tool/hello", req.Payment, price)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, seam.ErrNoRail) {
				status = http.StatusBadRequest
			}
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), status)
			return
		}

		// The work runs between verify and settle; a failed settle below
		// would withhold this result.
		result := map[string]any{"hello": "world", "paid": true}

		s, meta, first, err := pc.Settle(r.Context(), vc)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":       result,
			"meta":         meta,
			"external_ref": s.ExternalRef,
			"billed":       first,
		})
	})
}
