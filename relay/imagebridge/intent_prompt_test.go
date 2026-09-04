package imagebridge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectJSONPreservesPromptWhitespace(t *testing.T) {
	intent, matched, err := DetectJSON("/v1/images/generations", []byte(`{"prompt":"  keep  internal\nspacing  "}`))
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, "keep  internal\nspacing", intent.Request.Prompt)
}

func TestDetectJSONRejectsOversizedPromptInsteadOfTailTruncating(t *testing.T) {
	_, matched, err := DetectJSON("/v1/images/generations", []byte(`{"prompt":"`+strings.Repeat("x", maxPromptBytes+1)+`"}`))
	require.Error(t, err)
	require.False(t, matched)
	require.Contains(t, err.Error(), "exceeds")
}
