package lifecycle

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/logger"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

type heartbeatState struct {
	mu       sync.Mutex
	stop     chan struct{}
	done     chan struct{}
	started  bool
	interval time.Duration
}

func (lr *LogicalRequest) heartbeat() *heartbeatState {
	// stored on writer side via lr field to keep one owner
	return lr.ensureHeartbeat()
}

func (lr *LogicalRequest) ensureHeartbeat() *heartbeatState {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.hb != nil {
		return lr.hb
	}
	lr.hb = &heartbeatState{}
	return lr.hb
}

func (lr *LogicalRequest) StartHeartbeat(c *gin.Context, interval time.Duration) {
	if lr == nil || !lr.stream {
		return
	}
	if interval <= 0 {
		interval = lr.cfg.SSEHeartbeatInterval
	}
	if interval <= 0 {
		interval = DefaultSSEHeartbeatInterval
	}
	hb := lr.ensureHeartbeat()
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if hb.started {
		return
	}
	if lr.writer != nil {
		lr.writer.PrepareSSEHeaders()
	}
	hb.started = true
	hb.interval = interval
	hb.stop = make(chan struct{})
	hb.done = make(chan struct{})
	stop := hb.stop
	done := hb.done
	gopool.Go(func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if lr.rootCtx.Err() != nil {
					return
				}
				if lr.TerminalSent() {
					return
				}
				if lr.writer == nil {
					return
				}
				if err := lr.writer.WriteKeepalive(); err != nil {
					if c != nil {
						logger.LogDebug(c, "SSE root heartbeat write failed: %s", err.Error())
					}
					return
				}
			case <-stop:
				return
			case <-lr.rootCtx.Done():
				return
			}
		}
	})
}

func (lr *LogicalRequest) StopHeartbeat() {
	if lr == nil {
		return
	}
	lr.mu.Lock()
	hb := lr.hb
	lr.mu.Unlock()
	if hb == nil {
		return
	}
	hb.mu.Lock()
	if !hb.started {
		hb.mu.Unlock()
		return
	}
	select {
	case <-hb.stop:
	default:
		close(hb.stop)
	}
	done := hb.done
	hb.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (lr *LogicalRequest) HeartbeatRunning() bool {
	if lr == nil {
		return false
	}
	lr.mu.Lock()
	hb := lr.hb
	lr.mu.Unlock()
	if hb == nil {
		return false
	}
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.started
}
