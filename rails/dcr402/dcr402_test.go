package dcr402rail

import (
	"strings"
	"testing"
)

func TestAtomsForUSDMicros(t *testing.T) {
	tests := []struct {
		name      string
		usdMicros int64
		rate      float64
		want      int64
		wantErr   bool
	}{
		{"cent-fraction at $20/DCR", 1000, 20, 5000, false},   // $0.001 -> 5000 atoms
		{"one dollar at $20/DCR", 1_000_000, 20, 5_000_000, false},
		{"sub-dust rounds up to floor", 1, 20, dustFloorAtoms, false},
		{"rounds up in seller favor", 999, 20, 4995, false},   // ceil(999*100/20)=ceil(4995)=4995
		{"zero amount errors", 0, 20, 0, true},
		{"non-positive rate errors", 1000, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AtomsForUSDMicros(tt.usdMicros, tt.rate)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBoundResource(t *testing.T) {
	tests := []struct {
		resource  string
		usdMicros int64
		want      string
	}{
		{"/hello", 1000, "/hello?usd_micros=1000"},
		{"/img?scene=abc", 2000, "/img?scene=abc&usd_micros=2000"},
	}
	for _, tt := range tests {
		if got := boundResource(tt.resource, tt.usdMicros); got != tt.want {
			t.Errorf("boundResource(%q, %d) = %q, want %q", tt.resource, tt.usdMicros, got, tt.want)
		}
	}
}

func TestNetworkFor(t *testing.T) {
	for _, name := range []string{"mainnet", "testnet3", "simnet"} {
		n, err := networkFor(name)
		if err != nil {
			t.Fatalf("networkFor(%q): %v", name, err)
		}
		if !strings.HasPrefix(n.CAIP2, "bip122:") {
			t.Errorf("networkFor(%q).CAIP2 = %q, want a bip122: id", name, n.CAIP2)
		}
	}
	if _, err := networkFor("bogus"); err == nil {
		t.Error("networkFor(bogus): expected error")
	}
}
