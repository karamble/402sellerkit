// Package dcr402rail implements seam.Rail for the Decred Lightning rail:
// Lightning-settled x402 challenges minted and verified by the dcr402 gate
// (github.com/karamble/dcr402/lib), priced in USD and converted to DCR atoms
// at the live exchange rate. It is a thin adapter over lib.Gate; the service
// owns the dcrlnd node wiring. Its own go.mod keeps the Decred dependency
// tree out of the 402sellerkit core.
package dcr402rail
