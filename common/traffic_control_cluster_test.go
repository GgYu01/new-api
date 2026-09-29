package common

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// TestMultiInstanceWithoutSharedStore_DemonstratesAmplification shows that without
// a shared permit store, N instances each admit MaxActiveRequests, multiplying
// the global ceiling by N.
func TestMultiInstanceWithoutSharedStore_DemonstratesAmplification(t *testing.T) {
	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeConcurrency
	cfg.MaxActiveRequests = 3 // small ceiling C = 3

	// Create 3 instances
	inst1, err := NewTrafficController(cfg, time.Now())
	require.NoError(t, err)
	inst2, err := NewTrafficController(cfg, time.Now())
	require.NoError(t, err)
	inst3, err := NewTrafficController(cfg, time.Now())
	require.NoError(t, err)

	now := time.Now()
	var allLeases []*TrafficLease

	// Each instance admits up to its local MaxActiveRequests (3)
	for i := 0; i < 3; i++ {
		l1, _, _ := inst1.Admit(now)
		require.NotNil(t, l1)
		allLeases = append(allLeases, l1)

		l2, _, _ := inst2.Admit(now)
		require.NotNil(t, l2)
		allLeases = append(allLeases, l2)

		l3, _, _ := inst3.Admit(now)
		require.NotNil(t, l3)
		allLeases = append(allLeases, l3)
	}

	// In the unpatched baseline, total in-flight across 3 instances is 9 (3x C)!
	require.Equal(t, 9, len(allLeases), "Unpatched controllers allow 3x C across 3 instances")

	for _, l := range allLeases {
		l.Release()
	}
}

// TestMultiInstanceWithSharedPermitStore_StrictCeiling verifies that when a shared
// permit store is wired, N instances strictly obey the global limit C without amplification.
func TestMultiInstanceWithSharedPermitStore_StrictCeiling(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available on 127.0.0.1:6379, skipping distributed test")
	}

	// Clear test keys
	testKeyPrefix := fmt.Sprintf("test:traffic:%d", time.Now().UnixNano())
	store := NewRedisSharedPermitStoreWithKey(rdb, testKeyPrefix)
	defer store.Cleanup(context.Background())

	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeConcurrency
	cfg.MaxActiveRequests = 5 // Global ceiling C = 5

	inst1, err := NewTrafficControllerWithStore(cfg, time.Now(), "inst-1", store)
	require.NoError(t, err)
	inst2, err := NewTrafficControllerWithStore(cfg, time.Now(), "inst-2", store)
	require.NoError(t, err)
	inst3, err := NewTrafficControllerWithStore(cfg, time.Now(), "inst-3", store)
	require.NoError(t, err)

	now := time.Now()
	var leases []*TrafficLease

	// Admit 2 on inst1, 2 on inst2, 1 on inst3 -> total 5
	l, _, _ := inst1.Admit(now)
	require.NotNil(t, l)
	leases = append(leases, l)

	l, _, _ = inst1.Admit(now)
	require.NotNil(t, l)
	leases = append(leases, l)

	l, _, _ = inst2.Admit(now)
	require.NotNil(t, l)
	leases = append(leases, l)

	l, _, _ = inst2.Admit(now)
	require.NotNil(t, l)
	leases = append(leases, l)

	l, _, _ = inst3.Admit(now)
	require.NotNil(t, l)
	leases = append(leases, l)

	// Now cluster active = 5 (equals MaxActiveRequests).
	// Next request on ANY instance must be REJECTED!
	rej1, _, reason1 := inst1.Admit(now)
	require.Nil(t, rej1, "inst1 should reject when cluster is at capacity")
	require.Equal(t, TrafficControlRejectActive, reason1)

	rej2, _, reason2 := inst2.Admit(now)
	require.Nil(t, rej2, "inst2 should reject when cluster is at capacity")
	require.Equal(t, TrafficControlRejectActive, reason2)

	rej3, _, reason3 := inst3.Admit(now)
	require.Nil(t, rej3, "inst3 should reject when cluster is at capacity")
	require.Equal(t, TrafficControlRejectActive, reason3)

	// Release 1 lease on inst1
	leases[0].Release()
	leases = leases[1:]

	// Now inst3 should be able to admit exactly 1 request
	admitted, _, _ := inst3.Admit(now)
	require.NotNil(t, admitted, "inst3 should admit after release")
	leases = append(leases, admitted)

	// Again, capacity reached
	rejAgain, _, _ := inst2.Admit(now)
	require.Nil(t, rejAgain)

	// Release all
	for _, remaining := range leases {
		remaining.Release()
	}

	clusterActive, err := store.ClusterActive(context.Background())
	require.NoError(t, err)
	require.Zero(t, clusterActive, "All leases released cleanly")
}

// TestRedisSharedPermitStore_CrashAndLateRelease verifies crash recovery (TTL purge)
// and late release (safe no-op without underflow).
func TestRedisSharedPermitStore_CrashAndLateRelease(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available on 127.0.0.1:6379, skipping distributed test")
	}

	testKeyPrefix := fmt.Sprintf("test:traffic:crash:%d", time.Now().UnixNano())
	store := NewRedisSharedPermitStoreWithKey(rdb, testKeyPrefix)
	// Override short TTL for testing crash
	store.SetDefaultLeaseTTL(300 * time.Millisecond)
	defer store.Cleanup(context.Background())

	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeConcurrency
	cfg.MaxActiveRequests = 1 // Limit = 1

	instDying, err := NewTrafficControllerWithStore(cfg, time.Now(), "inst-dying", store)
	require.NoError(t, err)

	instSurvivor, err := NewTrafficControllerWithStore(cfg, time.Now(), "inst-survivor", store)
	require.NoError(t, err)

	now := time.Now()
	dyingLease, _, _ := instDying.Admit(now)
	require.NotNil(t, dyingLease)

	// Immediate second attempt rejected
	rej, _, _ := instSurvivor.Admit(now)
	require.Nil(t, rej)

	// Stop heartbeat on dying lease and let 350ms elapse (simulating process crash / drop)
	close(dyingLease.stopRenew) // stop renewal
	time.Sleep(400 * time.Millisecond)

	// Survivor can now admit because expired lease is purged!
	survivorLease, _, _ := instSurvivor.Admit(time.Now())
	require.NotNil(t, survivorLease, "Survivor must acquire permit after dead instance lease expired")

	// Late release by dying instance must not panic or underflow Redis
	dyingLease.Release()

	// Survivor lease is still valid and held
	clusterActive, err := store.ClusterActive(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), clusterActive)

	survivorLease.Release()
	clusterActiveAfter, _ := store.ClusterActive(context.Background())
	require.Zero(t, clusterActiveAfter)
}
