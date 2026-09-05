package imagebridge

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRewriteAutoImageToolUsesPrivatePlannerFunction(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)
	rewritten, changed, err := RewriteAutoImageTool(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(rewritten), PlannerImageToolName)
	require.NotContains(t, string(rewritten), `"type":"image_generation"`)
}

func TestRewriteAutoImageToolLeavesForcedChoiceUntouched(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`)
	rewritten, changed, err := RewriteAutoImageTool(body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(rewritten))
}

func TestRewriteForResponsesUsesFlatFunctionToolEncoding(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)
	rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeResponses)
	require.NoError(t, err)
	require.True(t, changed)
	// Responses function tools are flat; the Chat envelope must not leak in.
	require.Contains(t, string(rewritten), `"name":"__newapi_generate_gpt_image"`)
	require.NotContains(t, string(rewritten), `"function":`)
	require.NotContains(t, string(rewritten), `"type":"image_generation"`)
}

func TestRewriteForChatUsesNestedFunctionEncoding(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}]}`)
	rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(rewritten), `"function":`)
	require.Contains(t, string(rewritten), `"name":"__newapi_generate_gpt_image"`)
}

func TestRewriteSkipsWhenPlannerFunctionNameAlreadyDefined(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"__newapi_generate_gpt_image","parameters":{"type":"object"}}]}`)
	rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeResponses)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(rewritten))
}
