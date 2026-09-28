package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
)

func TestDelistedModelsGovernance(t *testing.T) {
	// GPT-5.4 / 5.5 系列必须被判定为 delisted
	delisted := []string{
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.4-nano",
		"gpt-5.4-pro",
		"gpt-5.5",
		"gpt-5.5-pro",
		"openai/gpt-5.4",
		"openai/gpt-5.5",
	}
	for _, m := range delisted {
		assert.True(t, service.IsDelistedModel(m), "Model %s must be delisted", m)
	}

	// 允许保留的公开文本模型绝不能被误判为 delisted
	allowed := []string{
		"codexautoreview",
		"codex-auto-review",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-6",
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-6-terra",
		"gpt-5.3-codex-spark",
	}
	for _, m := range allowed {
		assert.False(t, service.IsDelistedModel(m), "Allowed model %s must not be delisted", m)
	}
}
