package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexAutoReviewDefaultVendorIsOpenAI(t *testing.T) {
	require.Equal(t, "OpenAI", defaultVendorRules["codex"])
	require.Equal(t, "OpenAI", defaultVendorRules["chatgpt"])
	require.Equal(t, "OpenAI", defaultVendorRules["openai"])
}
