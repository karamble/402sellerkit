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
	name      string
	mintErr   error
	verifyErr error
}

func (s *stubPerCallRail) MCPAccepts(_ context.Context, resource string, amount USDMicros) ([]json.RawMessage, map[string]json.RawMessage, error) {
	if s.mintErr != nil {
		return nil, nil, s.mintErr
	}
	accept := json.RawMessage(fmt.Sprintf(`{"rail":%q,"resource":%q,"amount":%d}`, s.name, resource, int64(amount)))
	ext := map[string]json.RawMessage{"shared": json.RawMessage(fmt.Sprintf(`{"from":%q}`, s.name))}
	return []json.RawMessage{accept}, ext, nil
}

func (s *stubPerCallRail) MCPVerify(_ context.Context, _ string, raw json.RawMessage, _ USDMicros) (PerCallPayment, error) {
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
