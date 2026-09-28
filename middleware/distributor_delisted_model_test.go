package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type distributorDelistedCase struct {
	name              string
	modelName         string
	group             string
	userID            int
	tokenScope        string
	scopeExempt       bool
	specificChannelID string
	seedChannelID     int
	seedChannelModel  string
	wantStatus        int
	wantErrorCode     string
	wantMessage       string
	forbidMessage     string
	wantDownstream    int
	wantChannelID     int
}

func setupDistributorDelistedFixture(t *testing.T) *gorm.DB {
	t.Helper()

	require.NoError(t, i18n.Init())

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRedis := common.RedisEnabled
	originalType := common.MainDatabaseType()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.UserSubscription{}, &model.SubscriptionPlan{}))

	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = true
	common.RedisEnabled = false

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RedisEnabled = originalRedis
		common.SetMainDatabaseType(originalType)
		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func seedDistributorChannel(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func seedGrokSubscription(t *testing.T, db *gorm.DB, userID int) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.UserSubscription{
		UserId:           userID,
		PlanId:           1,
		AmountTotal:      1000,
		Status:           "active",
		StartTime:        now - 60,
		EndTime:          now + 3600,
		SubscriptionType: model.SubscriptionTypeGrok,
		CreatedAt:        now,
	}).Error)
}

func Test_Distribute_respectsDelistedModelPolicyBeforeNoChannel503(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []distributorDelistedCase{
		{
			name:           "authorized_delisted_gpt55_no_channel_is_model_not_found",
			modelName:      "gpt-5.5",
			group:          "sub",
			tokenScope:     model.SubscriptionTypeGPTOpenAICodex,
			wantStatus:     http.StatusNotFound,
			wantErrorCode:  string(types.ErrorCodeModelNotFound),
			wantMessage:    "The model 'gpt-5.5' does not exist or has been delisted",
			forbidMessage:  "No available channel",
			wantDownstream: 0,
		},
		{
			name:           "authorized_delisted_prefixed_alias_no_channel_is_model_not_found",
			modelName:      "openai/gpt-5.5",
			group:          "sub",
			tokenScope:     model.SubscriptionTypeGPTOpenAICodex,
			wantStatus:     http.StatusNotFound,
			wantErrorCode:  string(types.ErrorCodeModelNotFound),
			wantMessage:    "The model 'openai/gpt-5.5' does not exist or has been delisted",
			forbidMessage:  "No available channel",
			wantDownstream: 0,
		},
		{
			name:           "authorized_non_delisted_no_channel_stays_503",
			modelName:      "gpt-5.6-sol",
			group:          "sub",
			tokenScope:     model.SubscriptionTypeGPTOpenAICodex,
			wantStatus:     http.StatusServiceUnavailable,
			wantErrorCode:  string(types.ErrorCodeModelNotFound),
			wantMessage:    "No available channel for model gpt-5.6-sol under group sub (distributor)",
			wantDownstream: 0,
		},
		{
			name:           "unscoped_key_denied_stays_403",
			modelName:      "gpt-5.5",
			group:          "sub",
			wantStatus:     http.StatusForbidden,
			wantErrorCode:  string(types.ErrorCodeModelNotFound),
			wantMessage:    "The active subscription does not allow model gpt-5.5",
			forbidMessage:  "No available channel",
			wantDownstream: 0,
		},
		{
			name:           "subscription_denied_stays_403",
			modelName:      "gpt-5.5",
			group:          "sub",
			userID:         42,
			tokenScope:     model.SubscriptionTypeGPTOpenAICodex,
			wantStatus:     http.StatusForbidden,
			wantErrorCode:  string(types.ErrorCodeModelNotFound),
			wantMessage:    "The active subscription does not allow model gpt-5.5",
			forbidMessage:  "No available channel",
			wantDownstream: 0,
		},
		{
			name:             "valid_model_routing_reaches_downstream_exactly_once",
			modelName:        "gpt-5.6-sol",
			group:            "sub",
			tokenScope:       model.SubscriptionTypeGPTOpenAICodex,
			seedChannelID:    3101,
			seedChannelModel: "gpt-5.6-sol",
			wantStatus:       http.StatusOK,
			wantDownstream:   1,
			wantChannelID:    3101,
		},
		{
			name:              "token_specific_channel_does_not_permit_delisted",
			modelName:         "gpt-5.5",
			group:             "sub",
			tokenScope:        model.SubscriptionTypeGPTOpenAICodex,
			specificChannelID: "3201",
			seedChannelID:     3201,
			seedChannelModel:  "gpt-5.5",
			wantStatus:        http.StatusNotFound,
			wantErrorCode:     string(types.ErrorCodeModelNotFound),
			wantMessage:       "The model 'gpt-5.5' does not exist or has been delisted",
			forbidMessage:     "No available channel",
			wantDownstream:    0,
		},
		{
			name:              "token_specific_valid_model_reaches_downstream_exactly_once",
			modelName:         "gpt-5.6-sol",
			group:             "sub",
			tokenScope:        model.SubscriptionTypeGPTOpenAICodex,
			specificChannelID: "3301",
			seedChannelID:     3301,
			seedChannelModel:  "gpt-5.6-sol",
			wantStatus:        http.StatusOK,
			wantDownstream:    1,
			wantChannelID:     3301,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupDistributorDelistedFixture(t)
			if tc.userID > 0 {
				seedGrokSubscription(t, db, tc.userID)
			}
			if tc.seedChannelID > 0 {
				seedDistributorChannel(t, db, tc.seedChannelID, tc.group, tc.seedChannelModel)
			}
			model.InitChannelCache()

			var downstream atomic.Int32
			router := gin.New()
			router.POST("/v1/chat/completions", func(c *gin.Context) {
				c.Set(common.RequestIdKey, "dist-delist-test")
				if tc.userID > 0 {
					c.Set("id", tc.userID)
				}
				if tc.tokenScope != "" {
					common.SetContextKey(c, constant.ContextKeyTokenSubscriptionType, tc.tokenScope)
				}
				if tc.scopeExempt {
					common.SetContextKey(c, constant.ContextKeyTokenScopeExempt, true)
				}
				common.SetContextKey(c, constant.ContextKeyUsingGroup, tc.group)
				common.SetContextKey(c, constant.ContextKeyUserGroup, tc.group)
				if tc.specificChannelID != "" {
					common.SetContextKey(c, constant.ContextKeyTokenSpecificChannelId, tc.specificChannelID)
				}
				c.Next()
			}, Distribute(), func(c *gin.Context) {
				downstream.Add(1)
				c.JSON(http.StatusOK, gin.H{
					"ok":         true,
					"channel_id": common.GetContextKeyInt(c, constant.ContextKeyChannelId),
				})
			})

			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}]}`, tc.modelName)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)
			t.Logf("http_status=%d downstream=%d body=%s", recorder.Code, downstream.Load(), recorder.Body.String())

			require.Equal(t, tc.wantStatus, recorder.Code, "body=%s", recorder.Body.String())
			assert.Equal(t, int32(tc.wantDownstream), downstream.Load())

			if tc.wantStatus == http.StatusOK {
				var payload struct {
					OK        bool `json:"ok"`
					ChannelID int  `json:"channel_id"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
				assert.True(t, payload.OK)
				assert.Equal(t, tc.wantChannelID, payload.ChannelID)
				return
			}

			var payload struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Equal(t, tc.wantErrorCode, payload.Error.Code)
			assert.Equal(t, "new_api_error", payload.Error.Type)
			if tc.wantMessage != "" {
				assert.Contains(t, payload.Error.Message, tc.wantMessage)
			}
			if tc.forbidMessage != "" {
				assert.NotContains(t, payload.Error.Message, tc.forbidMessage)
			}
		})
	}
}
