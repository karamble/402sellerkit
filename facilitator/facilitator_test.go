package facilitator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sampleRequest() Request {
	return Request{
		PaymentPayload:      json.RawMessage(`{"accepted":{"network":"eip155:8453"}}`),
		PaymentRequirements: json.RawMessage(`{"scheme":"exact"}`),
	}
}

func TestHTTPVerify(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-API-Key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"isValid":true,"payer":"0xabc"}`))
	}))
	defer srv.Close()

	c := &HTTP{URL: srv.URL, APIKey: "k1", Client: srv.Client()}
	res, err := c.Verify(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.IsValid || res.Payer != "0xabc" {
		t.Errorf("got %+v", res)
	}
	if gotPath != "/verify" {
		t.Errorf("path = %q, want /verify", gotPath)
	}
	if gotKey != "k1" {
		t.Errorf("X-API-Key = %q, want k1", gotKey)
	}
	if _, ok := gotBody["paymentPayload"]; !ok {
		t.Error("request body missing paymentPayload")
	}
	if _, ok := gotBody["paymentRequirements"]; !ok {
		t.Error("request body missing paymentRequirements")
	}
}

func TestHTTPSettle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/settle" {
			t.Errorf("path = %q, want /settle", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"transaction":"0x123","network":"eip155:8453"}`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "")
	c.Client = srv.Client()
	res, err := c.Settle(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !res.Success || res.Transaction != "0x123" {
		t.Errorf("got %+v", res)
	}
}

func TestHTTPSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/supported" {
			t.Errorf("got %s %s, want GET /supported", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"kinds":[{"x402Version":2,"scheme":"exact","network":"eip155:8453"}],"extensions":[],"signers":{}}`))
	}))
	defer srv.Close()

	c := &HTTP{URL: srv.URL, Client: srv.Client()}
	res, err := c.Supported(context.Background())
	if err != nil {
		t.Fatalf("Supported: %v", err)
	}
	if len(res.Kinds) != 1 || res.Kinds[0].Scheme != "exact" {
		t.Errorf("got %+v", res)
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad payment", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := &HTTP{URL: srv.URL, Client: srv.Client()}
	if _, err := c.Verify(context.Background(), sampleRequest()); err == nil {
		t.Error("expected error on non-2xx status")
	}
}

func TestInline(t *testing.T) {
	ctx := context.Background()
	called := false
	in := Inline{
		SettleFunc: func(ctx context.Context, req Request) (*SettleResponse, error) {
			called = true
			return &SettleResponse{Success: true, Transaction: "local"}, nil
		},
	}
	res, err := in.Settle(ctx, sampleRequest())
	if err != nil || !called || res.Transaction != "local" {
		t.Fatalf("inline settle: res=%+v err=%v called=%v", res, err, called)
	}
	// An unconfigured operation errors rather than panicking.
	if _, err := in.Verify(ctx, sampleRequest()); err == nil {
		t.Error("expected error from unconfigured inline Verify")
	}
}
