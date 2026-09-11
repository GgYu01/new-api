package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetBuildInfoEnvVersionIsRuntimeLabelOnly(t *testing.T) {
	prevCompiled, prevOverride := CompiledVersion, RuntimeVersionOverride
	t.Cleanup(func() {
		CompiledVersion, RuntimeVersionOverride = prevCompiled, prevOverride
	})
	CompiledVersion = "v9.9.9-test"
	RuntimeVersionOverride = ""
	t.Setenv("VERSION", "v0.0.0-evil")

	info := GetBuildInfo()
	require.Equal(t, "v9.9.9-test", info.CompiledVersion)
	assert.Equal(t, "v0.0.0-evil", info.RuntimeOverride)
	assert.NotEmpty(t, info.GoVersion)
	assert.NotEmpty(t, info.Platform)
}
