package imagebridge_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectAndRewriteJSONStorage_MemoryStorage(t *testing.T) {
	inputJSON := `{"model":"gpt-4o","tools":[{"type":"image_generation"}],"tool_choice":"image_generation","messages":[{"role":"user","content":"draw a mountain"}]}`
	storage, err := common.CreateBodyStorage([]byte(inputJSON))
	require.NoError(t, err)
	defer storage.Close()

	assert.False(t, storage.IsDisk())

	rewrittenStorage, changed, err := imagebridge.DetectAndRewriteJSONStorage(storage, imagebridge.EnvelopeChat)
	require.NoError(t, err)
	require.True(t, changed)
	defer rewrittenStorage.Close()

	rewrittenBytes, err := rewrittenStorage.Bytes()
	require.NoError(t, err)
	rewrittenStr := string(rewrittenBytes)

	assert.Contains(t, rewrittenStr, imagebridge.PlannerImageToolName)
	assert.NotContains(t, rewrittenStr, `"type":"image_generation"`)
	assert.Contains(t, rewrittenStr, `"tool_choice":"__newapi_generate_gpt_image"`)
	assert.Contains(t, rewrittenStr, "draw a mountain")
}

func TestDetectAndRewriteJSONStorage_DiskStorage(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "disk-storage-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	oldConfig := common.GetDiskCacheConfig()
	defer common.SetDiskCacheConfig(oldConfig)

	// Force disk caching with threshold 0
	common.SetDiskCacheConfig(common.DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: 0,
		MaxSizeMB:   100,
		Path:        tempDir,
	})

	// Create payload with a large padding message to simulate realistic requests
	largePadding := strings.Repeat("A", 128*1024)
	payload := map[string]any{
		"model": "gpt-5.6-sol",
		"input": "caption this",
		"tools": []any{
			map[string]any{"type": "image_generation"},
			map[string]any{"type": "function", "function": map[string]any{"name": "weather"}},
		},
		"tool_choice": "image_generation",
		"padding":     largePadding,
	}
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	storage, err := common.CreateBodyStorage(payloadBytes)
	require.NoError(t, err)
	require.True(t, storage.IsDisk(), "expected storage to be on disk when threshold is 0")

	rewrittenStorage, changed, err := imagebridge.DetectAndRewriteJSONStorage(storage, imagebridge.EnvelopeResponses)
	require.NoError(t, err)
	require.True(t, changed)
	defer rewrittenStorage.Close()

	require.True(t, rewrittenStorage.IsDisk(), "expected rewritten storage to remain on disk")

	rewrittenBytes, err := rewrittenStorage.Bytes()
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(rewrittenBytes, &parsed)
	require.NoError(t, err)

	// Tool choice should be rewritten
	assert.Equal(t, imagebridge.PlannerImageToolName, parsed["tool_choice"])

	// Tools should contain PlannerImageToolName and weather, but not native image_generation
	tools, ok := parsed["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 2)

	foundPlanner := false
	foundWeather := false
	for _, rawTool := range tools {
		toolMap := rawTool.(map[string]any)
		if toolMap["name"] == imagebridge.PlannerImageToolName {
			foundPlanner = true
			assert.Equal(t, "function", toolMap["type"])
			params, hasParams := toolMap["parameters"].(map[string]any)
			assert.True(t, hasParams)
			assert.Contains(t, params, "properties")
		}
		if toolMap["type"] == "function" {
			if fn, ok := toolMap["function"].(map[string]any); ok && fn["name"] == "weather" {
				foundWeather = true
			}
		}
	}
	assert.True(t, foundPlanner, "planner tool must be present")
	assert.True(t, foundWeather, "non-image tool must be preserved")

	// Verify large padding was not corrupted
	assert.Equal(t, largePadding, parsed["padding"])
}

func TestDetectAndRewriteJSONStorage_ToolChoiceNoneIgnored(t *testing.T) {
	inputJSON := `{"model":"gpt-4o","tools":[{"type":"image_generation"}],"tool_choice":"none","messages":[{"role":"user","content":"draw a mountain"}]}`
	storage, err := common.CreateBodyStorage([]byte(inputJSON))
	require.NoError(t, err)
	defer storage.Close()

	rewrittenStorage, changed, err := imagebridge.DetectAndRewriteJSONStorage(storage, imagebridge.EnvelopeChat)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, storage, rewrittenStorage)
}

func TestDetectAndRewriteJSONStorage_NoImageToolsUnchanged(t *testing.T) {
	inputJSON := `{"model":"gpt-4o","tools":[{"type":"function","function":{"name":"search"}}],"messages":[{"role":"user","content":"hello"}]}`
	storage, err := common.CreateBodyStorage([]byte(inputJSON))
	require.NoError(t, err)
	defer storage.Close()

	rewrittenStorage, changed, err := imagebridge.DetectAndRewriteJSONStorage(storage, imagebridge.EnvelopeChat)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, storage, rewrittenStorage)
}

func TestDetectAndRewriteJSONStorage_DifferentialMemoryVsDisk(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "disk-storage-diff-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	payload := `{"model":"gpt-4o","tools":[{"type":"image_generation"}],"tool_choice":{"type":"function","function":{"name":"image_generation"}},"messages":[{"role":"user","content":"generate image"}]}`

	// 1. Run in-memory
	memStorage, err := common.CreateBodyStorage([]byte(payload))
	require.NoError(t, err)
	rewrittenMem, memChanged, err := imagebridge.DetectAndRewriteJSONStorage(memStorage, imagebridge.EnvelopeChat)
	require.NoError(t, err)
	require.True(t, memChanged)
	defer rewrittenMem.Close()
	memBytes, err := rewrittenMem.Bytes()
	require.NoError(t, err)

	// 2. Run on disk
	oldConfig := common.GetDiskCacheConfig()
	defer common.SetDiskCacheConfig(oldConfig)
	common.SetDiskCacheConfig(common.DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: 0,
		MaxSizeMB:   100,
		Path:        tempDir,
	})

	diskStorage, err := common.CreateBodyStorage([]byte(payload))
	require.NoError(t, err)
	require.True(t, diskStorage.IsDisk())
	rewrittenDisk, diskChanged, err := imagebridge.DetectAndRewriteJSONStorage(diskStorage, imagebridge.EnvelopeChat)
	require.NoError(t, err)
	require.True(t, diskChanged)
	defer rewrittenDisk.Close()
	diskBytes, err := rewrittenDisk.Bytes()
	require.NoError(t, err)

	// Verify semantic equality
	var memParsed, diskParsed map[string]any
	require.NoError(t, json.Unmarshal(memBytes, &memParsed))
	require.NoError(t, json.Unmarshal(diskBytes, &diskParsed))
	assert.Equal(t, memParsed, diskParsed)
}
