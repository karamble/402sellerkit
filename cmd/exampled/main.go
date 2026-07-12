// Command exampled is the 402sellerkit reference service: the copy-paste
// template for putting an endpoint behind payment. It gates a trivial /hello
// route with the fake rail so the seam lifecycle runs end to end. Swap the fake
// rail for a real one (dcr402, x402/USDC) and point prices.json at real prices
// to ship a paid service.
package main

import (
	"flag"
	"log"
	"log/slog"
	"net/http"

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

	comp := seam.NewComposer(table, seam.NewMemSeenRefs(), slog.Default(), fake.New())

	hello := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world","paid":true}` + "\n"))
	})

	mux := http.NewServeMux()
	mux.Handle("/hello", comp.Require("access", hello))

	slog.Info("exampled listening", "addr", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
