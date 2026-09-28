package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSubscriptionPlanTypeAcceptsOnlyTheTwoFamilies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	plan := &model.SubscriptionPlan{}
	require.True(t, normalizeSubscriptionPlanType(ctx, plan))
	require.Equal(t, model.SubscriptionTypeGPTOpenAICodex, plan.SubscriptionType)

	plan.SubscriptionType = model.SubscriptionTypeGrok
	require.True(t, normalizeSubscriptionPlanType(ctx, plan))
	require.Equal(t, model.SubscriptionTypeGrok, plan.SubscriptionType)

	plan.SubscriptionType = "claude"
	require.False(t, normalizeSubscriptionPlanType(ctx, plan))
}

func TestGetSubscriptionTypesExposesGPTAndGrok(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	GetSubscriptionTypes(ctx)

	require.Equal(t, 200, recorder.Code)
	var payload struct {
		Success bool                `json:"success"`
		Data    []map[string]string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Len(t, payload.Data, 2)
	require.Equal(t, model.SubscriptionTypeGPTOpenAICodex, payload.Data[0]["value"])
	require.Equal(t, model.SubscriptionTypeGrok, payload.Data[1]["value"])
}
