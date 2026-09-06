package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
