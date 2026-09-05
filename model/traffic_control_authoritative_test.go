package model

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useTrafficControlTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.SetTrafficControlPersistedRevision(0)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return db
}

func TestUpdateTrafficControlAuthoritativeBumpsRevisionAndPersists(t *testing.T) {
	db := useTrafficControlTestDB(t)

	cfg := common.DefaultTrafficControlConfig()
	cfg.MaxActiveRequests = 180

	metrics, err := UpdateTrafficControlAuthoritative(cfg, nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), metrics.Revision)
	assert.Equal(t, int64(180), metrics.Config.MaxActiveRequests)

	var revOpt Option
	require.NoError(t, db.Where("key = ?", common.TrafficControlRevisionOption).First(&revOpt).Error)
	assert.Equal(t, "1", revOpt.Value)

	var activeOpt Option
	require.NoError(t, db.Where("key = ?", common.TrafficControlMaxActiveOption).First(&activeOpt).Error)
	assert.Equal(t, "180", activeOpt.Value)

	// Second authoritative update with matching expectedRevision=1 succeeds and bumps to 2
	cfg2 := cfg
	cfg2.MaxActiveRequests = 200
	expectedRev := uint64(1)
	metrics2, err := UpdateTrafficControlAuthoritative(cfg2, &expectedRev)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), metrics2.Revision)
	assert.Equal(t, int64(200), metrics2.Config.MaxActiveRequests)

	// Stale update with expectedRevision=1 fails with ErrTrafficControlConflict
	cfg3 := cfg
	cfg3.MaxActiveRequests = 220
	staleRev := uint64(1)
	_, err = UpdateTrafficControlAuthoritative(cfg3, &staleRev)
	assert.ErrorIs(t, err, ErrTrafficControlConflict)
}

func TestUpdateTrafficControlAuthoritativeConcurrentBarrier(t *testing.T) {
	_ = useTrafficControlTestDB(t)

	// Seed revision 1
	cfg1 := common.DefaultTrafficControlConfig()
	cfg1.MaxActiveRequests = 180
	m, err := UpdateTrafficControlAuthoritative(cfg1, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), m.Revision)

	// Two concurrent writers both based on revision 1
	baseRev := uint64(1)
	var wg sync.WaitGroup
	barrier := make(chan struct{})

	var err1, err2 error
	var m1, m2 common.TrafficControlMetrics

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-barrier
		c := common.DefaultTrafficControlConfig()
		c.MaxActiveRequests = 150
		m1, err1 = UpdateTrafficControlAuthoritative(c, &baseRev)
	}()

	go func() {
		defer wg.Done()
		<-barrier
		c := common.DefaultTrafficControlConfig()
		c.MaxActiveRequests = 160
		m2, err2 = UpdateTrafficControlAuthoritative(c, &baseRev)
	}()

	close(barrier)
	wg.Wait()

	// Exactly one succeeds, the other fails with ErrTrafficControlConflict
	successes := 0
	conflicts := 0

	if err1 == nil {
		successes++
		assert.Equal(t, uint64(2), m1.Revision)
	} else if assert.ErrorIs(t, err1, ErrTrafficControlConflict) {
		conflicts++
	}

	if err2 == nil {
		successes++
		assert.Equal(t, uint64(2), m2.Revision)
	} else if assert.ErrorIs(t, err2, ErrTrafficControlConflict) {
		conflicts++
	}

	assert.Equal(t, 1, successes, "exactly one update must succeed")
	assert.Equal(t, 1, conflicts, "the concurrent stale update must receive 409 conflict")
}

func TestLoadOptionsFromDatabaseReadsFullSnapshotAndInitializesRevision(t *testing.T) {
	db := useTrafficControlTestDB(t)

	// Seed options into DB directly (simulating persistent state on startup)
	options := []Option{
		{Key: common.TrafficControlEnabledOption, Value: "true"},
		{Key: common.TrafficControlModeOption, Value: "concurrency"},
		{Key: common.TrafficControlMaxActiveOption, Value: "180"},
		{Key: common.TrafficControlRevisionOption, Value: "5"},
	}
	require.NoError(t, db.Create(&options).Error)

	loadOptionsFromDatabase()

	metrics := common.GetTrafficControlMetrics()
	assert.Equal(t, int64(180), metrics.Config.MaxActiveRequests)
	assert.Equal(t, uint64(5), metrics.Revision)
	assert.Equal(t, common.TrafficControlModeConcurrency, metrics.Config.Mode)
}

func TestUpdateOptionPropagatesDBError(t *testing.T) {
	_ = useTrafficControlTestDB(t)

	// Close database to trigger an error
	sqlDB, err := DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	err = UpdateOption("SomeKey", "SomeValue")
	assert.Error(t, err, "UpdateOption must propagate database errors instead of swallowing them")
}
