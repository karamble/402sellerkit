// Package challenge holds the shared x402 v2 challenge-response wire helpers:
// dual-envelope composition of the PaymentRequired header across rails. The
// x402 v2 shape is our common envelope (dcr402 reuses it), so this merge is
// shared wire code in core, not rail-private - each rail marshals its own typed
// PaymentRequired and hands the JSON to SetOrMerge. Stdlib-only; the core stays
// dependency-free.
package challenge
