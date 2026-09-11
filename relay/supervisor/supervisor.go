package supervisor

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/relay/health"
	"github.com/QuantumNous/new-api/relay/transportpath"
)

const (
	MaxAttempts                   = 8
	MaxCredentials                = 4
	MaxSamePathCredentialAttempts = 2
	RecoveryBudget                = 30 * time.Minute
)

type Attempt struct {
	Path    transportpath.ID
	Channel int
	// CredentialFP is an opaque fingerprint, never a credential secret. Legacy
	// callers without a fingerprint use the channel as their credential identity.
	CredentialFP string
}

type Result struct {
	Attempts      int
	Probes        int
	LastPath      transportpath.ID
	Hedged        bool
	SamePathSwap  bool
	SucceededPath transportpath.ID
}

type Runner struct{ Health *health.Table }

func New(table *health.Table) *Runner {
	if table == nil {
		table = health.NewTable(5 * time.Second)
	}
	return &Runner{Health: table}
}

// Run owns one logical root and dispatches only serially. Independent roots may
// share a Runner; their concurrency is not hedging within a request.
func (r *Runner) Run(ctx context.Context, attempts []Attempt, dispatch func(context.Context, Attempt) error) Result {
	deadline := time.Now().Add(RecoveryBudget)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	out := Result{}
	credentials := make(map[string]bool)
	type pathCredential struct {
		path       transportpath.ID
		credential string
	}
	pairs := make(map[pathCredential]int)
	var lastPath transportpath.ID
	for _, attempt := range attempts {
		if ctx.Err() != nil || !time.Now().Before(deadline) || out.Attempts >= MaxAttempts {
			return out
		}
		credential := attempt.CredentialFP
		if credential == "" {
			credential = "channel:" + strconv.Itoa(attempt.Channel)
		}
		pair := pathCredential{attempt.Path, credential}
		if pairs[pair] >= MaxSamePathCredentialAttempts || !credentials[credential] && len(credentials) >= MaxCredentials {
			continue
		}
		if lastPath != "" && attempt.Path == lastPath {
			out.SamePathSwap = true
		}
		key := health.Key{TransportPathID: string(attempt.Path)}
		snap := r.Health.Snapshot(key, time.Now())
		dispatched, err := r.Health.Dispatch(ctx, key, time.Now(), func(ctx context.Context) error {
			out.Attempts++
			out.LastPath = attempt.Path
			if snap.State == health.StateUnhealthy {
				out.Probes++
			}
			credentials[credential] = true
			pairs[pair]++
			return dispatch(ctx, attempt)
		})
		if dispatched && err == nil {
			out.SucceededPath = attempt.Path
			return out
		}
		lastPath = attempt.Path
	}
	return out
}
