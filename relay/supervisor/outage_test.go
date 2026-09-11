package supervisor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/relay/health"
	"github.com/QuantumNous/new-api/relay/transportpath"
)

// Two hundred logical roots, not a wall-clock load test. Each root uses an
// independent path serially after the dead transport's admission is denied.
func TestG010OutageAmplification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := health.NewTable(time.Second)
		runner := New(table)
		const clients = 200
		var dead, independent atomic.Int32
		for window := 0; window < 2; window++ {
			if window > 0 {
				<-time.NewTimer(time.Second).C
			}
			before := dead.Load()
			for i := 0; i < clients; i++ {
				calls, active := 0, 0
				res := runner.Run(context.Background(), []Attempt{
					{Path: transportpath.LegacyLocal8317, Channel: 1},
					{Path: transportpath.LegacyLocal8317, Channel: 9},
					{Path: transportpath.FallbackSSH, Channel: 1},
				}, func(ctx context.Context, a Attempt) error {
					active++
					defer func() { active-- }()
					if active != 1 {
						t.Fatal("overlapping root attempts")
					}
					calls++
					if a.Path == transportpath.LegacyLocal8317 {
						dead.Add(1)
						return errors.New("connection refused")
					}
					independent.Add(1)
					return nil
				})
				if res.Hedged || res.SucceededPath != transportpath.FallbackSSH {
					t.Fatalf("root %d: %+v", i, res)
				}
				if res.Attempts != calls || calls > 8 {
					t.Fatalf("root %d reported=%d dispatched=%d", i, res.Attempts, calls)
				}
			}
			probes := dead.Load() - before
			if probes != 1 {
				t.Fatalf("window=%d dead upstream attempts=%d for %d roots; want 1", window, probes, clients)
			}
		}
		t.Logf("G010-C001 logical_requests=%d dead_upstream_attempts=%d independent_attempts=%d outage_amplification=%.5f total_amplification=%.5f", 2*clients, dead.Load(), independent.Load(), float64(dead.Load())/(2*clients), float64(dead.Load()+independent.Load())/(2*clients))
	})
}

func TestG010SupervisorAttemptAndCredentialCaps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels int
		want     int
	}{
		{"eight attempts", 4, 8}, {"four credentials", 20, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := New(health.NewTable(time.Minute))
			var attempts []Attempt
			for i := 0; i < 20; i++ {
				attempts = append(attempts, Attempt{Path: transportpath.ID(fmt.Sprintf("path-%d", i)), Channel: i % tc.channels})
			}
			calls := 0
			res := runner.Run(context.Background(), attempts, func(context.Context, Attempt) error { calls++; return errors.New("connection refused") })
			if calls != tc.want || res.Attempts != calls {
				t.Fatalf("dispatched=%d result=%+v want=%d", calls, res, tc.want)
			}
		})
	}
}

func TestG010ConcurrentOutageSingleflight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const clients = 200
		runner := New(health.NewTable(time.Minute))
		started, release := make(chan struct{}), make(chan struct{})
		var dead, independent atomic.Int32
		var wg sync.WaitGroup
		results := make([]Result, clients)
		calls := make([]int, clients)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for i := 0; i < clients; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				active := 0
				results[i] = runner.Run(ctx, []Attempt{{Path: "dead", Channel: 1}, {Path: "independent", Channel: 2}}, func(ctx context.Context, a Attempt) error {
					active++
					defer func() { active-- }()
					if active != 1 {
						t.Error("overlapping attempts within a root")
					}
					calls[i]++
					if a.Path == "dead" {
						if dead.Add(1) == 1 {
							close(started)
						}
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						return errors.New("connection refused")
					}
					independent.Add(1)
					return nil
				})
			}()
		}
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("dead probe never started")
		}
		synctest.Wait()
		if dead.Load() != 1 || independent.Load() != 0 {
			t.Fatalf("while probe blocked: dead=%d independent=%d", dead.Load(), independent.Load())
		}
		close(release)
		wg.Wait()
		for i, res := range results {
			if res.Hedged || res.SucceededPath != "independent" || res.Attempts != calls[i] || calls[i] > 2 {
				t.Fatalf("root %d: result=%+v dispatched=%d", i, res, calls[i])
			}
		}
		if dead.Load() != 1 || independent.Load() != clients {
			t.Fatalf("dead=%d independent=%d", dead.Load(), independent.Load())
		}
		t.Logf("G010-C001 concurrent_logical_requests=%d dead_upstream_attempts=%d independent_attempts=%d outage_amplification=%.5f total_amplification=%.5f", clients, dead.Load(), independent.Load(), float64(dead.Load())/clients, float64(dead.Load()+independent.Load())/clients)
	})
}

func TestG010SupervisorCredentialFingerprints(t *testing.T) {
	runner := New(health.NewTable(time.Minute))
	attempts := make([]Attempt, 20)
	for i := range attempts {
		attempts[i] = Attempt{Path: transportpath.ID(fmt.Sprintf("path-%d", i)), Channel: 1, CredentialFP: fmt.Sprintf("credential-%d", i)}
	}
	credentials := make(map[string]bool)
	res := runner.Run(context.Background(), attempts, func(ctx context.Context, a Attempt) error {
		credentials[a.CredentialFP] = true
		return errors.New("connection refused")
	})
	if res.Attempts != 4 || len(credentials) != 4 {
		t.Fatalf("attempts=%d credentials=%d", res.Attempts, len(credentials))
	}
}

func TestG010SameBadPathCredentialCapSurvivesCooldownExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := New(health.NewTable(time.Second))
		var attempts []Attempt
		for i := 0; i < 10; i++ {
			attempts = append(attempts, Attempt{Path: "bad", CredentialFP: "same-credential"}, Attempt{Path: transportpath.ID(fmt.Sprintf("alternate-%d", i)), CredentialFP: "same-credential"})
		}
		bad := 0
		res := runner.Run(context.Background(), attempts, func(ctx context.Context, a Attempt) error {
			if a.Path == "bad" {
				bad++
			} else {
				<-time.NewTimer(time.Second).C
			} // Expire the preceding bad-path cooldown in virtual time.
			return errors.New("connection refused")
		})
		if bad != 2 || res.Attempts != 8 {
			t.Fatalf("same pair=%d total attempts=%d", bad, res.Attempts)
		}
	})
}

func TestG010SupervisorThirtyMinuteBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := New(health.NewTable(time.Second))
		calls := 0
		runner.Run(context.Background(), []Attempt{{Path: "dead", Channel: 1}, {Path: "alternate", Channel: 2}}, func(ctx context.Context, a Attempt) error {
			calls++
			<-time.NewTimer(30 * time.Minute).C // Virtual time: the budget itself is under test.
			return errors.New("connection refused")
		})
		if calls != 1 {
			t.Fatalf("dispatched=%d after 30m budget; want 1", calls)
		}
	})
}
