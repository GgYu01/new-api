package service

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

// officialModelPricing mirrors service/testdata/official_model_pricing.json.
// The fixture is the single source of truth for the OpenAI model billing
// expressions applied to production billing_setting; this test pins the
// expressions to the official $/1M prices so configuration drift or engine
// regressions fail loudly before any candidate is deployed.
type officialModelPricing struct {
	OfficialSource      string `json:"official_source"`
	TierBoundaryTokens  int    `json:"tier_boundary_tokens"`
	Models              map[string]officialModelEntry `json:"models"`
	Vectors             map[string]officialVector      `json:"vectors"`
}

type officialModelEntry struct {
	LongContextSupported bool     `json:"long_context_supported"`
	Expr                 string   `json:"expr"`
	Notes                string   `json:"note"`
}

type officialVector struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	CachedTokens        int `json:"cached_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
}

var officialPricingModels = []string{
	"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark", "codex-auto-review",
}

func loadOfficialModelPricing(t *testing.T) officialModelPricing {
	t.Helper()
	raw, err := os.ReadFile("testdata/official_model_pricing.json")
	require.NoError(t, err)
	var pricing officialModelPricing
	require.NoError(t, json.Unmarshal(raw, &pricing))
	return pricing
}

func TestOfficialModelPricingCoversAllOpenAIModels(t *testing.T) {
	pricing := loadOfficialModelPricing(t)
	for _, model := range officialPricingModels {
		entry, ok := pricing.Models[model]
		require.True(t, ok, "missing official pricing for %s", model)
		require.NotEmpty(t, entry.Expr, "empty expr for %s", model)
	}
	require.Len(t, pricing.Models, len(officialPricingModels))
}

func TestOfficialModelPricingExpressionsAreValid(t *testing.T) {
	pricing := loadOfficialModelPricing(t)
	for model, entry := range pricing.Models {
		t.Run(model, func(t *testing.T) {
			_, _, err := billingexpr.RunExpr(entry.Expr, billingexpr.TokenParams{P: 1000, C: 1000, Len: 1000})
			require.NoError(t, err)
			_, _, err = billingexpr.RunExpr(entry.Expr, billingexpr.TokenParams{P: 300000, C: 1000, Len: 300000})
			require.NoError(t, err)
		})
	}
}

// TestOfficialModelPricingMath pins the exact dollar cost of every fixture
// vector, exercising BuildTieredTokenParams normalization (OpenAI usage
// semantics: prompt_tokens includes cache sub-categories) and tier selection
// against the 272K boundary.
func TestOfficialModelPricingMath(t *testing.T) {
	pricing := loadOfficialModelPricing(t)

	shortPrices := map[string][4]float64{
		// {input, cache_read, cache_write, output} $/1M, short tier
		"gpt-6-astra":          {10.0, 1.0, 12.5, 50.0},
		"gpt-5.6-sol":          {4.0, 0.4, 5.0, 20.0},
		"gpt-5.6-terra":        {2.0, 0.2, 2.5, 12.0},
		"gpt-5.6-luna":         {0.2, 0.02, 0.25, 1.2},
		"gpt-5.5":              {5.0, 0.5, 5.0, 30.0},
		"gpt-5.4":              {2.5, 0.25, 2.5, 15.0},
		"gpt-5.4-mini":         {0.75, 0.075, 0.75, 4.5},
		"gpt-5.3-codex-spark":  {1.75, 0.175, 1.75, 14.0},
		"codex-auto-review":    {1.75, 0.175, 1.75, 14.0},
	}
	longPrices := map[string][4]float64{
		"gpt-6-astra":   {20.0, 2.0, 25.0, 75.0},
		"gpt-5.6-sol":   {8.0, 0.8, 10.0, 30.0},
		"gpt-5.6-terra": {4.0, 0.4, 5.0, 18.0},
		"gpt-5.6-luna":  {0.4, 0.04, 0.5, 1.8},
		"gpt-5.5":       {10.0, 1.0, 10.0, 45.0},
		"gpt-5.4":       {5.0, 0.5, 5.0, 22.5},
	}

	expected := func(p [4]float64, prompt int, completion int) float64 {
		// dollars = (p*in + c*out) / 1e6 for plain vectors
		return (float64(prompt)*p[0] + float64(completion)*p[3]) / 1e6
	}
	expectedCache := func(p [4]float64, v officialVector) float64 {
		pNet := v.PromptTokens - v.CachedTokens - v.CacheCreationTokens
		return (float64(pNet)*p[0] + float64(v.CachedTokens)*p[1] +
			float64(v.CacheCreationTokens)*p[2] + float64(v.CompletionTokens)*p[3]) / 1e6
	}

	runVector := func(t *testing.T, expr string, v officialVector) float64 {
		usage := &dto.Usage{
			PromptTokens:     v.PromptTokens,
			CompletionTokens: v.CompletionTokens,
		}
		usage.PromptTokensDetails.CachedTokens = v.CachedTokens
		usage.PromptTokensDetails.CacheWriteTokens = v.CacheCreationTokens
		params := BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expr))
		raw, _, err := billingexpr.RunExprWithRequest(expr, params, billingexpr.RequestInput{})
		require.NoError(t, err)
		return raw / 1e6
	}

	for model, entry := range pricing.Models {
		t.Run(model+"/short_plain", func(t *testing.T) {
			v := pricing.Vectors["short_plain"]
			cost := runVector(t, entry.Expr, v)
			require.InDelta(t, expected(shortPrices[model], v.PromptTokens, v.CompletionTokens), cost, 1e-9)
		})
		t.Run(model+"/short_cache", func(t *testing.T) {
			v := pricing.Vectors["short_cache"]
			cost := runVector(t, entry.Expr, v)
			require.InDelta(t, expectedCache(shortPrices[model], v), cost, 1e-9)
		})
		if !entry.LongContextSupported {
			continue
		}
		t.Run(model+"/long", func(t *testing.T) {
			v := pricing.Vectors["long"]
			cost := runVector(t, entry.Expr, v)
			require.InDelta(t, expected(longPrices[model], v.PromptTokens, v.CompletionTokens), cost, 1e-9)
			// The tier boundary must not flip inside the short tier.
			boundary := pricing.Vectors["short_plain"]
			boundary.PromptTokens = 272000
			shortCost := runVector(t, entry.Expr, boundary)
			require.InDelta(t, expected(shortPrices[model], 272000, boundary.CompletionTokens), shortCost, 1e-9)
		})
	}
}

// TestOfficialModelPricingLongTierUsesActualContextLength verifies that the
// tier decision uses len (full input context including cache sub-categories),
// so a cached long-context request still lands in the long tier.
func TestOfficialModelPricingLongTierUsesActualContextLength(t *testing.T) {
	pricing := loadOfficialModelPricing(t)
	entry := pricing.Models["gpt-6-astra"]
	usage := &dto.Usage{PromptTokens: 300000, CompletionTokens: 10}
	usage.PromptTokensDetails.CachedTokens = 280000 // p_net = 20000 < 272000
	params := BuildTieredTokenParams(usage, false, billingexpr.UsedVars(entry.Expr))
	raw, trace, err := billingexpr.RunExprWithRequest(entry.Expr, params, billingexpr.RequestInput{})
	require.NoError(t, err)
	require.Equal(t, "long", trace.MatchedTier)
	// long: p_net*20 + cached*2 + c*75
	require.InDelta(t, (20000*20+280000*2+10*75)/1e6, raw/1e6, 1e-9)
	require.False(t, math.IsNaN(raw/1e6))
}
