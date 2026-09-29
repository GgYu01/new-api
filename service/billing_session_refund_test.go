package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalletFunding_RefundIsIdempotent(t *testing.T) {
	userID := 801
	initialQuota := 100_000
	require.NoError(t, model.DB.Create(&model.User{
		Id:       userID,
		Username: "wallet-test-user-801",
		AffCode:  "aff-801",
		Quota:    initialQuota,
		Role:     1,
	}).Error)

	wf := &WalletFunding{
		userId:   userID,
		consumed: 20_000,
	}

	// First refund restores 20_000
	require.NoError(t, wf.Refund())
	q, err := model.GetUserQuota(userID, false)
	require.NoError(t, err)
	assert.Equal(t, initialQuota+20_000, q)

	// Second refund is a strict no-op, preventing double refund
	require.NoError(t, wf.Refund())
	q2, err := model.GetUserQuota(userID, false)
	require.NoError(t, err)
	assert.Equal(t, initialQuota+20_000, q2)
}

func TestBillingSession_RefundWithWalletIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	userID := 802
	initialQuota := 50_000
	require.NoError(t, model.DB.Create(&model.User{
		Id:       userID,
		Username: "wallet-session-user-802",
		AffCode:  "aff-802",
		Quota:    initialQuota,
		Role:     1,
	}).Error)

	wf := &WalletFunding{
		userId:   userID,
		consumed: 10_000,
	}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:   userID,
			TokenId:  901,
			TokenKey: "tok-wallet-session",
		},
		funding:          wf,
		preConsumedQuota: 10_000,
		tokenConsumed:    10_000,
	}

	// First call to Refund marks refunded=true and returns quota
	session.Refund(c)
	assert.True(t, session.refunded)

	// Repeated call must return immediately because session.refunded is true
	session.Refund(c)
	assert.True(t, session.refunded)
}
