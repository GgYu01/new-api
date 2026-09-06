package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

func AlphaSearchHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	switch info.ChannelType {
	case constant.ChannelTypeSub2API,
		constant.ChannelTypeNewAPI,
		constant.ChannelTypeCodex,
		constant.ChannelTypeAdvancedCustom:
	default:
		// Allow retry onto another channel that may support this endpoint.
		return types.NewError(
			errors.New("channel does not support /v1/alpha/search"),
			types.ErrorCodeInvalidRequest,
		)
	}

	request, ok := info.Request.(*dto.AlphaSearchRequest)
	if !ok {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("invalid request type, expected *dto.AlphaSearchRequest, got %T", info.Request),
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	err := helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	jsonData, err := buildAlphaSearchRequestBody(request.RawBody, info.OriginModelName, info.UpstreamModelName)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			return newAPIErrorFromParamOverride(err)
		}
	}

	logger.LogDebug(c, "requestBody: %s", jsonData)
	body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
	if err != nil {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	defer closer.Close()

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	resp, err := adaptor.DoRequest(c, info, body)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")
	httpResp, ok := resp.(*http.Response)
	if !ok || httpResp == nil {
		return types.NewOpenAIError(errors.New("invalid http response"), types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	const maxSearchRespBytes = 32 << 20 // 32 MiB
	limited := io.LimitReader(httpResp.Body, int64(maxSearchRespBytes)+1)
	respBytes, err := io.ReadAll(limited)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	if len(respBytes) > maxSearchRespBytes {
		return types.NewErrorWithStatusCode(
			errors.New("alpha search response exceeded size limit (32MB)"),
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}

	outputText, valErr := validateAlphaSearchResponseBody(respBytes)
	if valErr != nil {
		return valErr
	}

	trimmed := bytes.TrimSpace(respBytes)
	// Billing: check if upstream provided explicit token usage
	usage := &dto.Usage{}
	if usageNode := gjson.GetBytes(trimmed, "usage"); usageNode.IsObject() {
		_ = common.Unmarshal([]byte(usageNode.Raw), usage)
	}
	if usage.TotalTokens == 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		// Upstream alpha search returned no token usage; bill one web_search_preview call plus prompt tokens estimate
		if info.ResponsesUsageInfo == nil {
			info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{
				BuiltInTools: make(map[string]*relaycommon.BuildInToolInfo),
			}
		}
		if info.ResponsesUsageInfo.BuiltInTools == nil {
			info.ResponsesUsageInfo.BuiltInTools = make(map[string]*relaycommon.BuildInToolInfo)
		}
		info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview] = &relaycommon.BuildInToolInfo{
			ToolName:  dto.BuildInToolWebSearchPreview,
			CallCount: 1,
		}
		usage = service.ResponseText2Usage(c, outputText, info.UpstreamModelName, info.GetEstimatePromptTokens())
	}

	// Quota is settled authoritatively regardless of client-side write errors
	service.PostTextConsumeQuota(c, info, usage, nil)

	if contentType := httpResp.Header.Get("Content-Type"); contentType != "" {
		c.Writer.Header().Set("Content-Type", contentType)
	} else {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Writer.WriteHeader(httpResp.StatusCode)
	if _, writeErr := c.Writer.Write(respBytes); writeErr != nil {
		logger.LogWarn(c, fmt.Sprintf("alpha search client write error: %v", writeErr))
	}
	return nil
}

func validateAlphaSearchResponseBody(respBytes []byte) (string, *types.NewAPIError) {
	trimmed := bytes.TrimSpace(respBytes)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return "", types.NewErrorWithStatusCode(
			errors.New("alpha search returned invalid non-JSON payload"),
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if errNode := gjson.GetBytes(trimmed, "error"); errNode.Exists() {
		errMsg := "unknown upstream error"
		if msg := errNode.Get("message").String(); msg != "" {
			errMsg = msg
		}
		return "", types.NewErrorWithStatusCode(
			fmt.Errorf("alpha search upstream error: %s", errMsg),
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}
	outputNode := gjson.GetBytes(trimmed, "output")
	if !outputNode.Exists() || outputNode.Type != gjson.String || strings.TrimSpace(outputNode.String()) == "" {
		return "", types.NewErrorWithStatusCode(
			errors.New("alpha search response missing required non-empty 'output' string"),
			types.ErrorCodeBadResponseBody,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}
	return outputNode.String(), nil
}

// buildAlphaSearchRequestBody returns RawBody unchanged unless the model was
// mapped, in which case only the "model" field is rewritten using sjson so large
// integers, formatting, and unknown fields are preserved without lossy round-tripping.
func buildAlphaSearchRequestBody(rawBody []byte, originModel, upstreamModel string) ([]byte, error) {
	if len(rawBody) == 0 {
		return nil, errors.New("empty alpha search request body")
	}
	if upstreamModel == "" || upstreamModel == originModel {
		return rawBody, nil
	}
	return sjson.SetBytes(rawBody, "model", upstreamModel)
}
