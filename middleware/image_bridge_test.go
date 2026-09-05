package middleware

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectImageBridgeRewritesAutoImageToolForGPTPlanner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 1)
	common.SetContextKey(context, constant.ContextKeyTokenSubscriptionType, "gptopenaicodex")
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"caption this","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`))
	context.Request.Header.Set("Content-Type", "application/json")

	DetectImageBridge()(context)
	rewritten, err := io.ReadAll(context.Request.Body)
	require.NoError(t, err)
	require.Contains(t, string(rewritten), imagebridge.PlannerImageToolName)
	require.NotContains(t, string(rewritten), `"type":"image_generation"`)
}

func TestDetectImageBridgeOverridesOnlyChannelSelectionModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"input":"draw a fox","tools":[{"type":"image_generation"}],"tool_choice":"image_generation","stream":true
	}`))
	context.Request.Header.Set("Content-Type", "application/json")

	DetectImageBridge()(context)
	intent, ok := imagebridge.FromContext(context)

	require.True(t, ok)
	assert.Equal(t, imagebridge.DefaultModel, intent.Request.Model)
	modelRequest, shouldSelect, err := getModelRequest(context)
	require.NoError(t, err)
	assert.True(t, shouldSelect)
	assert.Equal(t, imagebridge.DefaultModel, modelRequest.Model)
}

func TestDetectImageBridgePreservesForeignImageModelForChannelSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{
		"model":"grok-imagine-image","prompt":"draw a fox"
	}`))
	context.Request.Header.Set("Content-Type", "application/json")

	DetectImageBridge()(context)
	intent, ok := imagebridge.FromContext(context)

	require.True(t, ok)
	assert.Equal(t, "grok-imagine-image", intent.ClientModel)
	modelRequest, shouldSelect, err := getModelRequest(context)
	require.NoError(t, err)
	assert.True(t, shouldSelect)
	assert.Equal(t, "grok-imagine-image", modelRequest.Model)
}

func TestDetectImageBridgeLeavesOrdinaryResponsesRequestUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.6-sol","input":"draw a conclusion"
	}`))
	context.Request.Header.Set("Content-Type", "application/json")

	DetectImageBridge()(context)
	_, ok := imagebridge.FromContext(context)

	assert.False(t, ok)
	modelRequest, shouldSelect, err := getModelRequest(context)
	require.NoError(t, err)
	assert.True(t, shouldSelect)
	assert.Equal(t, "gpt-5.6-sol", modelRequest.Model)
}

func TestDetectImageBridgeAcceptsMultipartEditWithoutModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "make it blue"))
	part, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("fake-png"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	context.Request.Header.Set("Content-Type", writer.FormDataContentType())

	DetectImageBridge()(context)
	intent, ok := imagebridge.FromContext(context)

	require.True(t, ok)
	assert.Equal(t, imagebridge.ModeEdit, intent.Mode)
	assert.Equal(t, imagebridge.DefaultModel, intent.Request.Model)
	assert.Equal(t, 1, intent.ImageCount)
}

func TestDetectImageBridgeUsesAcceptSSEWhenStreamIsOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"input":"draw a fox","tools":[{"type":"image_generation"}],"tool_choice":"image_generation"
	}`))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("Accept", "text/event-stream")

	DetectImageBridge()(context)
	intent, ok := imagebridge.FromContext(context)

	require.True(t, ok)
	assert.True(t, intent.Stream)
}

func TestDetectImageBridgeExplicitStreamFalseOverridesAcceptSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"input":"draw a fox","tools":[{"type":"image_generation"}],"tool_choice":"image_generation","stream":false
	}`))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("Accept", "text/event-stream")

	DetectImageBridge()(context)
	intent, ok := imagebridge.FromContext(context)

	require.True(t, ok)
	assert.False(t, intent.Stream)
}

func TestDetectImageBridgeRewriteTransfersBodyOwnershipToContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 1)
	common.SetContextKey(context, constant.ContextKeyTokenSubscriptionType, "gptopenaicodex")
	body := `{"model":"gpt-5.6-sol","input":"caption this","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")

	DetectImageBridge()(context)

	// The context storage must be the NEW storage, still open and replayable;
	// reading it again must not fail with "body storage is closed".
	seeker, err := common.GetBodyStorage(context)
	require.NoError(t, err)
	_, err = seeker.Seek(0, io.SeekStart)
	require.NoError(t, err)
	replayed, err := io.ReadAll(seeker)
	require.NoError(t, err)
	require.Contains(t, string(replayed), imagebridge.PlannerImageToolName)
	require.NotContains(t, string(replayed), `"type":"image_generation"`)
	// The rewritten body has a different length than the original; the request
	// content length must track the effective body.
	require.Equal(t, int64(len(replayed)), context.Request.ContentLength)
	// Responses requests must keep the flat function-tool encoding.
	require.NotContains(t, string(replayed), `"function":`)
}
