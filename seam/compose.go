package seam

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// PaymentRequired is the shared 402 body. It is never a dead end: it always
// lists the rails a buyer can pay on, with each rail's challenge object hanging
// off Challenges by rail name.
type PaymentRequired struct {
	AmountUSDMicros USDMicros                  `json:"amount_usd_micros"`
	Resource        string                     `json:"resource"`
	AcceptedRails   []string                   `json:"accepted_rails"`
	Challenges      map[string]json.RawMessage `json:"challenges,omitempty"`
}

// SeenRefs is the bill-once guard. MarkSettled records a Settlement's
// ExternalRef and reports whether this is the first time (true) or a replay
// (false). The production guard is a durable UNIQUE index; the scaffold ships
// an in-memory implementation.
type SeenRefs interface {
	MarkSettled(ref string) (firstTime bool)
}

type memSeen struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewMemSeenRefs returns an in-memory SeenRefs. It is process-local and does
// not survive a restart; a real service backs this with its ledger.
func NewMemSeenRefs() SeenRefs { return &memSeen{seen: map[string]struct{}{}} }

func (m *memSeen) MarkSettled(ref string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.seen[ref]; ok {
		return false
	}
	m.seen[ref] = struct{}{}
	return true
}

// Composer multiplexes an ordered set of rails into one payment surface: it
// composes the dual 402 challenge and demultiplexes an inbound proof to the
// owning rail.
type Composer struct {
	rails  []Rail
	prices Prices
	seen   SeenRefs
	log    *slog.Logger
}

// NewComposer builds a Composer over an ordered set of rails. Rail order is the
// demultiplex order for inbound proofs. A nil logger defaults to slog.Default.
func NewComposer(prices Prices, seen SeenRefs, log *slog.Logger, rails ...Rail) *Composer {
	if log == nil {
		log = slog.Default()
	}
	return &Composer{rails: rails, prices: prices, seen: seen, log: log}
}

// acceptedRails flattens every rail's Names in order.
func (c *Composer) acceptedRails() []string {
	var out []string
	for _, r := range c.rails {
		out = append(out, r.Names()...)
	}
	return out
}

// WriteChallenge emits the dual 402: each rail contributes its header family
// and challenge object, then the shared problem+json body is written once.
func (c *Composer) WriteChallenge(ctx context.Context, w http.ResponseWriter, spec ChallengeSpec) {
	challenges := map[string]json.RawMessage{}
	for _, rail := range c.rails {
		if err := rail.Write402(ctx, w.Header(), spec); err != nil {
			c.log.Error("rail Write402 failed", "rail", rail.Names(), "err", err)
			continue
		}
		cj, err := rail.ChallengeJSON(ctx, spec)
		if err != nil {
			c.log.Error("rail ChallengeJSON failed", "rail", rail.Names(), "err", err)
			continue
		}
		for _, n := range rail.Names() {
			challenges[n] = cj
		}
	}
	body := PaymentRequired{
		AmountUSDMicros: spec.AmountUSDMicros,
		Resource:        spec.Resource,
		AcceptedRails:   c.acceptedRails(),
		Challenges:      challenges,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(body)
}

// trySettle walks rails in order; the first non-nil Settlement wins. A rail
// returns (nil, nil) for a payload it does not own, so the ordered walk cleanly
// demultiplexes. A present-but-invalid credential surfaces as an error.
func (c *Composer) trySettle(ctx context.Context, h http.Header, spec ChallengeSpec) (*Settlement, error) {
	for _, rail := range c.rails {
		s, err := rail.TrySettle(ctx, h, spec)
		if err != nil {
			return nil, err
		}
		if s != nil {
			return s, nil
		}
	}
	return nil, nil
}

// SettleRequest demultiplexes an inbound proof for a custom site - one that
// builds its own ChallengeSpec instead of using Require, such as a
// variable-amount top-up or an account-bound purchase. It walks the rails,
// enforces bill-once on a verified settlement, and reports whether this
// ExternalRef settled for the first time. (nil, false, nil) means no rail
// claimed a payload: write the challenge. The spec must be recomputed
// exactly as it was at mint time - rails bind challenges to site and
// amount, so a drifting spec rejects the proof.
func (c *Composer) SettleRequest(ctx context.Context, h http.Header, spec ChallengeSpec) (*Settlement, bool, error) {
	s, err := c.trySettle(ctx, h, spec)
	if err != nil || s == nil {
		return nil, false, err
	}
	first := c.seen.MarkSettled(s.ExternalRef)
	if first {
		c.log.Info("settled", "ref", s.ExternalRef, "rail", s.Rail, "usd_micros", int64(s.AmountUSDMicros))
	} else {
		c.log.Info("replayed settlement, not re-charged", "ref", s.ExternalRef, "rail", s.Rail)
	}
	return s, first, nil
}

// Require gates an http.Handler behind payment. It derives the resource from
// the request path, looks up the price, and either serves (on a verified
// settlement) or writes the dual 402 challenge. purpose labels the site.
func (c *Composer) Require(purpose string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := r.URL.Path
		price, ok := c.prices.PriceFor(resource)
		if !ok {
			http.Error(w, "no price for resource", http.StatusNotFound)
			return
		}
		spec := ChallengeSpec{
			Purpose:         purpose,
			Resource:        resource,
			AmountUSDMicros: price,
			TTL:             time.Hour,
		}
		s, err := c.trySettle(r.Context(), r.Header, spec)
		if err != nil {
			// A credential was present but invalid: 401, never 402. Do not
			// invite a re-pay for a broken proof.
			http.Error(w, "invalid payment: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if s == nil {
			c.WriteChallenge(r.Context(), w, spec)
			return
		}
		// Verified settlement: enforce bill-once, then merge the rail's receipt
		// headers and serve.
		if c.seen.MarkSettled(s.ExternalRef) {
			c.log.Info("settled", "ref", s.ExternalRef, "rail", s.Rail, "usd_micros", int64(s.AmountUSDMicros))
		} else {
			c.log.Info("replayed settlement, not re-charged", "ref", s.ExternalRef, "rail", s.Rail)
		}
		for k, vs := range s.RespHeaders {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		next.ServeHTTP(w, r)
	})
}
