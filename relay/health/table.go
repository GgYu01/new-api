package health

import (
	"context"
	"errors"
	"sync"
	"time"
)

type State string

const (
	StateHealthy   State = "healthy"
	StateUnhealthy State = "unhealthy"
)

var ErrCooldown = errors.New("transport path is cooling down")

type Key struct {
	TransportPathID string
	Provider        string
	ModelDomain     string
	CredentialFP    string
}

func (k Key) String() string {
	return k.TransportPathID + "|" + k.Provider + "|" + k.ModelDomain + "|" + k.CredentialFP
}

type Snapshot struct {
	State     State
	Until     time.Time
	ProbeRuns int
}

type Table struct {
	mu     sync.Mutex
	states map[Key]Snapshot
	ttl    time.Duration
	// A per-key singleflight admission gate. Unlike a shared dispatch result,
	// this gate never mistakes another logical request's success for our own.
	flights map[Key]chan struct{}
}

func NewTable(ttl time.Duration) *Table {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &Table{states: make(map[Key]Snapshot), ttl: ttl, flights: make(map[Key]chan struct{})}
}

func (t *Table) MarkUnhealthy(key Key, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := t.states[key]
	cur.State = StateUnhealthy
	cur.Until = now.Add(t.ttl)
	t.states[key] = cur
}

func (t *Table) Snapshot(key Key, now time.Time) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := t.states[key]
	// Expiry permits a probe; it is not evidence that the path recovered.
	if cur.State == "" {
		cur.State = StateHealthy
	}
	return cur
}

func (t *Table) WaitOrProbe(ctx context.Context, key Key, now time.Time, probe func(context.Context) error) (Snapshot, error) {
	_, err := t.run(ctx, key, now, false, probe)
	return t.Snapshot(key, now), err
}

// Dispatch admits an actual request, including the first request on an unknown
// path. It returns whether this caller dispatched; waiters never spend an
// attempt or credential merely by observing another caller's probe.
func (t *Table) Dispatch(ctx context.Context, key Key, now time.Time, dispatch func(context.Context) error) (bool, error) {
	return t.run(ctx, key, now, true, dispatch)
}

func (t *Table) run(ctx context.Context, key Key, now time.Time, request bool, dispatch func(context.Context) error) (bool, error) {
	started := time.Now()
	currentTime := func() time.Time { return now.Add(time.Since(started)) }
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		t.mu.Lock()
		if done := t.flights[key]; done != nil {
			t.mu.Unlock()
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-done:
			}
			continue
		}
		cur := t.states[key]
		if cur.State == StateUnhealthy && currentTime().Before(cur.Until) {
			t.mu.Unlock()
			return false, ErrCooldown
		}
		if cur.State != StateUnhealthy && !request {
			t.mu.Unlock()
			return false, nil
		}
		if cur.State == StateHealthy {
			t.mu.Unlock()
			err := dispatch(ctx)
			if err != nil {
				t.MarkUnhealthy(key, currentTime())
			}
			return true, err
		}
		// Unknown paths and expired unhealthy paths get exactly one trial. The
		// admission decision and publication of the flight are atomic.
		done := make(chan struct{})
		t.flights[key] = done
		if cur.State == StateUnhealthy {
			cur.ProbeRuns++
		}
		t.states[key] = cur
		t.mu.Unlock()

		err := dispatch(ctx)
		t.mu.Lock()
		cur = t.states[key]
		if err != nil {
			cur.State = StateUnhealthy
			cur.Until = currentTime().Add(t.ttl)
		} else {
			cur.State = StateHealthy
			cur.Until = time.Time{}
		}
		t.states[key] = cur
		delete(t.flights, key)
		close(done)
		t.mu.Unlock()
		return true, err
	}
}
