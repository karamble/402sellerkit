// Package seam is the universal settlement contract at the heart of
// 402sellerkit: the rail interfaces every payment plugin implements, the types
// they exchange, and the Composer that multiplexes them into one payment
// surface. It is deliberately stdlib-only so the core module stays
// dependency-free; heavy rail SDKs live in their own modules under rails/.
//
// The shape generalizes satportal's proven internal/pay seam: two interaction
// modes rather than one. Rail covers challenge-shaped sites (settle before
// serve). PerCallRail covers per-call work (verify, run, then settle, so a
// failed settle withholds the result).
package seam
