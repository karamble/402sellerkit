package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sampleDescriptor() Descriptor {
	return Descriptor{
		Resource:    "https://svc.example.com/hello",
		Accepts:     []json.RawMessage{json.RawMessage(`{"scheme":"exact","network":"eip155:8453"}`)},
		ServiceName: "Example",
		Description: "a paid hello",
		MimeType:    "application/json",
		Tags:        []string{"demo"},
	}
}

func TestSubmitWireShape(t *testing.T) {
	var gotPath, gotKey, gotCT string
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-API-Key")
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := Submit(context.Background(), srv.Client(), Target{URL: srv.URL, APIKey: "k1"}, sampleDescriptor())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if gotPath != "/discovery/submit" {
		t.Errorf("path = %q, want /discovery/submit", gotPath)
	}
	if gotKey != "k1" {
		t.Errorf("X-API-Key = %q, want k1", gotKey)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if string(gotBody["resource"]) != `"https://svc.example.com/hello"` {
		t.Errorf("resource = %s", gotBody["resource"])
	}
	if string(gotBody["type"]) != `"http"` {
		t.Errorf("type = %s, want defaulted \"http\"", gotBody["type"])
	}
	var accepts []json.RawMessage
	if err := json.Unmarshal(gotBody["accepts"], &accepts); err != nil || len(accepts) != 1 {
		t.Errorf("accepts = %s (err %v)", gotBody["accepts"], err)
	}
	var meta map[string]any
	if err := json.Unmarshal(gotBody["metadata"], &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta["serviceName"] != "Example" {
		t.Errorf("metadata.serviceName = %v, want Example", meta["serviceName"])
	}
}

func TestSubmitErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer srv.Close()
	if err := Submit(context.Background(), srv.Client(), Target{URL: srv.URL}, sampleDescriptor()); err == nil {
		t.Error("expected error on non-2xx status")
	}
}

func TestSubmitValidation(t *testing.T) {
	ctx := context.Background()
	if err := Submit(ctx, nil, Target{URL: "https://x"}, Descriptor{Accepts: sampleDescriptor().Accepts}); err == nil {
		t.Error("expected error on empty Resource")
	}
	if err := Submit(ctx, nil, Target{URL: "https://x"}, Descriptor{Resource: "https://x"}); err == nil {
		t.Error("expected error on missing accepts")
	}
	if err := Submit(ctx, nil, Target{}, sampleDescriptor()); err == nil {
		t.Error("expected error on empty target URL")
	}
}

func TestAcceptsFromChallenge(t *testing.T) {
	challenge := []byte(`{"x402Version":2,"resource":{"url":"/x"},"accepts":[{"scheme":"exact"},{"scheme":"lightning"}]}`)
	got, err := AcceptsFromChallenge(challenge)
	if err != nil {
		t.Fatalf("AcceptsFromChallenge: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("accepts = %d, want 2", len(got))
	}
	if _, err := AcceptsFromChallenge([]byte(`{"accepts":[]}`)); err == nil {
		t.Error("expected error on empty accepts")
	}
}

func TestMultiplexerBestEffort(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	m := NewMultiplexer(nil, Target{URL: ok.URL}, Target{URL: bad.URL})
	if n := m.RegisterAll(context.Background(), sampleDescriptor()); n != 1 {
		t.Errorf("RegisterAll succeeded on %d targets, want 1 (one up, one down)", n)
	}
}

// TestSubmitWireShapeExtensions checks the extensions bag rides the submit
// body verbatim, and ExtensionsFromChallenge lifts it from a live challenge.
func TestSubmitWireShapeExtensions(t *testing.T) {
	challenge := []byte(`{
		"x402Version": 2,
		"accepts": [{"scheme": "exact"}],
		"extensions": {
			"bazaar": {"info": {"input": {"type": "mcp", "toolName": "process"}}, "schema": {"type": "object"}},
			"l402": {"info": {"tokenTtlSeconds": 60}}
		}
	}`)
	exts, err := ExtensionsFromChallenge(challenge, "bazaar")
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 {
		t.Fatalf("lifted %d extensions, want the bazaar one only", len(exts))
	}
	if _, ok := exts["bazaar"]; !ok {
		t.Fatalf("bazaar extension missing: %v", exts)
	}
	if absent, err := ExtensionsFromChallenge(challenge, "nope"); err != nil || absent != nil {
		t.Fatalf("absent key: %v %v", absent, err)
	}

	var got submitBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"indexed"}`))
	}))
	defer srv.Close()

	accepts, err := AcceptsFromChallenge(challenge)
	if err != nil {
		t.Fatal(err)
	}
	d := Descriptor{
		Resource:   "https://api.example.com/mcp",
		Type:       "mcp",
		Accepts:    accepts,
		Extensions: exts,
	}
	if err := Submit(context.Background(), nil, Target{URL: srv.URL}, d); err != nil {
		t.Fatal(err)
	}
	if len(got.Extensions) != 1 {
		t.Fatalf("posted extensions: %v", got.Extensions)
	}
	var info struct {
		Info struct {
			Input struct {
				ToolName string `json:"toolName"`
			} `json:"input"`
		} `json:"info"`
	}
	if err := json.Unmarshal(got.Extensions["bazaar"], &info); err != nil ||
		info.Info.Input.ToolName != "process" {
		t.Fatalf("extension did not survive the wire verbatim: %s", got.Extensions["bazaar"])
	}
}
