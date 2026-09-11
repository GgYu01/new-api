package service

import "strings"

var delistedModels = map[string]struct{}{
	"gpt-5.4":               {},
	"gpt-5.4-mini":          {},
	"gpt-5.4-nano":          {},
	"gpt-5.4-pro":           {},
	"gpt-5.4-2026-03-05":    {},
	"gpt-5.4-pro-2026-03-05": {},
	"gpt-5.5":               {},
	"gpt-5.5-pro":           {},
	"openai/gpt-5.4":        {},
	"openai/gpt-5.4-mini":   {},
	"openai/gpt-5.4-nano":   {},
	"openai/gpt-5.4-pro":    {},
	"openai/gpt-5.5":        {},
	"openai/gpt-5.5-pro":    {},
}

// IsDelistedModel 返回模型是否已全系下架。
// 对下架模型直接返回稳定 model_not_found，不触发无频道 503 重试。
func IsDelistedModel(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	if _, ok := delistedModels[normalized]; ok {
		return true
	}
	if strings.HasPrefix(normalized, "gpt-5.4") || strings.HasPrefix(normalized, "gpt-5.5") {
		return true
	}
	if strings.HasPrefix(normalized, "openai/gpt-5.4") || strings.HasPrefix(normalized, "openai/gpt-5.5") {
		return true
	}
	return false
}
