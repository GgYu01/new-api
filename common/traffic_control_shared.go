package common

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

// SharedPermitStore provides cross-instance coordination for concurrency leases and rate limits.
type SharedPermitStore interface {
	AcquireLease(ctx context.Context, leaseID string, maxActive int64, ttl time.Duration) (bool, int64, time.Duration, error)
	RenewLease(ctx context.Context, leaseID string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, leaseID string) error
	ClusterActive(ctx context.Context) (int64, error)
	AcquireRPMToken(ctx context.Context, rpm int64, burst int64, now time.Time) (bool, time.Duration, error)
	Cleanup(ctx context.Context) error
}

const (
	defaultLeaseZSetKey = "newapi:traffic:concurrency:leases"
	defaultRPMBucketKey = "newapi:traffic:rpm:bucket"
)

// RedisSharedPermitStore coordinates leases across multiple NewAPI instances using Redis.
type RedisSharedPermitStore struct {
	client        *redis.Client
	leasesKey     string
	rpmKey        string
	defaultTTL    time.Duration
	renewInterval time.Duration
	mu            sync.Mutex
}

func NewRedisSharedPermitStore(client *redis.Client) *RedisSharedPermitStore {
	return NewRedisSharedPermitStoreWithKey(client, "newapi:traffic")
}

func NewRedisSharedPermitStoreWithKey(client *redis.Client, prefix string) *RedisSharedPermitStore {
	return &RedisSharedPermitStore{
		client:        client,
		leasesKey:     prefix + ":concurrency:leases",
		rpmKey:        prefix + ":rpm:bucket",
		defaultTTL:    30 * time.Second,
		renewInterval: 5 * time.Second,
	}
}

func (s *RedisSharedPermitStore) SetDefaultLeaseTTL(ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultTTL = ttl
	s.renewInterval = ttl / 3
	if s.renewInterval < 20*time.Millisecond {
		s.renewInterval = 20 * time.Millisecond
	}
}

func (s *RedisSharedPermitStore) GetDefaultLeaseTTL() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultTTL
}

func (s *RedisSharedPermitStore) GetRenewInterval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewInterval
}

// Lua script for concurrency admission:
// 1. Purge expired leases based on score (Unix timestamp in ms)
// 2. Count remaining active leases
// 3. If count >= maxActive, reject
// 4. Else insert lease with score = now_ms + ttl_ms
var acquireLeaseLua = redis.NewScript(`
local key = KEYS[1]
local now_ms = tonumber(ARGV[1])
local max_active = tonumber(ARGV[2])
local lease_id = ARGV[3]
local ttl_ms = tonumber(ARGV[4])

redis.call('ZREMRANGEBYSCORE', key, '-inf', now_ms)
local count = redis.call('ZCARD', key)
if count >= max_active then
    return {0, count}
end

local expire = now_ms + ttl_ms
redis.call('ZADD', key, expire, lease_id)
return {1, count + 1}
`)

func (s *RedisSharedPermitStore) AcquireLease(ctx context.Context, leaseID string, maxActive int64, ttl time.Duration) (bool, int64, time.Duration, error) {
	if s.client == nil {
		return false, 0, 0, fmt.Errorf("redis client is nil")
	}
	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	ttlMs := int64(ttl / time.Millisecond)
	if ttlMs <= 0 {
		ttlMs = int64(s.defaultTTL / time.Millisecond)
	}

	res, err := acquireLeaseLua.Run(ctx, s.client, []string{s.leasesKey}, nowMs, maxActive, leaseID, ttlMs).Result()
	if err != nil {
		return false, 0, 0, err
	}
	slice, ok := res.([]interface{})
	if !ok || len(slice) < 2 {
		return false, 0, 0, fmt.Errorf("unexpected redis lua result: %v", res)
	}
	admitted := slice[0].(int64) == 1
	active := slice[1].(int64)
	if !admitted {
		return false, active, time.Second, nil
	}
	return true, active, 0, nil
}

// Lua script for lease renewal:
// Extends expiration if lease still exists in ZSet
var renewLeaseLua = redis.NewScript(`
local key = KEYS[1]
local lease_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])

local score = redis.call('ZSCORE', key, lease_id)
if score then
    local new_expire = now_ms + ttl_ms
    redis.call('ZADD', key, new_expire, lease_id)
    return 1
end
return 0
`)

func (s *RedisSharedPermitStore) RenewLease(ctx context.Context, leaseID string, ttl time.Duration) (bool, error) {
	if s.client == nil {
		return false, fmt.Errorf("redis client is nil")
	}
	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	ttlMs := int64(ttl / time.Millisecond)
	if ttlMs <= 0 {
		ttlMs = int64(s.defaultTTL / time.Millisecond)
	}

	res, err := renewLeaseLua.Run(ctx, s.client, []string{s.leasesKey}, leaseID, nowMs, ttlMs).Result()
	if err != nil {
		return false, err
	}
	return res.(int64) == 1, nil
}

func (s *RedisSharedPermitStore) ReleaseLease(ctx context.Context, leaseID string) error {
	if s.client == nil {
		return nil
	}
	return s.client.ZRem(ctx, s.leasesKey, leaseID).Err()
}

func (s *RedisSharedPermitStore) ClusterActive(ctx context.Context) (int64, error) {
	if s.client == nil {
		return 0, nil
	}
	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	_ = s.client.ZRemRangeByScore(ctx, s.leasesKey, "-inf", fmt.Sprintf("%d", nowMs)).Err()
	return s.client.ZCard(ctx, s.leasesKey).Result()
}

// Lua script for distributed token bucket RPM
var acquireRPMTokenLua = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local rpm = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local rate = rpm / 60.0

local data = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens = tonumber(data[1])
local last_refill = tonumber(data[2])

if not tokens or not last_refill then
    tokens = burst
    last_refill = now
else
    local elapsed = math.max(0, now - last_refill)
    tokens = math.min(burst, tokens + elapsed * rate)
    last_refill = now
end

if tokens >= 1 then
    tokens = tokens - 1
    redis.call('HMSET', key, 'tokens', tokens, 'last_refill', last_refill)
    redis.call('EXPIRE', key, 120)
    return {1, 0}
else
    redis.call('HMSET', key, 'tokens', tokens, 'last_refill', last_refill)
    redis.call('EXPIRE', key, 120)
    local wait_ms = math.ceil(((1.0 - tokens) / rate) * 1000)
    return {0, wait_ms}
end
`)

func (s *RedisSharedPermitStore) AcquireRPMToken(ctx context.Context, rpm int64, burst int64, now time.Time) (bool, time.Duration, error) {
	if s.client == nil {
		return true, 0, nil
	}
	nowSec := float64(now.UnixNano()) / 1e9
	res, err := acquireRPMTokenLua.Run(ctx, s.client, []string{s.rpmKey}, nowSec, rpm, burst).Result()
	if err != nil {
		return false, 0, err
	}
	slice, ok := res.([]interface{})
	if !ok || len(slice) < 2 {
		return false, 0, fmt.Errorf("unexpected lua result: %v", res)
	}
	allowed := slice[0].(int64) == 1
	waitMs := slice[1].(int64)
	return allowed, time.Duration(waitMs) * time.Millisecond, nil
}

func (s *RedisSharedPermitStore) Cleanup(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	return s.client.Del(ctx, s.leasesKey, s.rpmKey).Err()
}

func generateInstanceID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "newapi-node"
	}
	return fmt.Sprintf("%s-%s", hostname, uuid.New().String()[:8])
}
