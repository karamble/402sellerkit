// Package x402usdc implements seam.Rail for the x402 USDC-on-Base rail:
// per-payment USDC settlement via the x402-foundation SDK's EVM exact scheme,
// verified and settled through a SELF-HOSTED facilitator - never Coinbase/CDP,
// per ADR-0001. Prices are USD and map 1:1 to USDC's 6 decimals. Its own
// go.mod keeps the x402 SDK and its EVM dependency tree out of the
// 402sellerkit core.
package x402usdc
