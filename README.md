# 402sellerkit

The reusable seller framework for putting any service behind payment across
multiple rails. A service imports the lean core (`seam`, `prices`) plus the rail
modules it enables; the framework composes the dual 402 challenge, demultiplexes
an inbound proof to the owning rail, and enforces bill-once settlement.

The design is generalized from satportal's proven `internal/pay` seam and the
`dcr402/lib` gate. Boundary and rationale live in the 402works vault
(ADR-0002 Rail plugin architecture, the Seller blueprint note).

## Layout

    seam/            the universal settlement contract: Rail / PerCallRail
                     interfaces, ChallengeSpec / Settlement types, the Composer
    prices/          resource -> USD-micros table (prices.json), enforced
    present/         challenge (x402 v2 wire helpers) + checkout (fiat) - stubs
    discovery/       x402 v2 Bazaar descriptor + submit client - stub
    facilitator/     client to a self-hosted verify/settle endpoint - stub
    rails/fake/      dependency-free stub rail (dev + reference service)
    rails/dcr402/    Decred Lightning rail (own module) - stub
    rails/x402usdc/  x402 USDC-on-Base rail (own module) - stub
    cmd/exampled/    reference service: /hello gated with the fake rail

The core module is dependency-free (stdlib only). Each real rail lives in its
own module so a service pulls only the dependencies of the rails it enables.

## Try it

    go run ./cmd/exampled -addr :8402
    curl -i localhost:8402/hello                      # 402 + challenge
    curl -i -H 'X-Fake-Payment: ref-1' localhost:8402/hello   # 200 + resource
