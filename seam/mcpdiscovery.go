package seam

import "encoding/json"

// extensionBazaar is the x402 extensions key for bazaar discovery.
const extensionBazaar = "bazaar"

const draft2020 = "https://json-schema.org/draft/2020-12/schema"

// discoveryExtension renders the bazaar discovery extension for the site:
// info identifying the tool (type, toolName, inputSchema, streamable-http
// transport) plus the Draft 2020-12 schema validating it. The shape mirrors
// dcr402 lib's BuildMCPDiscovery exactly (replicated here so the seam core
// needs no rail/SDK type; pinned by TestMCPSiteDiscoveryExtensionShape and
// the scheme's mcp-payment-required-server-url test vector).
func (s MCPSite) discoveryExtension() json.RawMessage {
	input := map[string]any{
		"type":      "mcp",
		"toolName":  s.Tool,
		"transport": "streamable-http",
	}
	if len(s.InputSchema) > 0 {
		input["inputSchema"] = s.InputSchema
	} else {
		input["inputSchema"] = map[string]any{"type": "object"}
	}
	inputProps := map[string]any{
		"type":        map[string]any{"type": "string", "const": "mcp"},
		"toolName":    map[string]any{"type": "string"},
		"inputSchema": map[string]any{"type": "object"},
		"transport":   map[string]any{"type": "string", "enum": []string{"streamable-http", "sse"}},
	}
	ext := map[string]any{
		"info": map[string]any{
			"input": input,
		},
		"schema": map[string]any{
			"$schema": draft2020,
			"type":    "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":                 "object",
					"properties":           inputProps,
					"required":             []string{"type", "toolName", "inputSchema"},
					"additionalProperties": false,
				},
			},
			"required": []string{"input"},
		},
	}
	raw, err := json.Marshal(ext)
	if err != nil {
		// The extension is built from literals and the caller's schema
		// bytes; a marshal failure means invalid InputSchema JSON, which
		// json.RawMessage re-marshaling surfaces here. Advertise nothing
		// rather than a broken extension.
		return json.RawMessage(`{"info":{},"schema":{}}`)
	}
	return raw
}
