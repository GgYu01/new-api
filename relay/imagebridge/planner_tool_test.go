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
