package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// seedTerminalSub creates an active subscription carrying explicit period
// markers so cross-period tests can simulate a reset without invoking the
// reset scheduler.
func seedTerminalSub(t *testing.T, id int, total, used, lastResetTime int64) {
	t.Helper()
	var plan SubscriptionPlan
	if err := DB.First(&plan, 1).Error; err != nil {
		require.NoError(t, DB.Create(&SubscriptionPlan{
			Id: 1, Title: "default", DurationUnit: "month", DurationValue: 1, Currency: "USD",
		}).Error)
	}
	require.NoError(t, DB.Create(&UserSubscription{
		Id: id, UserId: 1, PlanId: 1, AmountTotal: total, AmountUsed: used,
		StartTime: 1, EndTime: 1<<62 - 1, Status: "active", LastResetTime: lastResetTime,
	}).Error)
}

func getTerminalRecord(t *testing.T, requestId string) SubscriptionPreConsumeRecord {
	t.Helper()
	var record SubscriptionPreConsumeRecord
	require.NoError(t, DB.Where("request_id = ?", requestId).First(&record).Error)
	return record
}

// TestRefundSubscriptionPreConsumeSettledIsTerminal pins the review finding:
// a settled reservation was already converted into consumption; a late retry
// (crash recovery, duplicated cleanup) must never refund it again.
func TestRefundSubscriptionPreConsumeSettledIsTerminal(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 521, 1000, 250, 0)
	seedRefundRecord(t, "req-settled-refund", 521, 150, "settled")

	require.NoError(t, RefundSubscriptionPreConsume("req-settled-refund"))
	require.Equal(t, int64(250), getRefundSubUsed(t, 521), "settled record must not be refunded")
	require.Equal(t, "settled", getTerminalRecord(t, "req-settled-refund").Status)
}

// TestRefundSkipsQuotaAdjustmentAcrossPeriodReset pins the review scenario:
// old period pre-consumed 150, the period reset (new period usage 100), the
// old request is refunded — the new period's 100 must survive. A negative
// clamp is a safety net, not period policy.
func TestRefundSkipsQuotaAdjustmentAcrossPeriodReset(t *testing.T) {
	truncateTables(t)
	// sub.LastResetTime=200 > record.SubscriptionResetTime=100 → period moved.
	seedTerminalSub(t, 522, 1000, 100, 200)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId: "req-cross-period", UserId: 1, UserSubscriptionId: 522,
		PreConsumed: 150, Status: "consumed", SubscriptionResetTime: 100,
	}).Error)

	require.NoError(t, RefundSubscriptionPreConsume("req-cross-period"))
	require.Equal(t, int64(100), getRefundSubUsed(t, 522), "new-period usage must not be erased by an old-period refund")
	require.Equal(t, "refunded", getTerminalRecord(t, "req-cross-period").Status)
}

// TestRefundSamePeriodStillAdjustsQuota verifies the period gate only skips
// adjustments when the subscription actually reset after the reservation.
func TestRefundSamePeriodStillAdjustsQuota(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 523, 1000, 150, 100)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId: "req-same-period", UserId: 1, UserSubscriptionId: 523,
		PreConsumed: 150, Status: "consumed", SubscriptionResetTime: 100,
	}).Error)

	require.NoError(t, RefundSubscriptionPreConsume("req-same-period"))
	require.Equal(t, int64(0), getRefundSubUsed(t, 523))
	require.Equal(t, "refunded", getTerminalRecord(t, "req-same-period").Status)
}

// TestPreConsumeStampsSubscriptionResetTime verifies the period snapshot is
// recorded at reservation time.
func TestPreConsumeStampsSubscriptionResetTime(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 524, 1000, 0, 321)
	res, err := PreConsumeUserSubscription("req-stamp", 1, "", 0, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), res.PreConsumed)
	require.Equal(t, int64(321), getTerminalRecord(t, "req-stamp").SubscriptionResetTime)
}

// TestMarkSubscriptionPreConsumeRefundRequestedSemantics covers the durable
// refund-intent marker: consumed records are marked once, terminal records
// are untouched, unknown ids are not an error.
func TestMarkSubscriptionPreConsumeRefundRequestedSemantics(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 525, 1000, 100, 0)
	seedRefundRecord(t, "req-mark-consumed", 525, 50, "consumed")
	seedRefundRecord(t, "req-mark-refunded", 525, 50, "refunded")

	require.NoError(t, MarkSubscriptionPreConsumeRefundRequested("req-mark-consumed"))
	first := getTerminalRecord(t, "req-mark-consumed")
	require.Greater(t, first.RefundRequestedAt, int64(0))

	// Re-marking keeps the original timestamp (idempotent evidence).
	time.Sleep(time.Second)
	require.NoError(t, MarkSubscriptionPreConsumeRefundRequested("req-mark-consumed"))
	require.Equal(t, first.RefundRequestedAt, getTerminalRecord(t, "req-mark-consumed").RefundRequestedAt)

	require.NoError(t, MarkSubscriptionPreConsumeRefundRequested("req-mark-refunded"))
	require.Equal(t, int64(0), getTerminalRecord(t, "req-mark-refunded").RefundRequestedAt, "terminal records must not gain a refund marker")

	require.NoError(t, MarkSubscriptionPreConsumeRefundRequested("req-mark-missing"))
	require.Error(t, MarkSubscriptionPreConsumeRefundRequested(""))
}

// TestRecoverRequestedSubscriptionRefundsRetriesOnlyStuckMarkers covers crash
// recovery: a refund requested long ago but still "consumed" is retried; a
// fresh marker inside the grace window is left alone; already-terminal
// records are ignored.
func TestRecoverRequestedSubscriptionRefundsRetriesOnlyStuckMarkers(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 526, 1000, 300, 0)
	seedRefundRecord(t, "req-stuck", 526, 100, "consumed")
	seedRefundRecord(t, "req-fresh", 526, 100, "consumed")
	now := GetDBTimestamp()
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", "req-stuck").Update("refund_requested_at", now-3600).Error)
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", "req-fresh").Update("refund_requested_at", now).Error)

	recovered, err := RecoverRequestedSubscriptionRefunds(600, 100)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, "refunded", getTerminalRecord(t, "req-stuck").Status)
	require.Equal(t, int64(300-100), getRefundSubUsed(t, 526))
	require.Equal(t, "consumed", getTerminalRecord(t, "req-fresh").Status)

	// Idempotent: a second sweep finds nothing new.
	recovered, err = RecoverRequestedSubscriptionRefunds(600, 100)
	require.NoError(t, err)
	require.Equal(t, 0, recovered)
}

// TestClassifySubscriptionPreConsumeRecordsCountsStates pins the read-only
// observability classification, including legacy rows without period
// evidence.
func TestClassifySubscriptionPreConsumeRecordsCountsStates(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 527, 10000, 0, 0)
	seedRefundRecord(t, "req-c1", 527, 10, "consumed")
	seedRefundRecord(t, "req-c2", 527, 10, "consumed")
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", "req-c2").Update("refund_requested_at", 123).Error)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{RequestId: "req-c3", UserId: 1, UserSubscriptionId: 527, PreConsumed: 10, Status: "consumed", SubscriptionResetTime: 999}).Error)
	seedRefundRecord(t, "req-s1", 527, 10, "settled")
	seedRefundRecord(t, "req-r1", 527, 10, "refunded")

	consumed, settled, refunded, pendingRefund, legacy, incomplete, err := ClassifySubscriptionPreConsumeRecords()
	require.NoError(t, err)
	require.False(t, incomplete)
	require.Equal(t, int64(3), consumed)
	require.Equal(t, int64(1), settled)
	require.Equal(t, int64(1), refunded)
	require.Equal(t, int64(1), pendingRefund)
	require.Equal(t, int64(2), legacy, "req-c1 and req-c2 have no period snapshot")
}

// TestSettleSkipsQuotaAdjustmentAcrossPeriodReset verifies that an old-period reservation
// (e.g. pre-consumed 150) settling in a new period (after reset with new usage 100)
// with actual usage 50 (delta -100) does NOT erase the new period's 100 usage.
func TestSettleSkipsQuotaAdjustmentAcrossPeriodReset(t *testing.T) {
	truncateTables(t)
	// sub.LastResetTime=200 > record.SubscriptionResetTime=100 → period moved.
	seedTerminalSub(t, 528, 1000, 100, 200)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId: "req-cross-period-settle", UserId: 1, UserSubscriptionId: 528,
		PreConsumed: 150, Status: "consumed", SubscriptionResetTime: 100,
	}).Error)

	require.NoError(t, SettleSubscriptionPreConsume("req-cross-period-settle", -100))
	require.Equal(t, int64(100), getRefundSubUsed(t, 528), "new-period usage must not be erased by old-period negative delta settlement")
	require.Equal(t, "settled", getTerminalRecord(t, "req-cross-period-settle").Status)
}

// TestReserveExtraSubscriptionQuotaAtomic covers atomic extra reserve and rollback across resets.
func TestReserveExtraSubscriptionQuotaAtomic(t *testing.T) {
	truncateTables(t)
	seedTerminalSub(t, 529, 1000, 100, 100)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId: "req-extra-reserve", UserId: 1, UserSubscriptionId: 529,
		PreConsumed: 100, Status: "consumed", SubscriptionResetTime: 100,
	}).Error)

	// 1. Reserve extra 50
	require.NoError(t, ReserveExtraSubscriptionQuota(529, "req-extra-reserve", 50))
	require.Equal(t, int64(150), getRefundSubUsed(t, 529))
	require.Equal(t, int64(50), getTerminalRecord(t, "req-extra-reserve").ExtraReserved)

	// 2. Rollback extra 50
	require.NoError(t, RollbackExtraSubscriptionQuota(529, "req-extra-reserve", 50))
	require.Equal(t, int64(100), getRefundSubUsed(t, 529))
	require.Equal(t, int64(0), getTerminalRecord(t, "req-extra-reserve").ExtraReserved)

	// Duplicate rollback should be a no-op (idempotent) and not subtract quota again
	require.NoError(t, RollbackExtraSubscriptionQuota(529, "req-extra-reserve", 50))
	require.Equal(t, int64(100), getRefundSubUsed(t, 529), "duplicate rollback must not subtract quota again")
	require.Equal(t, int64(0), getTerminalRecord(t, "req-extra-reserve").ExtraReserved)

	// 3. Reject extra reserve across period reset
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", 529).Update("last_reset_time", 200).Error)
	err := ReserveExtraSubscriptionQuota(529, "req-extra-reserve", 50)
	require.Error(t, err)
	require.Contains(t, err.Error(), "across subscription period reset")
}

func TestRecoverPendingTokenSettlements(t *testing.T) {
	truncateTables(t)
	token := Token{
		UserId:         1,
		Key:            "sk-test-pending-rec",
		RemainQuota:    1000,
		UnlimitedQuota: false,
	}
	require.NoError(t, DB.Create(&token).Error)

	seedTerminalSub(t, 530, 10000, 500, 100)
	record := SubscriptionPreConsumeRecord{
		RequestId:          "req-pending-tok",
		UserId:             1,
		UserSubscriptionId: 530,
		PreConsumed:        100,
		Status:             "settled",
		TokenId:            token.Id,
		TokenKey:           token.Key,
		TokenDelta:         50,
		TokenSettled:       false,
	}
	require.NoError(t, DB.Create(&record).Error)
	now := GetDBTimestamp()
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", "req-pending-tok").UpdateColumn("updated_at", now-100).Error)

	recovered, err := RecoverPendingTokenSettlements(1, 10)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	var updatedRecord SubscriptionPreConsumeRecord
	require.NoError(t, DB.Where("request_id = ?", "req-pending-tok").First(&updatedRecord).Error)
	require.True(t, updatedRecord.TokenSettled)

	var updatedToken Token
	require.NoError(t, DB.Where("id = ?", token.Id).First(&updatedToken).Error)
	require.Equal(t, 950, updatedToken.RemainQuota)
}

func TestSettleSubscriptionPreConsumeWithTokenAndTerminalRollback(t *testing.T) {
	truncateTables(t)
	token := Token{
		UserId:         1,
		Key:            "sk-test-settle-tok",
		RemainQuota:    2000,
		UnlimitedQuota: false,
	}
	require.NoError(t, DB.Create(&token).Error)

	seedTerminalSub(t, 531, 10000, 500, 100)
	record := SubscriptionPreConsumeRecord{
		RequestId:          "req-settle-tok",
		UserId:             1,
		UserSubscriptionId: 531,
		PreConsumed:        100,
		ExtraReserved:      50,
		Status:             "consumed",
	}
	require.NoError(t, DB.Create(&record).Error)

	// Settle with token delta
	err := SettleSubscriptionPreConsumeWithToken("req-settle-tok", 20, token.Id, token.Key, 20)
	require.NoError(t, err)

	var settledRecord SubscriptionPreConsumeRecord
	require.NoError(t, DB.Where("request_id = ?", "req-settle-tok").First(&settledRecord).Error)
	require.Equal(t, "settled", settledRecord.Status)
	require.Equal(t, token.Id, settledRecord.TokenId)
	require.Equal(t, token.Key, settledRecord.TokenKey)
	require.Equal(t, int64(20), settledRecord.TokenDelta)
	require.False(t, settledRecord.TokenSettled)

	// Rolling back an already settled record must be a no-op
	err = RollbackExtraSubscriptionQuota(531, "req-settle-tok", 50)
	require.NoError(t, err)
	require.Equal(t, int64(50), getTerminalRecord(t, "req-settle-tok").ExtraReserved)
}


