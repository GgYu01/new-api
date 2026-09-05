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

	consumed, settled, refunded, pendingRefund, legacy, err := ClassifySubscriptionPreConsumeRecords()
	require.NoError(t, err)
	require.Equal(t, int64(3), consumed)
	require.Equal(t, int64(1), settled)
	require.Equal(t, int64(1), refunded)
	require.Equal(t, int64(1), pendingRefund)
	require.Equal(t, int64(2), legacy, "req-c1 and req-c2 have no period snapshot")
}
