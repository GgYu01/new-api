package common

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLargeBodyAdmissionAllowsSixteenAndQueuesSeventeenth(t *testing.T) {
	const (
		largeBodyThreshold = int64(8 << 20)
		bodyBytes          = largeBodyThreshold + 1
		largeBodyLimit     = 16
	)
	configureBodyStorageDiskCache(t, 1, 256)
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: largeBodyThreshold,
		MaxInFlight:    largeBodyLimit,
		WaitTimeout:    time.Second,
	})

	type result struct {
		storage BodyStorage
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, largeBodyLimit)
	var workers sync.WaitGroup
	for range largeBodyLimit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			storage, err := CreateBodyStorageFromReaderWithAdmission(
				context.Background(),
				admission,
				&fixedSizeZeroReader{remaining: bodyBytes},
				bodyBytes,
				bodyBytes,
			)
			results <- result{storage: storage, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	storages := make([]BodyStorage, 0, largeBodyLimit)
	for got := range results {
		require.NoError(t, got.err)
		require.NotNil(t, got.storage)
		storages = append(storages, got.storage)
	}
	require.Len(t, storages, largeBodyLimit)
	stats := admission.Stats()
	require.Equal(t, largeBodyLimit, stats.InFlight)
	require.Equal(t, largeBodyLimit, stats.PeakInFlight)

	seventeenthDone := make(chan result, 1)
	go func() {
		storage, err := CreateBodyStorageFromReaderWithAdmission(
			context.Background(),
			admission,
			&fixedSizeZeroReader{remaining: bodyBytes},
			bodyBytes,
			bodyBytes,
		)
		seventeenthDone <- result{storage: storage, err: err}
	}()
	require.Eventually(t, func() bool {
		return admission.Stats().Waiting == 1
	}, time.Second, time.Millisecond)
	select {
	case got := <-seventeenthDone:
		if got.storage != nil {
			_ = got.storage.Close()
		}
		t.Fatalf("seventeenth request bypassed the large-body lane: %v", got.err)
	default:
	}

	require.NoError(t, storages[0].Close())
	got := <-seventeenthDone
	require.NoError(t, got.err)
	require.NotNil(t, got.storage)
	storages[0] = got.storage

	for _, storage := range storages {
		require.NoError(t, storage.Close())
	}
	require.Eventually(t, func() bool {
		stats = admission.Stats()
		return stats.InFlight == 0 && stats.Waiting == 0
	}, time.Second, time.Millisecond)
}

func TestLargeBodyAdmissionChunkedWaitCancellationReleasesAllCounters(t *testing.T) {
	configureBodyStorageDiskCache(t, 1, 16)
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: 8,
		MaxInFlight:    1,
		WaitTimeout:    time.Second,
	})

	first, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		bytes.NewReader([]byte("123456789")),
		-1,
		64,
	)
	require.NoError(t, err)
	require.Equal(t, 1, admission.Stats().InFlight)

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		storage, createErr := CreateBodyStorageFromReaderWithAdmission(
			ctx,
			admission,
			bytes.NewReader([]byte("abcdefghi")),
			-1,
			64,
		)
		if storage != nil {
			_ = storage.Close()
		}
		secondDone <- createErr
	}()
	require.Eventually(t, func() bool {
		return admission.Stats().Waiting == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-secondDone, context.Canceled)
	require.Eventually(t, func() bool {
		stats := admission.Stats()
		return stats.InFlight == 1 && stats.Waiting == 0
	}, time.Second, time.Millisecond)

	require.NoError(t, first.Close())
	stats := admission.Stats()
	assert.Zero(t, stats.InFlight)
	assert.Zero(t, stats.Waiting)
	assert.Equal(t, uint64(1), stats.CanceledTotal)
}

func TestLargeBodyAdmissionReturnsTypedOverloadAfterBoundedWait(t *testing.T) {
	configureBodyStorageDiskCache(t, 1, 16)
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: 8,
		MaxInFlight:    1,
		WaitTimeout:    20 * time.Millisecond,
	})

	first, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		bytes.NewReader([]byte("123456789")),
		9,
		64,
	)
	require.NoError(t, err)
	defer first.Close()

	second, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		bytes.NewReader([]byte("abcdefghi")),
		9,
		64,
	)
	require.Nil(t, second)
	require.ErrorIs(t, err, ErrLargeBodyAdmissionTimeout)
	stats := admission.Stats()
	assert.Equal(t, 1, stats.InFlight)
	assert.Zero(t, stats.Waiting)
	assert.Equal(t, uint64(1), stats.TimeoutTotal)
}

func TestLargeBodyAdmissionDoesNotLimitBodiesAtThreshold(t *testing.T) {
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: 8,
		MaxInFlight:    1,
		WaitTimeout:    time.Second,
	})

	for range 3 {
		storage, err := CreateBodyStorageFromReaderWithAdmission(
			context.Background(),
			admission,
			io.LimitReader(bytes.NewReader([]byte("12345678extra")), 8),
			-1,
			8,
		)
		require.NoError(t, err)
		require.NoError(t, storage.Close())
	}
	stats := admission.Stats()
	assert.Zero(t, stats.InFlight)
	assert.Zero(t, stats.PeakInFlight)
}

func TestLargeBodyAdmissionPreservesSourceReadErrorAndReleasesTicket(t *testing.T) {
	configureBodyStorageDiskCache(t, 1, 16)
	sourceErr := errors.New("source failed")
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: 8,
		MaxInFlight:    1,
		WaitTimeout:    time.Second,
	})

	storage, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		&failingAfterReader{remaining: 9, err: sourceErr},
		-1,
		64,
	)
	require.Nil(t, storage)
	require.ErrorIs(t, err, sourceErr)
	stats := admission.Stats()
	assert.Zero(t, stats.InFlight)
	assert.Zero(t, stats.Waiting)
}

func TestLargeBodyAdmissionRejectsKnownOversizeBeforeWaitingOrReading(t *testing.T) {
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: 8,
		MaxInFlight:    1,
		WaitTimeout:    20 * time.Millisecond,
	})
	first, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		bytes.NewReader([]byte("123456789")),
		9,
		64,
	)
	require.NoError(t, err)
	defer first.Close()

	reader := &readCountingReader{}
	storage, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		reader,
		65,
		64,
	)
	require.Nil(t, storage)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)
	assert.Zero(t, reader.readCalls)
	stats := admission.Stats()
	assert.Equal(t, 1, stats.InFlight)
	assert.Zero(t, stats.Waiting)
	assert.Zero(t, stats.TimeoutTotal)
}

func TestGetRequestBodyAppliesLargeBodyAdmissionToLegacyCachedBody(t *testing.T) {
	admission := NewLargeBodyAdmission(LargeBodyAdmissionConfig{
		ThresholdBytes: 8,
		MaxInFlight:    1,
		WaitTimeout:    20 * time.Millisecond,
	})
	previous := globalLargeBodyAdmission.Swap(admission)
	t.Cleanup(func() {
		globalLargeBodyAdmission.Store(previous)
	})

	first, err := CreateBodyStorageFromReaderWithAdmission(
		context.Background(),
		admission,
		bytes.NewReader([]byte("123456789")),
		9,
		64,
	)
	require.NoError(t, err)
	defer first.Close()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "http://example.test/v1/responses", nil)
	c.Set(KeyRequestBody, []byte("abcdefghi"))
	storage, err := GetBodyStorage(c)
	require.Nil(t, storage)
	require.ErrorIs(t, err, ErrLargeBodyAdmissionTimeout)
	assert.Nil(t, c.Value(KeyRequestBody), "failed legacy body admission must release the cached byte slice")
}
