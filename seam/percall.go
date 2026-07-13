package seam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// ErrNoRail is returned by PerCall.Verify when no composed rail claims the
// presented payment payload.
var ErrNoRail = errors.New("seam: no rail claims this payment payload")

// PerCall multiplexes an ordered set of per-call rails into one payment
// surface for transports where the work runs between verify and settle -
// the MCP mirror of Composer. It composes the merged payment-required
// object and demultiplexes an inbound payment to the owning rail.
//
// Lifecycle for one payable call:
//
//	no payment presented  -> Challenge -> return it as the error result
//	payment presented     -> Verify -> run the work -> Settle -> attach meta
//
// Settle reports whether the settlement was billed for the first time. A
// replayed proof still verifies (the rail answers idempotently), so a
// service whose work must not re-run keys its own result cache on the
// verified ref rather than re-executing.
type PerCall struct {
	service string
	rails   []PerCallRail
	seen    SeenRefs
	log     *slog.Logger
}

// NewPerCall builds a PerCall over an ordered set of rails. Rail order is
// the demultiplex order for inbound payments. service names the seller in
// the payment-required resource block. A nil logger defaults to
// slog.Default.
func NewPerCall(service string, seen SeenRefs, log *slog.Logger, rails ...PerCallRail) *PerCall {
	if log == nil {
		log = slog.Default()
	}
	return &PerCall{service: service, rails: rails, seen: seen, log: log}
}

// Challenge renders the merged x402 v2 PaymentRequired object for one
// payable call: every rail's accepts entries in one accepts array,
// first-writer-wins on extensions. Transports embed it directly - as an MCP
// tool result's structuredContent, or any other error-result body.
func (p *PerCall) Challenge(ctx context.Context, resource string, amount USDMicros) (json.RawMessage, error) {
	var accepts []json.RawMessage
	exts := map[string]json.RawMessage{}
	for _, rail := range p.rails {
		as, es, err := rail.MCPAccepts(ctx, resource, amount)
		if err != nil {
			p.log.Error("rail MCPAccepts failed", "resource", resource, "err", err)
			continue
		}
		accepts = append(accepts, as...)
		for k, v := range es {
			if _, taken := exts[k]; !taken {
				exts[k] = v
			}
		}
	}
	if len(accepts) == 0 {
		return nil, fmt.Errorf("seam: no rail minted a challenge for %s", resource)
	}
	body := map[string]any{
		"x402Version": 2,
		"error":       "payment_required",
		"resource": map[string]any{
			"url":         resource,
			"mimeType":    "application/json",
			"serviceName": p.service,
		},
		"accepts": accepts,
	}
	if len(exts) > 0 {
		body["extensions"] = exts
	}
	return json.Marshal(body)
}

// VerifiedCall is a demultiplexed, rail-verified but unsettled per-call
// payment: hold it across the work, then Settle it.
type VerifiedCall struct {
	rail    PerCallRail
	payment PerCallPayment
}

// Verify walks rails in order; the owning rail verifies the payment without
// consuming it. A rail returns (nil, nil) for a payload it does not own, so
// the ordered walk cleanly demultiplexes. A present-but-invalid payment
// surfaces as the rail's error; ErrNoRail means nothing claimed it.
func (p *PerCall) Verify(ctx context.Context, resource string, raw json.RawMessage, amount USDMicros) (*VerifiedCall, error) {
	for _, rail := range p.rails {
		pay, err := rail.MCPVerify(ctx, resource, raw, amount)
		if err != nil {
			return nil, err
		}
		if pay != nil {
			return &VerifiedCall{rail: rail, payment: pay}, nil
		}
	}
	return nil, ErrNoRail
}

// Settle consumes a verified payment and enforces bill-once. It returns the
// rail's Settlement, the transport meta to attach to the successful result
// (e.g. the x402/payment-response receipt), and whether this ExternalRef
// settled for the first time - false is a replay the service must not
// treat as new revenue.
func (p *PerCall) Settle(ctx context.Context, vc *VerifiedCall) (*Settlement, map[string]any, bool, error) {
	s, meta, err := vc.rail.MCPSettle(ctx, vc.payment)
	if err != nil {
		return nil, nil, false, err
	}
	first := p.seen.MarkSettled(s.ExternalRef)
	if first {
		p.log.Info("per-call settled", "ref", s.ExternalRef, "rail", s.Rail, "usd_micros", int64(s.AmountUSDMicros))
	} else {
		p.log.Info("per-call settlement replayed, not re-charged", "ref", s.ExternalRef, "rail", s.Rail)
	}
	return s, meta, first, nil
}
