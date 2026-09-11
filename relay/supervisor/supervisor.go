package supervisor

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/relay/health"
	"github.com/QuantumNous/new-api/relay/transportpath"
)

type Attempt struct {
	Path    transportpath.ID
	Channel int
}

type Result struct {
	Attempts      int
	Probes        int
	LastPath      transportpath.ID
	Hedged        bool
	SamePathSwap  bool
	SucceededPath transportpath.ID
}

type Runner struct {
	Health   *health.Table
	inflight atomic.Int32
}

func New(table *health.Table) *Runner {
	if table == nil {
		table = health.NewTable(5 * time.Second)
	}
	return &Runner{Health: table}
}

// Run serial attempts. Callers must not invoke it concurrently for one request.
func (r *Runner) Run(ctx context.Context, attempts []Attempt, dispatch func(context.Context, Attempt) error) Result {
	out := Result{}
	if r.inflight.Add(1) != 1 {
		out.Hedged = true
	}
	defer r.inflight.Add(-1)

	var lastPath transportpath.ID
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return out
		}
		if lastPath != "" && attempt.Path == lastPath && out.Attempts > 0 {
			out.SamePathSwap = true
		}
		key := health.Key{TransportPathID: string(attempt.Path)}
		snap := r.Health.Snapshot(key, time.Now())
		if snap.State == health.StateUnhealthy {
			_, _ = r.Health.WaitOrProbe(ctx, key, time.Now(), func(context.Context) error {
				out.Probes++
				return dispatch(ctx, attempt)
			})
			out.Attempts++
			out.LastPath = attempt.Path
			if r.Health.Snapshot(key, time.Now()).State == health.StateHealthy {
				out.SucceededPath = attempt.Path
				return out
			}
			lastPath = attempt.Path
			continue
		}
		out.Attempts++
		out.LastPath = attempt.Path
		if err := dispatch(ctx, attempt); err != nil {
			r.Health.MarkUnhealthy(key, time.Now())
			lastPath = attempt.Path
			continue
		}
		out.SucceededPath = attempt.Path
		return out
	}
	return out
}
