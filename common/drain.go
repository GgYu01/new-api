package common

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// DrainHTTPServer stops accepting new connections and waits for in-flight
// requests (including long SSE streams) up to timeout. It never kills an
// active logical request before the budget expires; callers must align the
// container stop timeout with the same budget or the orchestrator will
// SIGKILL first.
func DrainHTTPServer(srv *http.Server, timeout time.Duration) error {
	if srv == nil {
		return fmt.Errorf("nil server")
	}
	if timeout <= 0 {
		timeout = LoadTimeoutLadder().GracefulDrainTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return srv.Shutdown(ctx)
}
