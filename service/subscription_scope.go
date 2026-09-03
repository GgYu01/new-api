package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// SubscriptionAccessForRequest loads the active provider scope once per
// request. Middleware and billing share the context value so ordinary traffic
// does not pay for duplicate subscription queries.
func SubscriptionAccessForRequest(c *gin.Context, userId int) (model.SubscriptionAccess, error) {
	if userId <= 0 {
		return model.SubscriptionAccess{}, nil
	}
	if c != nil {
		if value, ok := common.GetContextKey(c, constant.ContextKeySubscriptionAccess); ok {
			if access, valid := value.(model.SubscriptionAccess); valid {
				return access, nil
			}
		}
	}
	access, err := model.GetActiveSubscriptionAccess(userId)
	if err != nil {
		return model.SubscriptionAccess{}, err
	}
	if c != nil {
		common.SetContextKey(c, constant.ContextKeySubscriptionAccess, access)
	}
	return access, nil
}

func CheckSubscriptionModelAccess(c *gin.Context, userId int, modelName string, requiresModel bool) error {
	if userId <= 0 {
		return nil
	}
	access, err := SubscriptionAccessForRequest(c, userId)
	if err != nil {
		return err
	}
	if !access.AllowsRequestModel(modelName, requiresModel) {
		return model.ErrSubscriptionModelNotAllowed
	}
	// Intersect the user's active subscription union with the scope carried by
	// the API key. This keeps GPT and Grok billing independent even when one
	// user owns both subscriptions.
	if tokenScope := common.GetContextKeyString(c, constant.ContextKeyTokenSubscriptionType); tokenScope != "" &&
		strings.TrimSpace(modelName) != "" && !model.TokenSubscriptionTypeAllowsModel(tokenScope, modelName) {
		return model.ErrSubscriptionModelNotAllowed
	}
	return nil
}
