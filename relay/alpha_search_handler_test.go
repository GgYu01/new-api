package relay

import (
	"bytes"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildAlphaSearchRequestBodyPreservesUnknownFields(t *testing.T) {
	raw := []byte(`{
		"id":"req_1",
		"model":"gpt-5.1",
		"input":[{"role":"user","content":"hi"}],
		"commands":{"search_query":[{"q":"weather","recency":1}]},
		"settings":{"locale":"en"},
		"future_field":{"nested":true}
	}`)

	out, err := buildAlphaSearchRequestBody(raw, "gpt-5.1", "gpt-5.1-mapped")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, common.Unmarshal(out, &body))
	assert.Equal(t, "gpt-5.1-mapped", body["model"])
	assert.Equal(t, "req_1", body["id"])
	require.Contains(t, body, "commands")
	require.Contains(t, body, "settings")
	require.Contains(t, body, "future_field")
	require.Contains(t, body, "input")

	commands, ok := body["commands"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, commands, "search_query")

	future, ok := body["future_field"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, future["nested"])
}

func TestBuildAlphaSearchRequestBodyNoMappingKeepsRawBytes(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.1","commands":{"search_query":[{"q":"x"}]},"future_field":1}`)
	out, err := buildAlphaSearchRequestBody(raw, "gpt-5.1", "gpt-5.1")
	require.NoError(t, err)
	assert.Equal(t, raw, out)
}

func TestValidateAlphaSearchResponseBody(t *testing.T) {
	// Rejects HTML
	_, err := validateAlphaSearchResponseBody([]byte(`<html><body>502 Bad Gateway</body></html>`))
	require.NotNil(t, err)

	// Rejects JSON array
	_, err = validateAlphaSearchResponseBody([]byte(`["not", "an", "object"]`))
	require.NotNil(t, err)

	// Rejects invalid JSON
	_, err = validateAlphaSearchResponseBody([]byte(`{"output": "truncated`))
	require.NotNil(t, err)

	// Rejects top-level error
	_, err = validateAlphaSearchResponseBody([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	require.NotNil(t, err)

	// Rejects missing output
	_, err = validateAlphaSearchResponseBody([]byte(`{"results":[{"url":"https://example.com"}]}`))
	require.NotNil(t, err)

	// Rejects empty output
	_, err = validateAlphaSearchResponseBody([]byte(`{"output":"   "}`))
	require.NotNil(t, err)

	// Accepts valid output with optional fields (results, encrypted_output, unknown variants)
	validJSON := []byte(`{
		"output": "Paris is the capital of France.",
		"results": [{"url": "https://en.wikipedia.org/wiki/Paris"}],
		"encrypted_output": "enc_xyz",
		"custom_future_variant": {"flag": 1}
	}`)
	outputText, err := validateAlphaSearchResponseBody(validJSON)
	require.Nil(t, err)
	assert.Equal(t, "Paris is the capital of France.", outputText)
}

func TestAlphaSearchResponseModelAndSearchCountExtraction(t *testing.T) {
	// Test Tier 3 conversation fallback: search_call_count = 0
	tier3JSON := []byte(`{
		"output": "[Note: Real-time search is currently unavailable.]\n\nHello",
		"model": "gpt-5-fallback",
		"search_call_count": 0,
		"usage": {
			"prompt_tokens": 15,
			"completion_tokens": 25,
			"total_tokens": 40
		}
	}`)
	outputText, valErr := validateAlphaSearchResponseBody(tier3JSON)
	require.Nil(t, valErr)
	assert.Contains(t, outputText, "Hello")

	trimmed := bytes.TrimSpace(tier3JSON)
	assert.Equal(t, "gpt-5-fallback", gjson.GetBytes(trimmed, "model").String())
	assert.Equal(t, int64(0), gjson.GetBytes(trimmed, "search_call_count").Int())
	assert.Equal(t, int64(40), gjson.GetBytes(trimmed, "usage.total_tokens").Int())

	// Test Tier 2 web search fallback: search_call_count = 1
	tier2JSON := []byte(`{
		"output": "Search result for query",
		"model": "gpt-5",
		"search_call_count": 1
	}`)
	outputText2, valErr2 := validateAlphaSearchResponseBody(tier2JSON)
	require.Nil(t, valErr2)
	assert.Equal(t, "Search result for query", outputText2)
	assert.Equal(t, int64(1), gjson.GetBytes(tier2JSON, "search_call_count").Int())
}

