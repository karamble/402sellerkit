package challenge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func b64Body(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestMergeInto(t *testing.T) {
	existingB64 := b64Body(t, map[string]any{
		"x402Version": 2,
		"resource":    map[string]any{"url": "/hello"},
		"accepts":     []any{map[string]any{"scheme": "lightning", "network": "bip122:x"}},
	})
	incoming, _ := json.Marshal(map[string]any{
		"x402Version": 2,
		"accepts":     []any{map[string]any{"scheme": "exact", "network": "eip155:8453"}},
		"extensions":  map[string]any{"foo": "bar"},
	})

	mergedB64, err := MergeInto(existingB64, incoming)
	if err != nil {
		t.Fatalf("MergeInto: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(mergedB64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var merged struct {
		Resource   json.RawMessage            `json:"resource"`
		Accepts    []json.RawMessage          `json:"accepts"`
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &merged); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(merged.Accepts) != 2 {
		t.Errorf("accepts = %d, want 2 (existing frame keeps its entry, incoming appended)", len(merged.Accepts))
	}
	if len(merged.Resource) == 0 {
		t.Error("existing envelope frame (resource) should be preserved")
	}
	if _, ok := merged.Extensions["foo"]; !ok {
		t.Error("incoming extension key foo should be present")
	}
}

func TestSetOrMerge(t *testing.T) {
	const name = "Payment-Required"
	h := http.Header{}
	first, _ := json.Marshal(map[string]any{
		"x402Version": 2,
		"accepts":     []any{map[string]any{"scheme": "lightning"}},
	})
	second, _ := json.Marshal(map[string]any{
		"x402Version": 2,
		"accepts":     []any{map[string]any{"scheme": "exact"}},
	})

	// First rail sets the header.
	if err := SetOrMerge(h, name, first); err != nil {
		t.Fatalf("first SetOrMerge: %v", err)
	}
	// Second rail merges into it.
	if err := SetOrMerge(h, name, second); err != nil {
		t.Fatalf("second SetOrMerge: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(h.Get(name))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got struct {
		Accepts []json.RawMessage `json:"accepts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Accepts) != 2 {
		t.Errorf("accepts = %d, want 2 after set + merge", len(got.Accepts))
	}
}
