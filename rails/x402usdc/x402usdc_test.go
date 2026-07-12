package x402usdc

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

const evmAddr = "0x1111111111111111111111111111111111111111"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		wantURL string // checked only when !wantErr
	}{
		{
			name:    "sepolia defaults to keyless testnet facilitator",
			cfg:     Config{Network: NetworkBaseSepolia, PayTo: evmAddr},
			wantURL: keylessTestnetFacilitator,
		},
		{
			name:    "mainnet with self-hosted facilitator",
			cfg:     Config{Network: NetworkBaseMainnet, PayTo: evmAddr, FacilitatorURL: "https://fac.example.com"},
			wantURL: "https://fac.example.com",
		},
		{
			name:    "bad PayTo",
			cfg:     Config{Network: NetworkBaseSepolia, PayTo: "not-an-address"},
			wantErr: true,
		},
		{
			name:    "unsupported network",
			cfg:     Config{Network: "eip155:1", PayTo: evmAddr, FacilitatorURL: "https://fac.example.com"},
			wantErr: true,
		},
		{
			name:    "mainnet without facilitator is rejected (ADR-0001)",
			cfg:     Config{Network: NetworkBaseMainnet, PayTo: evmAddr},
			wantErr: true,
		},
		{
			name:    "CDP facilitator is rejected (ADR-0001)",
			cfg:     Config{Network: NetworkBaseMainnet, PayTo: evmAddr, FacilitatorURL: "https://api.cdp.coinbase.com/platform/v2/x402"},
			wantErr: true,
		},
		{
			name:    "mainnet with the keyless testnet facilitator is rejected",
			cfg:     Config{Network: NetworkBaseMainnet, PayTo: evmAddr, FacilitatorURL: keylessTestnetFacilitator},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cfg.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.FacilitatorURL != tt.wantURL {
				t.Errorf("FacilitatorURL = %q, want %q", got.FacilitatorURL, tt.wantURL)
			}
			if got.Service == "" {
				t.Error("Service should default to a non-empty value")
			}
		})
	}
}

func TestMergeChallengeHeader(t *testing.T) {
	existing := map[string]any{
		"x402Version": 2,
		"accepts":     []any{map[string]any{"scheme": "lightning", "network": "bip122:x"}},
	}
	existingRaw, _ := json.Marshal(existing)
	existingB64 := base64.StdEncoding.EncodeToString(existingRaw)

	incoming := map[string]any{
		"x402Version": 2,
		"accepts":     []any{map[string]any{"scheme": "exact", "network": "eip155:8453"}},
		"extensions":  map[string]any{"foo": "bar"},
	}
	incomingRaw, _ := json.Marshal(incoming)

	mergedB64, err := mergeChallengeHeader(existingB64, incomingRaw)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	mergedRaw, err := base64.StdEncoding.DecodeString(mergedB64)
	if err != nil {
		t.Fatalf("decode merged: %v", err)
	}
	var merged struct {
		Accepts    []json.RawMessage          `json:"accepts"`
		Extensions map[string]json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(mergedRaw, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if len(merged.Accepts) != 2 {
		t.Errorf("merged accepts = %d entries, want 2 (existing + incoming)", len(merged.Accepts))
	}
	if _, ok := merged.Extensions["foo"]; !ok {
		t.Error("merged extensions missing incoming key foo")
	}
}
