package health

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type State string

const (
	StateHealthy   State = "healthy"
	StateUnhealthy State = "unhealthy"
)

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
	states map[string]Snapshot
	ttl    time.Duration
	group  singleflight.Group
}

func NewTable(ttl time.Duration) *Table {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &Table{states: make(map[string]Snapshot), ttl: ttl}
}

func (t *Table) MarkUnhealthy(key Key, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := key.String()
	cur := t.states[id]
	cur.State = StateUnhealthy
	cur.Until = now.Add(t.ttl)
	t.states[id] = cur
}

func (t *Table) Snapshot(key Key, now time.Time) Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := key.String()
	cur := t.states[id]
	if cur.State == StateUnhealthy && now.After(cur.Until) {
		cur.State = StateHealthy
		t.states[id] = cur
	}
	if cur.State == "" {
		cur.State = StateHealthy
	}
	return cur
}

func (t *Table) WaitOrProbe(ctx context.Context, key Key, now time.Time, probe func(context.Context) error) (Snapshot, error) {
	snap := t.Snapshot(key, now)
	if snap.State == StateHealthy {
		return snap, nil
	}
	_, err, _ := t.group.Do(key.String(), func() (any, error) {
		t.mu.Lock()
		cur := t.states[key.String()]
		cur.ProbeRuns++
		t.states[key.String()] = cur
		t.mu.Unlock()
		if err := probe(ctx); err != nil {
			t.MarkUnhealthy(key, time.Now())
			return nil, err
		}
		t.mu.Lock()
		cur = t.states[key.String()]
		cur.State = StateHealthy
		cur.Until = time.Time{}
		t.states[key.String()] = cur
		t.mu.Unlock()
		return nil, nil
	})
	return t.Snapshot(key, time.Now()), err
}
