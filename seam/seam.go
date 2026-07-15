package seam

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ChallengeSpec describes one challenge-shaped payment site: what is being paid
// for, how much, and the context a rail needs to mint and bind a challenge. It
// is the input every Rail method receives.
type ChallengeSpec struct {
	Purpose         string        // e.g. "access", "topup", "register"
	Resource        string        // binding key: absolute URL (REST) or mcp://tool/... (MCP)
	AmountUSDMicros USDMicros     // price of this site
	AccountID       string        // "" when the caller is unauthenticated
	Body            []byte        // exact request body, for rails that digest-bind it
	TTL             time.Duration // challenge validity
	// WireURL, when set, is the public resource URL the 402 advertises in
	// place of the Resource-derived one - an MCP server's streamable-HTTP
	// endpoint, or a REST URL cleaned of binding decorations. Settlement
	// still binds to Resource, so a shared wire URL never loosens per-site
	// binding.
	WireURL string
	// MCPTool, when set, names the MCP tool this site sells; rails that
	// speak the bazaar discovery extension advertise it (with InputSchema)
	// so a facilitator can catalog the (WireURL, MCPTool) tuple.
	MCPTool     string
	InputSchema json.RawMessage // optional MCP tool input JSON-Schema
}

// Settlement is a verified, settled payment reported by a rail. Every field is
// rail-verified against the ChallengeSpec, never client-trusted.
type Settlement struct {
	Rail            string      // the rail name that settled (see Rail.Names)
	Payer           string      // rail-native payer id; "" when the rail reveals none
	AmountUSDMicros USDMicros   // verified amount
	ExternalRef     string      // tx hash / preimage / pi_... - the bill-once anchor
	ReceiptJSON     string      // opaque rail receipt
	RespHeaders     http.Header // merged into the final response (e.g. Payment-Response)
}

// Rail is one payment rail serving challenge-shaped sites (settle before
// serve). A service composes an ordered set of these; the Composer walks them
// and each rail self-selects the payloads it owns.
type Rail interface {
	// Names lists the rail names this instance settles as, for accepted-rails
	// listings and Settlement.Rail.
	Names() []string
	// ChallengeJSON renders the rail's in-band challenge object, used in the
	// shared 402 body and MCP payment-required payloads.
	ChallengeJSON(ctx context.Context, spec ChallengeSpec) (json.RawMessage, error)
	// Write402 adds the rail's challenge header family to a pending 402
	// response. It must not write the status or body; the Composer does that
	// once, after every rail has contributed.
	Write402(ctx context.Context, h http.Header, spec ChallengeSpec) error
	// TrySettle inspects the request headers for this rail's credential:
	// (nil, nil) when absent (not this rail's payload), a Settlement when
	// verified and settled, an error when a credential was present but invalid.
	TrySettle(ctx context.Context, h http.Header, spec ChallengeSpec) (*Settlement, error)
}

// PerCallPayment is a rail's opaque verified-but-unsettled per-call payment,
// held between verify and settle.
type PerCallPayment any

// PerCallRail settles per-call payments where the work must run between verify
// and settle: accepts entries for the payment-required body, a verify before
// the call, and a settle after it delivers, so a failed settle withholds the
// result. Not exercised by the scaffold's fake rail; declared here to fix the
// boundary for future MCP per-call rails.
type PerCallRail interface {
	MCPAccepts(ctx context.Context, resource string, amount USDMicros) (accepts []json.RawMessage, extensions map[string]json.RawMessage, err error)
	MCPVerify(ctx context.Context, resource string, raw json.RawMessage, amount USDMicros) (PerCallPayment, error)
	MCPSettle(ctx context.Context, p PerCallPayment) (*Settlement, map[string]any, error)
}

// Prices is the minimal pricing lookup the Composer needs: the USD-micros price
// for a resource. Implemented by prices.Table.
type Prices interface {
	PriceFor(resource string) (USDMicros, bool)
}
