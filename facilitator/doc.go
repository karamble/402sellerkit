// Package facilitator is a client for the x402 v2 facilitator protocol:
// stateless /verify, /settle (which notarizes or moves the payment), and a
// /supported kinds query. It offers two topologies behind one Client interface:
// HTTP points at a self-hosted facilitator (dcrfac, the EVM facilitator), and
// Inline runs verify/settle in-process for maximum sovereignty (the T1
// topology, no third party). The rail-specific payment payload and requirements
// pass through as raw JSON, so core stays dependency-free. The facilitator
// SERVERS live outside this repo - the gas/key-holding server stays off the
// seller side.
package facilitator
