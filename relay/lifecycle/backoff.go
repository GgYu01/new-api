package lifecycle

import "time"

func RetryBackoff(attemptIndex int) time.Duration {
	if attemptIndex < 0 {
		attemptIndex = 0
	}
	d := time.Duration(250*(1<<attemptIndex)) * time.Millisecond
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}
