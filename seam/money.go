package seam

// USDMicros is money in millionths of a US dollar (1e-6 USD). It maps 1:1 to
// USDC's 6-decimal atomic unit and can express sub-cent prices (a $0.001 call =
// 1000). All prices and settled amounts are carried in this unit; floats appear
// only at rail-native conversion or display.
type USDMicros int64

// MicrosPerUSD is the scale factor: 1 USD = 1e6 USDMicros.
const MicrosPerUSD = 1_000_000

// Dollars renders the amount as a float dollar value, for rail SDKs (e.g. the
// x402 exact scheme's Price field) and display only. Never use it for
// accounting.
func (m USDMicros) Dollars() float64 { return float64(m) / MicrosPerUSD }
