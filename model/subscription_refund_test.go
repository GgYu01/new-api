package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func seedRefundSub(t *testing.T, id int, total, used int64) {
	t.Helper()
	require.NoError(t, DB.Create(&UserSubscription{
		Id: id, UserId: 1, PlanId: 1, AmountTotal: total, AmountUsed: used,
		StartTime: 1, EndTime: 1<<62 - 1, Status: "active",
	}).Error)
}

func seedRefundRecord(t *testing.T, requestId string, subId int, preConsumed int64, status string) {
	t.Helper()
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId: requestId, UserId: 1, UserSubscriptionId: subId,
		PreConsumed: preConsumed, Status: status,
	}).Error)
}

func getRefundSubUsed(t *testing.T, id int) int64 {
	t.Helper()
	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", id).First(&sub).Error)
	return sub.AmountUsed
}

func getRefundRecordStatus(t *testing.T, requestId string) string {
	t.Helper()
	var record SubscriptionPreConsumeRecord
	require.NoError(t, DB.Where("request_id = ?", requestId).First(&record).Error)
	return record.Status
}

func TestRefundSubscriptionPreConsumeAdjustsQuotaAndStatusAtomically(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 501, 1000, 400)
	seedRefundRecord(t, "req-refund-ok", 501, 150, "consumed")

	require.NoError(t, RefundSubscriptionPreConsume("req-refund-ok"))
	require.Equal(t, int64(250), getRefundSubUsed(t, 501))
	require.Equal(t, "refunded", getRefundRecordStatus(t, "req-refund-ok"))
}

func TestRefundSubscriptionPreConsumeIsIdempotentWithoutDoubleRefund(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 502, 1000, 400)
	seedRefundRecord(t, "req-refund-dup", 502, 150, "consumed")

	require.NoError(t, RefundSubscriptionPreConsume("req-refund-dup"))
	require.Equal(t, int64(250), getRefundSubUsed(t, 502))
	// A repeated refund (retry after lost response, crash recovery, race) must
	// leave the quota unchanged: the locked refunded status is the only gate.
	require.NoError(t, RefundSubscriptionPreConsume("req-refund-dup"))
	require.Equal(t, int64(250), getRefundSubUsed(t, 502))
	require.Equal(t, "refunded", getRefundRecordStatus(t, "req-refund-dup"))
}

func TestRefundAfterQuotaResetClampsToZeroAndTerminates(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 503, 1000, 0) // quota was reset after the pre-consume
	seedRefundRecord(t, "req-refund-reset", 503, 150, "consumed")

	require.NoError(t, RefundSubscriptionPreConsume("req-refund-reset"))
	// The old period's pre-consume must not become new-period extra quota.
	require.Equal(t, int64(0), getRefundSubUsed(t, 503))
	require.Equal(t, "refunded", getRefundRecordStatus(t, "req-refund-reset"))
}

func TestMarkSubscriptionPreConsumeSettledTransitionsOnlyConsumed(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 504, 1000, 100)
	seedRefundRecord(t, "req-settle-1", 504, 100, "consumed")
	seedRefundRecord(t, "req-settle-2", 504, 100, "refunded")

	require.NoError(t, MarkSubscriptionPreConsumeSettled("req-settle-1"))
	require.Equal(t, "settled", getRefundRecordStatus(t, "req-settle-1"))
	// Refunded is terminal: marking settled must not overwrite it.
	require.NoError(t, MarkSubscriptionPreConsumeSettled("req-settle-2"))
	require.Equal(t, "refunded", getRefundRecordStatus(t, "req-settle-2"))
	// Unknown requestId is not an error.
	require.NoError(t, MarkSubscriptionPreConsumeSettled("req-settle-missing"))
}

func TestCleanupSubscriptionPreConsumeRecordsKeepsConsumed(t *testing.T) {
	truncateTables(t)
	seedRefundSub(t, 505, 1000, 100)
	seedRefundRecord(t, "req-old-refunded", 505, 10, "refunded")
	seedRefundRecord(t, "req-old-settled", 505, 10, "settled")
	seedRefundRecord(t, "req-old-consumed", 505, 10, "consumed")
	// Age every record past the cutoff.
	require.NoError(t, DB.Exec("UPDATE subscription_pre_consume_records SET updated_at = 1").Error)

	deleted, err := CleanupSubscriptionPreConsumeRecords(60)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	// The consumed record may hold un-recovered quota and must survive.
	require.Equal(t, "consumed", getRefundRecordStatus(t, "req-old-consumed"))
	require.Error(t, DB.Where("request_id = ?", "req-old-refunded").First(&SubscriptionPreConsumeRecord{}).Error)
	require.Error(t, DB.Where("request_id = ?", "req-old-settled").First(&SubscriptionPreConsumeRecord{}).Error)
}

func TestRefundSubscriptionPreConsumeUnknownRequestFails(t *testing.T) {
	truncateTables(t)
	require.Error(t, RefundSubscriptionPreConsume("req-missing"))
	require.Error(t, RefundSubscriptionPreConsume(""))
}
