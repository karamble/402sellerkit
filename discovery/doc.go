// Package discovery publishes a service's paid resources to bazaar-shaped
// discovery indexes so agents can find them. It is the submit (publish) side:
// a Descriptor in the x402 v2 discovery shape is POSTed to one or more index
// URLs at <URL>/discovery/submit (the dcrfac shape). The read side is handled
// by buyers using the stock x402 WithBazaar client against an index that
// mirrors our submissions.
//
// The x402 v2 discovery shape is shared wire code, so this lives in core and
// stays dependency-free; rails contribute only their accepts entries (as raw
// JSON, e.g. via AcceptsFromChallenge). Listing is advertising, not settlement:
// an index holds no funds, and the authoritative payment terms always come from
// the origin's live 402, so a lying index cannot redirect payment.
package discovery
