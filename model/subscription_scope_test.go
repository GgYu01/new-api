package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexAutoReviewCannotUseNativeCodexChannel(t *testing.T) {
	assert.False(t, channelCanServeModel(&Channel{Type: constant.ChannelTypeCodex}, "codex-auto-review"))
	assert.False(t, channelCanServeModel(&Channel{Type: constant.ChannelTypeCodex}, "codex-auto-review-compact"))
	assert.True(t, channelCanServeModel(&Channel{Type: constant.ChannelTypeOpenAI}, "codex-auto-review"))
	assert.True(t, channelCanServeModel(&Channel{Type: constant.ChannelTypeCodex}, "gpt-5.6-sol"))
}

func TestGetChannelSkipsStaleCodexAutoReviewAbility(t *testing.T) {
	truncateTables(t)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	require.NoError(t, DB.Create(&Channel{Id: 9800, Name: "legacy-codex", Type: constant.ChannelTypeCodex, Status: 1, Models: "codex-auto-review", Group: "default"}).Error)
	priority := int64(0)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "codex-auto-review", ChannelId: 9800, Enabled: true, Priority: &priority}).Error)

	selected, err := GetChannel("default", "codex-auto-review", 0, "")
	require.NoError(t, err)
	assert.Nil(t, selected)
}

func TestSubscriptionModelFamilyClassifiesProviderNames(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		family SubscriptionModelFamily
	}{
		{name: "codex auto review is openai", model: "codex-auto-review", family: SubscriptionModelFamilyGPTOpenAICodex},
		{name: "gpt compact is openai", model: "gpt-5.6-sol-compact", family: SubscriptionModelFamilyGPTOpenAICodex},
		{name: "prefixed openai model", model: "openai/gpt-5.4", family: SubscriptionModelFamilyGPTOpenAICodex},
		{name: "grok model", model: "grok-4.6", family: SubscriptionModelFamilyGrok},
		{name: "prefixed xai model", model: "xai/grok-imagine-image", family: SubscriptionModelFamilyGrok},
		{name: "xai dashed model", model: "xai-grok-code", family: SubscriptionModelFamilyGrok},
		{name: "unknown provider", model: "claude-opus-5", family: SubscriptionModelFamilyUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.family, SubscriptionModelFamilyForModel(tt.model))
		})
	}
}

func TestSubscriptionTypeNormalizesLegacyAndRejectsUnknown(t *testing.T) {
	assert.Equal(t, SubscriptionTypeGPTOpenAICodex, NormalizeSubscriptionType(""))
	assert.Equal(t, SubscriptionTypeGPTOpenAICodex, NormalizeSubscriptionType("openai"))
	assert.Equal(t, SubscriptionTypeGrok, NormalizeSubscriptionType("xai"))
	assert.Error(t, ValidateSubscriptionType("other"))
	assert.NoError(t, ValidateSubscriptionType(SubscriptionTypeGPTOpenAICodex))
}

func TestActiveSubscriptionTypeAllowsOnlyMatchingProviderFamily(t *testing.T) {
	assert.True(t, IsModelAllowedBySubscriptionType(SubscriptionTypeGPTOpenAICodex, "codex-auto-review"))
	assert.True(t, IsModelAllowedBySubscriptionType(SubscriptionTypeGrok, "grok-4.6"))
	assert.False(t, IsModelAllowedBySubscriptionType(SubscriptionTypeGPTOpenAICodex, "grok-4.6"))
	assert.False(t, IsModelAllowedBySubscriptionType(SubscriptionTypeGrok, "gpt-5.6-sol"))
	assert.False(t, IsModelAllowedBySubscriptionType(SubscriptionTypeGrok, "claude-opus-5"))
	access := SubscriptionAccess{Enforced: true, Types: []string{SubscriptionTypeGrok}}
	assert.False(t, access.AllowsRequestModel("", true))
	assert.True(t, access.AllowsRequestModel("", false))
}

func TestTokenSubscriptionTypeIntersectsUsersSubscriptionUnion(t *testing.T) {
	models := []string{"gpt-5.6-sol", "grok-4.6", "claude-opus-5"}
	gpt, err := FilterModelsForTokenSubscriptionType(SubscriptionTypeGPTOpenAICodex, models)
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-5.6-sol"}, gpt)
	grok, err := FilterModelsForTokenSubscriptionType(SubscriptionTypeGrok, models)
	require.NoError(t, err)
	assert.Equal(t, []string{"grok-4.6"}, grok)
	assert.False(t, TokenSubscriptionTypeAllowsModel(SubscriptionTypeGrok, "gpt-5.6-sol"))
	assert.True(t, TokenSubscriptionTypeAllowsModel("", "grok-4.6"), "legacy unscoped keys remain compatible")
}

func TestFilterModelsForSubscriptionPreservesLegacyUnsubscribedUsers(t *testing.T) {
	truncateTables(t)
	models := []string{"gpt-5.6-sol", "grok-4.6", "claude-opus-5"}
	filtered, err := FilterModelsForSubscription(9799, models)
	require.NoError(t, err)
	assert.Equal(t, models, filtered)
}

func TestFilterModelsForSubscriptionReturnsOnlyTheActiveTypeUnion(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	require.NoError(t, DB.Create(&SubscriptionPlan{Id: 9780, Title: "GPT", TotalAmount: 100, SubscriptionType: SubscriptionTypeGPTOpenAICodex}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9781, UserId: 9782, PlanId: 9780, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGPTOpenAICodex}).Error)

	filtered, err := FilterModelsForSubscription(9782, []string{"gpt-5.6-sol", "codex-auto-review", "grok-4.6", "claude-opus-5"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-5.6-sol", "codex-auto-review"}, filtered)
}

func TestGetActiveSubscriptionAccessUnionsDistinctTypes(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	require.NoError(t, DB.Create(&SubscriptionPlan{Id: 9770, Title: "GPT", TotalAmount: 100, SubscriptionType: SubscriptionTypeGPTOpenAICodex}).Error)
	require.NoError(t, DB.Create(&SubscriptionPlan{Id: 9771, Title: "Grok", TotalAmount: 100, SubscriptionType: SubscriptionTypeGrok}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9772, UserId: 9773, PlanId: 9770, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGPTOpenAICodex}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9774, UserId: 9773, PlanId: 9771, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGrok}).Error)

	access, err := GetActiveSubscriptionAccess(9773)
	require.NoError(t, err)
	assert.True(t, access.Enforced)
	assert.Equal(t, []string{SubscriptionTypeGPTOpenAICodex, SubscriptionTypeGrok}, access.Types)
	assert.True(t, access.AllowsModel("gpt-5.6-sol"))
	assert.True(t, access.AllowsModel("grok-4.6"))
}

func TestCreateUserSubscriptionSnapshotsTypeFromPlan(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 9760, Username: "snapshot-scope-user", Password: "unused", Status: 1, Group: "default"}
	require.NoError(t, DB.Create(user).Error)
	plan := &SubscriptionPlan{Id: 9761, Title: "Grok", TotalAmount: 100, SubscriptionType: SubscriptionTypeGrok}
	require.NoError(t, DB.Create(plan).Error)
	subscription, err := CreateUserSubscriptionFromPlanTx(DB, user.Id, plan, "test")
	require.NoError(t, err)
	assert.Equal(t, SubscriptionTypeGrok, subscription.SubscriptionType)

	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).Update("subscription_type", SubscriptionTypeGPTOpenAICodex).Error)
	var persisted UserSubscription
	require.NoError(t, DB.First(&persisted, subscription.Id).Error)
	assert.Equal(t, SubscriptionTypeGrok, persisted.SubscriptionType)
}

func TestSubscriptionPlanMapUpdatePreservesGrokType(t *testing.T) {
	truncateTables(t)
	plan := &SubscriptionPlan{Id: 9751, Title: "Grok map update", TotalAmount: 100, SubscriptionType: SubscriptionTypeGrok}
	require.NoError(t, DB.Create(plan).Error)
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]interface{}{
		"title":             "Grok map update v2",
		"subscription_type": SubscriptionTypeGrok,
	}).Error)
	var persisted SubscriptionPlan
	require.NoError(t, DB.First(&persisted, plan.Id).Error)
	assert.Equal(t, SubscriptionTypeGrok, persisted.SubscriptionType)
}

func TestPreConsumeSubscriptionUsesOnlyMatchingSubscriptionType(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	user := &User{Id: 9701, Username: "subscription-scope-user", Password: "unused", Status: 1, Group: "default"}
	require.NoError(t, DB.Create(user).Error)
	gptPlan := &SubscriptionPlan{Id: 9702, Title: "GPT only", TotalAmount: 100, SubscriptionType: SubscriptionTypeGPTOpenAICodex}
	grokPlan := &SubscriptionPlan{Id: 9703, Title: "Grok only", TotalAmount: 100, SubscriptionType: SubscriptionTypeGrok}
	require.NoError(t, DB.Create(gptPlan).Error)
	require.NoError(t, DB.Create(grokPlan).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9704, UserId: user.Id, PlanId: gptPlan.Id, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGPTOpenAICodex}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9705, UserId: user.Id, PlanId: grokPlan.Id, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGrok}).Error)

	grok, err := PreConsumeUserSubscription("scope-grok", user.Id, "grok-4.6", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 9705, grok.UserSubscriptionId)

	gpt, err := PreConsumeUserSubscription("scope-gpt", user.Id, "codex-auto-review", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 9704, gpt.UserSubscriptionId)

	_, err = PreConsumeUserSubscription("scope-denied", user.Id, "claude-opus-5", 0, 10)
	assert.ErrorIs(t, err, ErrSubscriptionModelNotAllowed)
	var deniedSub UserSubscription
	require.NoError(t, DB.First(&deniedSub, 9704).Error)
	assert.EqualValues(t, 10, deniedSub.AmountUsed, "a denied provider must not consume quota")
}

func TestBackfillSubscriptionTypesDefaultsLegacyRowsAndKeepsExplicitGrok(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	require.NoError(t, DB.Create(&SubscriptionPlan{Id: 9710, Title: "legacy", TotalAmount: 100, SubscriptionType: ""}).Error)
	require.NoError(t, DB.Create(&SubscriptionPlan{Id: 9711, Title: "grok", TotalAmount: 100, SubscriptionType: SubscriptionTypeGrok}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9712, UserId: 1, PlanId: 9710, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: ""}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9713, UserId: 1, PlanId: 9711, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGrok}).Error)

	// Hooks normalize new rows, so emulate a pre-migration empty value directly.
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", 9710).Update("subscription_type", "").Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", 9712).Update("subscription_type", "").Error)
	require.NoError(t, BackfillSubscriptionTypes())

	var legacyPlan SubscriptionPlan
	var legacySub UserSubscription
	var grokSub UserSubscription
	require.NoError(t, DB.First(&legacyPlan, 9710).Error)
	require.NoError(t, DB.First(&legacySub, 9712).Error)
	require.NoError(t, DB.First(&grokSub, 9713).Error)
	assert.Equal(t, SubscriptionTypeGPTOpenAICodex, legacyPlan.SubscriptionType)
	assert.Equal(t, SubscriptionTypeGPTOpenAICodex, legacySub.SubscriptionType)
	assert.Equal(t, SubscriptionTypeGrok, grokSub.SubscriptionType)
}

func TestWalletOverflowDecisionIsScopedToSelectedSubscription(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	userID := 9720
	// GPT is strict, while Grok explicitly permits wallet fallback. The Grok
	// request must not be blocked by the unrelated GPT row.
	require.NoError(t, DB.Create(&UserSubscription{Id: 9721, UserId: userID, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGPTOpenAICodex, AllowWalletOverflow: false}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9722, UserId: userID, AmountTotal: 100, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGrok, AllowWalletOverflow: true}).Error)

	grokAllowed, err := UserActiveSubscriptionsAllowWalletOverflow(userID, SubscriptionTypeGrok)
	require.NoError(t, err)
	assert.True(t, grokAllowed)
	gptAllowed, err := UserActiveSubscriptionsAllowWalletOverflow(userID, SubscriptionTypeGPTOpenAICodex)
	require.NoError(t, err)
	assert.False(t, gptAllowed)
}

func TestPreviewTokenScopeMigrationIsSecretFreeAndClassifiesActions(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	require.NoError(t, DB.Create(&Token{Id: 9731, UserId: 9730, Key: "secret-must-not-be-read", SubscriptionType: SubscriptionTypeGrok}).Error)
	require.NoError(t, DB.Create(&Token{Id: 9732, UserId: 9730, Key: "legacy-single"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 9733, UserId: 9731, Key: "legacy-ambiguous"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 9734, UserId: 9732, Key: "legacy-none"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 9735, UserId: 0, Key: "system", ScopeExempt: true}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9736, UserId: 9730, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGrok}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9737, UserId: 9731, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGrok}).Error)
	require.NoError(t, DB.Create(&UserSubscription{Id: 9738, UserId: 9731, EndTime: now + 3600, Status: "active", SubscriptionType: SubscriptionTypeGPTOpenAICodex}).Error)

	report, err := PreviewTokenScopeMigration()
	require.NoError(t, err)
	assert.Equal(t, 1, report.ExplicitGrok)
	assert.Equal(t, 1, report.EmptySingleFamily)
	assert.Equal(t, 1, report.EmptyAmbiguous)
	assert.Equal(t, 1, report.EmptyNoFamily)
	assert.Equal(t, 1, report.SystemExempt)
}
