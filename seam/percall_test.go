package seam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// stubPerCallRail claims payloads whose JSON carries its rail name and
// settles them with ref "stub:<n>".
type stubPerCallRail struct {
	name               string
	mintErr            error
	verifyErr          error
	lastVerifyResource string
}

func (s *stubPerCallRail) MCPAccepts(_ context.Context, resource string, amount USDMicros) ([]json.RawMessage, map[string]json.RawMessage, error) {
	if s.mintErr != nil {
		return nil, nil, s.mintErr
	}
	accept := json.RawMessage(fmt.Sprintf(`{"rail":%q,"resource":%q,"amount":%d}`, s.name, resource, int64(amount)))
	ext := map[string]json.RawMessage{"shared": json.RawMessage(fmt.Sprintf(`{"from":%q}`, s.name))}
	return []json.RawMessage{accept}, ext, nil
}

func (s *stubPerCallRail) MCPVerify(_ context.Context, resource string, raw json.RawMessage, _ USDMicros) (PerCallPayment, error) {
	s.lastVerifyResource = resource
	var probe struct {
		Rail string `json:"rail"`
		Ref  string `json:"ref"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Rail != s.name {
		return nil, nil // not this rail's payload
	}
	if s.verifyErr != nil {
		return nil, s.verifyErr
	}
	return probe.Ref, nil
}

func (s *stubPerCallRail) MCPSettle(_ context.Context, p PerCallPayment) (*Settlement, map[string]any, error) {
	ref := p.(string)
	return &Settlement{
		Rail:        s.name,
		ExternalRef: s.name + ":" + ref,
	}, map[string]any{"x402/payment-response": "receipt-" + ref}, nil
}

func payload(rail, ref string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"rail":%q,"ref":%q}`, rail, ref))
}

func TestPerCallChallengeMergesRails(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a"}, &stubPerCallRail{name: "b"})
	raw, err := pc.Challenge(context.Background(), "mcp://tool/x", 5000)
	if err != nil {
		t.Fatal(err)
	}
	var pr struct {
		X402Version int               `json:"x402Version"`
		Error       string            `json:"error"`
		Accepts     []json.RawMessage `json:"accepts"`
		Extensions  map[string]json.RawMessage
		Resource    struct {
			URL         string `json:"url"`
			ServiceName string `json:"serviceName"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.X402Version != 2 || pr.Error != "payment_required" {
		t.Fatalf("envelope = %d %q", pr.X402Version, pr.Error)
	}
	if len(pr.Accepts) != 2 {
		t.Fatalf("accepts = %d, want 2 (both rails)", len(pr.Accepts))
	}
	if pr.Resource.URL != "mcp://tool/x" || pr.Resource.ServiceName != "svc" {
		t.Fatalf("resource = %+v", pr.Resource)
	}
}

func TestPerCallChallengeFirstWriterWinsExtensions(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a"}, &stubPerCallRail{name: "b"})
	raw, err := pc.Challenge(context.Background(), "mcp://tool/x", 5000)
	if err != nil {
		t.Fatal(err)
	}
	var pr struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatal(err)
	}
	var from struct {
		From string `json:"from"`
	}
	if err := json.Unmarshal(pr.Extensions["shared"], &from); err != nil {
		t.Fatal(err)
	}
	if from.From != "a" {
		t.Fatalf("extension owner = %q, want first rail a", from.From)
	}
}

func TestPerCallChallengeSkipsFailingRail(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil,
		&stubPerCallRail{name: "a", mintErr: errors.New("node down")},
		&stubPerCallRail{name: "b"})
	raw, err := pc.Challenge(context.Background(), "mcp://tool/x", 5000)
	if err != nil {
		t.Fatal(err)
	}
	var pr struct {
		Accepts []json.RawMessage `json:"accepts"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatal(err)
	}
	if len(pr.Accepts) != 1 {
		t.Fatalf("accepts = %d, want 1 (the healthy rail)", len(pr.Accepts))
	}
}

func TestPerCallChallengeAllRailsFail(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a", mintErr: errors.New("down")})
	if _, err := pc.Challenge(context.Background(), "mcp://tool/x", 5000); err == nil {
		t.Fatal("want error when no rail mints")
	}
}

func TestPerCallVerifyDemuxes(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a"}, &stubPerCallRail{name: "b"})
	vc, err := pc.Verify(context.Background(), "mcp://tool/x", payload("b", "r1"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	s, meta, first, err := pc.Settle(context.Background(), vc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rail != "b" || s.ExternalRef != "b:r1" {
		t.Fatalf("settled on %q ref %q", s.Rail, s.ExternalRef)
	}
	if !first {
		t.Fatal("first settlement must report first=true")
	}
	if meta["x402/payment-response"] != "receipt-r1" {
		t.Fatalf("meta = %v", meta)
	}
}

func TestPerCallVerifyNoRail(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a"})
	if _, err := pc.Verify(context.Background(), "mcp://tool/x", payload("z", "r1"), 5000); !errors.Is(err, ErrNoRail) {
		t.Fatalf("err = %v, want ErrNoRail", err)
	}
}

func TestPerCallVerifyInvalidPayment(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a", verifyErr: errors.New("bad proof")})
	if _, err := pc.Verify(context.Background(), "mcp://tool/x", payload("a", "r1"), 5000); err == nil {
		t.Fatal("want the rail's verify error")
	}
}

func TestPerCallSettleReplayBilledOnce(t *testing.T) {
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, &stubPerCallRail{name: "a"})
	ctx := context.Background()
	vc1, err := pc.Verify(ctx, "mcp://tool/x", payload("a", "r1"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, first, _ := pc.Settle(ctx, vc1); !first {
		t.Fatal("first settle must be first")
	}
	vc2, err := pc.Verify(ctx, "mcp://tool/x", payload("a", "r1"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, first, _ := pc.Settle(ctx, vc2); first {
		t.Fatal("replayed settle must report first=false")
	}
}

// TestChallengeMCPWireAndBind checks the wire/bind split: rails mint and
// bind against the per-tool key while the wire body advertises the server
// endpoint URL and the bazaar discovery extension identifies the tool.
func TestChallengeMCPWireAndBind(t *testing.T) {
	rail := &stubPerCallRail{name: "a"}
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, rail)
	site := MCPSite{
		ServerURL:   "https://api.example.com/mcp",
		Tool:        "x",
		Description: "example tool",
	}
	raw, err := pc.ChallengeMCP(context.Background(), site, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Resource struct {
			URL         string `json:"url"`
			Description string `json:"description"`
			ServiceName string `json:"serviceName"`
		} `json:"resource"`
		Accepts []struct {
			Resource string `json:"resource"`
		} `json:"accepts"`
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Resource.URL != site.ServerURL || body.Resource.Description != "example tool" ||
		body.Resource.ServiceName != "svc" {
		t.Fatalf("wire resource block: %+v", body.Resource)
	}
	if len(body.Accepts) != 1 || body.Accepts[0].Resource != site.BindKey() {
		t.Fatalf("rail must receive the bind key, got %+v", body.Accepts)
	}
	var ext struct {
		Info struct {
			Input struct {
				Type      string `json:"type"`
				ToolName  string `json:"toolName"`
				Transport string `json:"transport"`
			} `json:"input"`
		} `json:"info"`
	}
	if err := json.Unmarshal(body.Extensions["bazaar"], &ext); err != nil {
		t.Fatalf("bazaar extension: %v (%s)", err, body.Extensions["bazaar"])
	}
	if ext.Info.Input.Type != "mcp" || ext.Info.Input.ToolName != "x" ||
		ext.Info.Input.Transport != "streamable-http" {
		t.Fatalf("bazaar input: %+v", ext.Info.Input)
	}
	if _, ok := body.Extensions["shared"]; !ok {
		t.Fatal("rail-contributed extension dropped")
	}

	// Fallback: no server URL advertises the binding key itself.
	raw, err = pc.ChallengeMCP(context.Background(), MCPSite{Tool: "x"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Resource.URL != "mcp://tool/x" {
		t.Fatalf("fallback wire url = %q", body.Resource.URL)
	}
}

// TestVerifyMCPDelegatesBindKey checks VerifyMCP verifies against the
// per-tool binding key, never the wire URL.
func TestVerifyMCPDelegatesBindKey(t *testing.T) {
	rail := &stubPerCallRail{name: "a"}
	pc := NewPerCall("svc", NewMemSeenRefs(), nil, rail)
	site := MCPSite{ServerURL: "https://api.example.com/mcp", Tool: "x"}
	vc, err := pc.VerifyMCP(context.Background(), site, payload("a", "r1"), 5000)
	if err != nil || vc == nil {
		t.Fatalf("VerifyMCP: %v %v", vc, err)
	}
	if rail.lastVerifyResource != site.BindKey() {
		t.Fatalf("rail verified against %q, want %q", rail.lastVerifyResource, site.BindKey())
	}
}

// TestMCPSiteDiscoveryExtensionShape pins the seam-built bazaar extension to
// the shape dcr402 lib's BuildMCPDiscovery emits (also pinned cross-repo by
// the scheme's mcp-payment-required-server-url test vector).
func TestMCPSiteDiscoveryExtensionShape(t *testing.T) {
	site := MCPSite{Tool: "process"}
	got := site.discoveryExtension()
	want := `{
	  "info": {"input": {"inputSchema": {"type": "object"}, "toolName": "process", "transport": "streamable-http", "type": "mcp"}},
	  "schema": {
	    "$schema": "https://json-schema.org/draft/2020-12/schema",
	    "properties": {"input": {"additionalProperties": false,
	      "properties": {"inputSchema": {"type": "object"}, "toolName": {"type": "string"},
	        "transport": {"enum": ["streamable-http", "sse"], "type": "string"}, "type": {"const": "mcp", "type": "string"}},
	      "required": ["type", "toolName", "inputSchema"], "type": "object"}},
	    "required": ["input"], "type": "object"}
	}`
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(g, w) {
		t.Fatalf("extension shape drifted from BuildMCPDiscovery:\n%s", got)
	}

	// A caller-supplied input schema rides through verbatim.
	site.InputSchema = json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	var withSchema struct {
		Info struct {
			Input struct {
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"input"`
		} `json:"info"`
	}
	if err := json.Unmarshal(site.discoveryExtension(), &withSchema); err != nil {
		t.Fatal(err)
	}
	if string(withSchema.Info.Input.InputSchema) != string(site.InputSchema) {
		t.Fatalf("inputSchema not carried verbatim: %s", withSchema.Info.Input.InputSchema)
	}
}

// jsonEqual compares two unmarshaled JSON values structurally.
func jsonEqual(a, b any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}
