package challenge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

// SetOrMerge writes a rail's x402 v2 PaymentRequired JSON to the named header,
// base64-encoded. If an earlier rail already set the header, the two are merged
// (see MergeInto) so one header carries every rail's accepts entries. This is
// the dual-envelope composition, and it is order-independent across rails.
func SetOrMerge(h http.Header, name string, prJSON []byte) error {
	existing := h.Get(name)
	if existing == "" {
		h.Set(name, base64.StdEncoding.EncodeToString(prJSON))
		return nil
	}
	merged, err := MergeInto(existing, prJSON)
	if err != nil {
		return err
	}
	h.Set(name, merged)
	return nil
}

// MergeInto merges a rail's x402 v2 PaymentRequired JSON into an existing
// base64-encoded PaymentRequired header value at the JSON level: it appends the
// incoming accepts entries and adds any new extension keys (first-writer-wins),
// keeping the existing envelope frame (version, resource). It depends on no
// rail's Go types, only the shared x402 v2 shape.
func MergeInto(existingB64 string, prJSON []byte) (string, error) {
	existing, err := base64.StdEncoding.DecodeString(existingB64)
	if err != nil {
		return "", fmt.Errorf("challenge: existing header: %w", err)
	}
	var body, incoming map[string]json.RawMessage
	if err := json.Unmarshal(existing, &body); err != nil {
		return "", fmt.Errorf("challenge: existing body: %w", err)
	}
	if err := json.Unmarshal(prJSON, &incoming); err != nil {
		return "", fmt.Errorf("challenge: incoming body: %w", err)
	}

	var accepts, add []json.RawMessage
	if a, ok := body["accepts"]; ok {
		if err := json.Unmarshal(a, &accepts); err != nil {
			return "", fmt.Errorf("challenge: existing accepts: %w", err)
		}
	}
	if a, ok := incoming["accepts"]; ok {
		if err := json.Unmarshal(a, &add); err != nil {
			return "", fmt.Errorf("challenge: incoming accepts: %w", err)
		}
	}
	accepts = append(accepts, add...)
	if body["accepts"], err = json.Marshal(accepts); err != nil {
		return "", err
	}

	if e, ok := incoming["extensions"]; ok {
		exts := map[string]json.RawMessage{}
		if be, ok := body["extensions"]; ok {
			if err := json.Unmarshal(be, &exts); err != nil {
				return "", fmt.Errorf("challenge: existing extensions: %w", err)
			}
		}
		addExt := map[string]json.RawMessage{}
		if err := json.Unmarshal(e, &addExt); err != nil {
			return "", fmt.Errorf("challenge: incoming extensions: %w", err)
		}
		for k, v := range addExt {
			if _, taken := exts[k]; !taken {
				exts[k] = v
			}
		}
		if body["extensions"], err = json.Marshal(exts); err != nil {
			return "", err
		}
	}

	merged, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(merged), nil
}
