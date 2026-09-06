package service

import (
	"errors"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockFundingSource struct {
	source        string
	settleCalled  int
	settleErr     error
	refundCalled  int
	refundErr     error
	lastDelta     int
}

func (m *mockFundingSource) PreConsume(quota int) error {
	return nil
}

func (m *mockFundingSource) Settle(delta int) error {
	m.settleCalled++
	m.lastDelta = delta
	return m.settleErr
}

func (m *mockFundingSource) Refund() error {
	m.refundCalled++
	return m.refundErr
}

func (m *mockFundingSource) Source() string {
	return m.source
}

func TestBillingSessionSettle_TokenAdjustmentRetry(t *testing.T) {
	origAdjust := adjustTokenQuotaFn
	defer func() { adjustTokenQuotaFn = origAdjust }()

	tokenShouldFail := true
	tokenAdjustCalls := 0
	adjustTokenQuotaFn = func(tokenId int, tokenKey string, delta int) error {
		tokenAdjustCalls++
		if tokenShouldFail {
			return errors.New("simulated token adjustment failure")
		}
		return nil
	}

	funding := &mockFundingSource{source: BillingSourceWallet}
	info := &relaycommon.RelayInfo{
		UserId:       1,
		TokenId:      1,
		TokenKey:     "test-key",
		IsPlayground: false,
	}

	session := &BillingSession{
		relayInfo:        info,
		funding:          funding,
		preConsumedQuota: 100,
	}

	// 1. Settle with actual 150 (delta +50) -> funding succeeds, but token adjustment fails
	err := session.Settle(150)
	require.Error(t, err, "expected error adjusting token")
	assert.True(t, session.fundingSettled, "funding should be marked settled")
	assert.False(t, session.tokenSettled, "tokenSettled should remain false on error")
	assert.False(t, session.settled, "session should NOT be marked fully settled")
	assert.Equal(t, 1, funding.settleCalled, "funding.Settle should have been called once")
	assert.Equal(t, 1, tokenAdjustCalls)

	// 2. Retry while token still fails -> should still return error and NOT return premature nil
	err2 := session.Settle(150)
	require.Error(t, err2, "retry must NOT return premature nil while token adjustment is pending")
	assert.Equal(t, 1, funding.settleCalled, "funding must NOT be double-settled on retry")
	assert.Equal(t, 2, tokenAdjustCalls)
	assert.False(t, session.settled)

	// 3. Token adjustment succeeds on next retry
	tokenShouldFail = false
	err3 := session.Settle(150)
	require.NoError(t, err3)
	assert.True(t, session.tokenSettled)
	assert.True(t, session.settled)
	assert.Equal(t, 1, funding.settleCalled, "funding still called only once")
	assert.Equal(t, 3, tokenAdjustCalls)
}

func TestBillingSessionSettle_ZeroDeltaTransitionsFunding(t *testing.T) {
	funding := &mockFundingSource{source: BillingSourceSubscription}
	info := &relaycommon.RelayInfo{
		UserId:       1,
		TokenId:      1,
		IsPlayground: true,
	}

	session := &BillingSession{
		relayInfo:        info,
		funding:          funding,
		preConsumedQuota: 100,
	}

	// Settle with actual 100 (delta == 0) -> funding must be called to transition record to settled
	err := session.Settle(100)
	require.NoError(t, err)
	assert.Equal(t, 1, funding.settleCalled)
	assert.Equal(t, 0, funding.lastDelta)
	assert.True(t, session.fundingSettled)
	assert.True(t, session.settled)
}
