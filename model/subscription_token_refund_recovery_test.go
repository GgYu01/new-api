package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func seedTestToken(t *testing.T, id int, key string, remain, used int64) {
	t.Helper()
	require.NoError(t, DB.Create(&Token{
		Id:          id,
		UserId:      1,
		Key:         key,
		Name:        "test-token",
		RemainQuota: int(remain),
		UsedQuota:   int(used),
		Status:      1,
	}).Error)
}

func getTestToken(t *testing.T, id int) Token {
	t.Helper()
	var tok Token
	require.NoError(t, DB.Where("id = ?", id).First(&tok).Error)
	return tok
}

func TestRefundSubscriptionPreConsumeWithToken_AtomicSuccess(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 601, 1000, 400)
	seedTestToken(t, 701, "tok-test-1", 500, 100)
	seedRefundRecord(t, "req-atomic-1", 601, 100, "consumed")

	// Atomically refund both subscription and token
	require.NoError(t, RefundSubscriptionPreConsumeWithToken("req-atomic-1", 701, "tok-test-1", 100))

	// Verify subscription quota refunded
	require.Equal(t, int64(300), getRefundSubUsed(t, 601))

	// Verify token quota refunded
	tok := getTestToken(t, 701)
	require.Equal(t, 600, tok.RemainQuota)
	require.Equal(t, 0, tok.UsedQuota)

	// Verify record status
	rec := getTerminalRecord(t, "req-atomic-1")
	require.Equal(t, "refunded", rec.Status)
	require.True(t, rec.TokenSettled)
	require.Equal(t, int64(-100), rec.TokenDelta)

	// Repeated call must be completely idempotent: no double refund!
	require.NoError(t, RefundSubscriptionPreConsumeWithToken("req-atomic-1", 701, "tok-test-1", 100))
	require.Equal(t, int64(300), getRefundSubUsed(t, 601))
	tok2 := getTestToken(t, 701)
	require.Equal(t, 600, tok2.RemainQuota)
	require.Equal(t, 0, tok2.UsedQuota)
}

func TestRefundSubscriptionPreConsumeWithToken_CrashRecoveryReentrant(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 602, 1000, 300) // Subscription was already refunded from 400 to 300 before crash
	seedTestToken(t, 702, "tok-test-2", 500, 100) // Token quota was not yet refunded when crash occurred

	// Record was left in status="refunded" but TokenSettled=false
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId:          "req-crash-interrupted",
		UserId:             1,
		UserSubscriptionId: 602,
		PreConsumed:        100,
		Status:             "refunded",
		TokenId:            702,
		TokenKey:           "tok-test-2",
		TokenDelta:         -100,
		TokenSettled:       false,
	}).Error)

	// Crash recovery or retry executes
	require.NoError(t, RefundSubscriptionPreConsumeWithToken("req-crash-interrupted", 702, "tok-test-2", 100))

	// Subscription used quota must NOT be refunded again!
	require.Equal(t, int64(300), getRefundSubUsed(t, 602))

	// Token quota must be cleanly refunded!
	tok := getTestToken(t, 702)
	require.Equal(t, 600, tok.RemainQuota)
	require.Equal(t, 0, tok.UsedQuota)

	rec := getTerminalRecord(t, "req-crash-interrupted")
	require.True(t, rec.TokenSettled)
}

func TestRecoverRequestedSubscriptionRefunds_PicksUpInterruptedTokenRefund(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 603, 1000, 400)
	seedTestToken(t, 703, "tok-test-3", 500, 100)

	// Record with refund requested before goroutine ran
	now := GetDBTimestamp()
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId:          "req-recovery-sweep",
		UserId:             1,
		UserSubscriptionId: 603,
		PreConsumed:        100,
		Status:             "consumed",
		RefundRequestedAt:  now - 1000,
		TokenId:            703,
		TokenKey:           "tok-test-3",
		TokenDelta:         -100,
		TokenSettled:       false,
	}).Error)

	recovered, err := RecoverRequestedSubscriptionRefunds(60, 10)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	// Both subscription and token must be refunded
	require.Equal(t, int64(300), getRefundSubUsed(t, 603))
	tok := getTestToken(t, 703)
	require.Equal(t, 600, tok.RemainQuota)
	require.Equal(t, 0, tok.UsedQuota)
}
